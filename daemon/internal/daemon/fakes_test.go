package daemon

// This file holds the doubles these tests share: one Discord fake that each
// failure mode is configured into, a capture for a component's log lines, and
// the wait-until-true helper every asynchronous test needs. They live together
// because they are one subject -- the scaffolding a test stands the daemon up
// on -- and because a new counter or a new failure mode is a change here rather
// than a change in six places.

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/diag"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/presence"
)

// fakeDiscord is the one Discord double these tests share. It records what the
// daemon does to the socket and what it sends down it, and a failure mode is a
// configuration applied to it rather than a type of its own: six types
// implementing the same five methods meant six copies of the same mutex and the
// same counters, and every counter added later had to be added six times.
//
// reachable and connected are separate because the daemon asks about them
// separately and the gap between them is a state that really occurs. reachable
// is whether the transport works at all: when it is false a Connect fails and a
// write fails. connected is what Connected reports, which is whether the daemon
// believes it is still holding a socket.
//
// The two disagree, and the disagreement is the failure worth modelling. A
// Discord that has quit is neither reachable nor connected. A socket that died
// under a live Discord is unreachable while still reported as connected -- the
// daemon has sent nothing since, so nothing has told it otherwise -- which is
// the state a restart leaves behind and the one run's quiet path exists to
// notice.
//
// Every field is guarded by mu, because the daemon runs concurrently with the
// test body under -race, and every read a test makes goes through an accessor
// for the same reason. The accessors return copies, never the fields.
type fakeDiscord struct {
	mu sync.Mutex

	reachable bool
	connected bool

	connects int
	closes   int
	sets     int
	clears   int
	accepted int

	activities []presence.Activity

	connectErr error
	setErr     error
	clearErr   error
}

// newFakeDiscord returns a Discord that is running and already holds a socket,
// which is the state most of these tests start from and the one the six fakes
// this replaced claimed by returning true unconditionally.
//
// connected starts true rather than false because these tests are about the run
// loop, not about the client's handshake: a test that hands the daemon a client
// is asserting what the daemon does with a working Discord, and one that wants a
// daemon which has never connected says so with refuseConnect.
func newFakeDiscord() *fakeDiscord {
	return &fakeDiscord{reachable: true, connected: true}
}

// refuseConnect makes Discord unreachable, so every Connect fails with err.
// This is a desktop session with no Discord client running: the failure is the
// absence, and it is the same failure every time rather than a new one.
func (f *fakeDiscord) refuseConnect(err error) *fakeDiscord {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reachable = false
	f.connected = false
	f.connectErr = err
	return f
}

// comesBack makes Discord reachable again, so one test can put an outage on
// either side of a working connection. It does not open the socket: the next
// Connect does that, and Connected stays false until one succeeds.
func (f *fakeDiscord) comesBack() *fakeDiscord {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reachable = true
	f.connectErr = nil
	return f
}

// goesAway takes Discord away while the daemon is holding a socket, which is
// what quitting the client leaves behind. It is refuseConnect under the name a
// test reads at the point it takes a working Discord away, rather than at the
// point it builds one that never worked.
func (f *fakeDiscord) goesAway() *fakeDiscord {
	return f.refuseConnect(errDiscordUnreachable)
}

// losesSocket kills the open socket while Discord itself stays up, which is
// what a restart leaves behind and what the other configurations cannot model:
// Connected still reports the socket the daemon is holding, and only the write
// that finds it dead reports the truth.
func (f *fakeDiscord) losesSocket() *fakeDiscord {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reachable = false
	f.connectErr = errDiscordUnreachable
	return f
}

// rejectActivity makes Discord turn down every activity with err. Discord read
// the request and refused its contents, so the socket is still good; only the
// payload is the problem.
func (f *fakeDiscord) rejectActivity(err error) *fakeDiscord {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setErr = err
	return f
}

// acceptsActivity lets the next activity through, so one test can put an
// accepted activity between two rejections.
func (f *fakeDiscord) acceptsActivity() *fakeDiscord {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setErr = nil
	return f
}

// rejectsActivity turns Discord against the payload again, undoing
// acceptsActivity.
func (f *fakeDiscord) rejectsActivity() *fakeDiscord {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setErr = errActivityRejected
	return f
}

// failsClearWith makes the clear fail for a reason other than Discord refusing
// it, which is the case that must still be answered by reconnecting rather than
// by keeping a socket Discord is still willing to read.
func (f *fakeDiscord) failsClearWith(err error) *fakeDiscord {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clearErr = err
	return f
}

// Connected reports whether a socket is open. The daemon consults it to decide
// whether to skip a publish that is already on screen and whether a dead
// connection needs dialling again, so it answers about the socket rather than
// about Discord's reachability.
func (f *fakeDiscord) Connected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected
}

// Connect counts the attempt and opens the socket when Discord is reachable.
func (f *fakeDiscord) Connect(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connects++
	if !f.reachable {
		return f.connectErr
	}
	f.connected = true
	return nil
}

// Close counts the teardown and drops the socket.
func (f *fakeDiscord) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes++
	f.connected = false
	return nil
}

// SetActivity records or refuses one activity. A configured refusal is returned
// without recording anything, because a payload Discord turned down is not one
// it published.
func (f *fakeDiscord) SetActivity(activity *presence.Activity) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets++
	if err := f.writeErr(); err != nil {
		return err
	}
	if f.setErr != nil {
		return f.setErr
	}
	f.accepted++
	f.activities = append(f.activities, *activity)
	return nil
}

// ClearActivity counts one clear and answers the way a write to this socket
// answers. It shares writeErr with SetActivity because a clear is a SET_ACTIVITY
// like any other: the socket does not care which payload killed it.
func (f *fakeDiscord) ClearActivity() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clears++
	if err := f.writeErr(); err != nil {
		return err
	}
	if f.clearErr != nil {
		return f.clearErr
	}
	return errActivityRejected
}

// writeErr reports what sending over this socket does, which is what ties the
// socket's health to the send rather than to a flag a test has to remember to
// set: the write that finds Discord gone is also the write that discovers the
// connection went with it, because the connection goes with the peer rather
// than only the one request.
//
// The caller holds mu.
func (f *fakeDiscord) writeErr() error {
	if f.reachable {
		return nil
	}
	f.connected = false
	return errDiscordWrite
}

// connectAttempts counts the Connect calls. A test asserts on this rather than
// on a report, because a daemon that retried in silence and one that never
// retried are the same from every other input it passes in.
func (f *fakeDiscord) connectAttempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connects
}

// teardowns counts the Close calls. The count rather than a flag, because the
// wrong behaviour is silent: reconnecting on a rejection looks exactly like a
// successful publish from everything else the daemon does.
func (f *fakeDiscord) teardowns() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closes
}

// activityCount counts the SetActivity calls, whether or not Discord took the
// payload. It is what tells a daemon that kept trying from one that stopped.
func (f *fakeDiscord) activityCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sets
}

// acceptedCount counts the activities Discord actually published, so a test can
// tell an accepted publish between two rejections from the rejections alone.
func (f *fakeDiscord) acceptedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.accepted
}

// clearCount counts the ClearActivity calls. Without it a daemon that cleared
// its presence and one that simply published nothing more are the same.
func (f *fakeDiscord) clearCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clears
}

// snapshot returns the published activities, oldest first. A copy, because
// the daemon appends to the real slice from its own goroutine.
func (f *fakeDiscord) snapshot() []presence.Activity {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]presence.Activity(nil), f.activities...)
}

// captureLog returns a buffer and a daemon logger writing into it, so a test
// can assert on what a component said without touching process-wide state. The
// buffer is safe to read while the daemon is still running because syncBuffer
// guards it, and the two are returned together because a test asserting on the
// lines needs both.
func captureLog() (*syncBuffer, diag.Logger) {
	logs := &syncBuffer{}
	return logs, diag.New(logs, "daemon")
}

// syncBuffer collects log output written from the daemon's goroutine. A plain
// bytes.Buffer would be read by the test while the daemon wrote to it, which
// -race reports as a data race on the buffer's own fields.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write appends one component's output under the mutex.
func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String returns what has been written so far, under the mutex, so a test can
// read the log while the daemon is still writing to it.
func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitFor polls condition until it holds, failing rather than hanging. The
// description names what was being waited for, so a timeout says which
// expectation went unmet rather than only that one did.
func waitFor(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out after 3s waiting for %s", description)
}

// waitForActivity polls until a recorded activity matches, returning it. It is
// waitFor with the search expressed as a predicate, so a test that needs the
// activity itself rather than a yes does not have to record it twice.
func (f *fakeDiscord) waitForActivity(t *testing.T, within time.Duration, match func(presence.Activity) bool) presence.Activity {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		for _, activity := range f.snapshot() {
			if match(activity) {
				return activity
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no activity matching the test's condition within %v", within)
	return presence.Activity{}
}
