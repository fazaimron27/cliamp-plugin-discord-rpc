package daemon

// This file tests the daemon's release check: the watcher's cadence and its
// report-once latching, the sentence it shares with the --check report, and the
// three answers that report can print. It is a file of its own rather than part of
// daemon_test.go because the watcher is deliberately not part of the run loop, so
// none of this needs a socket, a Cliamp session, or a Discord.

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/config"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/diag"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/release"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/version"
)

// fakeRelease is the release lookup these tests drive. A tag and an error are the
// whole answer; what is counted is when the daemon asked, because the behaviour
// under test is the cadence and the latching rather than the reply.
//
// block is how a test holds a lookup open: a request to a GitHub that accepted the
// connection and then said nothing, which is the state the watcher must not be
// trapped in.
type fakeRelease struct {
	mu     sync.Mutex
	tag    string
	err    error
	block  chan struct{}
	calls  int
	served int
}

// Latest answers with the tag and error the fake currently holds, counting the
// call and waiting on block when a test has set one.
func (f *fakeRelease) Latest(ctx context.Context) (string, error) {
	f.mu.Lock()
	f.calls++
	block := f.block
	tag, err := f.tag, f.err
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if err == nil {
		f.mu.Lock()
		f.served++
		f.mu.Unlock()
	}
	return tag, err
}

// serve makes every later lookup succeed with tag.
func (f *fakeRelease) serve(tag string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tag, f.err = tag, nil
}

// failWith makes every later lookup fail with err.
func (f *fakeRelease) failWith(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// callCount counts the lookups made. A watcher that checked once at startup and
// one that checked on every interval are the same from every other observable.
func (f *fakeRelease) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// servedCount counts the successful lookups, which is how a test knows the watcher
// has seen a working GitHub rather than only a failing one.
func (f *fakeRelease) servedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.served
}

// startWatcherWith runs the watcher on its own goroutine and returns a stop
// function that cancels it and waits for it to return. Every test here drives the
// interval down to milliseconds rather than waiting out the real one, and the wait
// in stop is what makes a watcher that ignored its context a failure rather than a
// hang.
func startWatcherWith(t *testing.T, check releaseChecker, logger diag.Logger) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchReleases(ctx, check, 5*time.Millisecond, logger)
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("watchReleases did not return after its context was cancelled")
		}
	}
}

// A daemon that has just started has not checked anything, so the first lookup
// cannot wait out the interval. A user service is restarted by a hand, a logout,
// and a reboot, and one that only asked after a full day would usually never ask
// at all.
//
// The once is the other half: the interval comes round every day for weeks, and a
// line per check would be thirty lines about one release.
func TestWatchReleasesReportsANewerReleaseOnceAtStartup(t *testing.T) {
	logs, logger := captureLog()
	check := &fakeRelease{tag: "v1.12.0"}

	stop := startWatcherWith(t, check, logger)
	waitFor(t, "the newer release to be reported", func() bool {
		return strings.Contains(logs.String(), "a newer release exists")
	})
	waitFor(t, "several checks to have run", func() bool { return check.callCount() >= 3 })
	stop()

	if got := strings.Count(logs.String(), "a newer release exists"); got != 1 {
		t.Fatalf("one release was reported %d times, want 1:\n%s", got, logs.String())
	}
}

// Neither half of an up-to-date pairing is news: this release, and a source build
// running ahead of it. The second is the common case for this project's own
// author, and a feature that nagged them would be turned off immediately.
func TestWatchReleasesStaysQuietOnAReleaseThatIsNotNewer(t *testing.T) {
	tests := []struct {
		name string
		tag  string
	}{
		{"this release", "v" + version.Number},
		{"a source build ahead of the release", "v1.0.0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			logs, logger := captureLog()
			check := &fakeRelease{tag: test.tag}

			stop := startWatcherWith(t, check, logger)
			waitFor(t, "several checks to have run", func() bool { return check.callCount() >= 3 })
			stop()

			if logs.String() != "" {
				t.Fatalf("an up-to-date daemon logged:\n%s", logs.String())
			}
		})
	}
}

// A machine that is offline, or behind a firewall that answers GitHub with
// nothing, is an ordinary state for a daemon that already treats an absent Discord
// as one. So an unmet lookup says so once and then stops, rather than writing the
// same line once a day forever.
func TestWatchReleasesReportsAFailedLookupOnce(t *testing.T) {
	logs, logger := captureLog()
	check := &fakeRelease{err: errors.New("release lookup failed")}

	stop := startWatcherWith(t, check, logger)
	waitFor(t, "the failure to be reported", func() bool {
		return strings.Contains(logs.String(), "check for a newer release")
	})
	waitFor(t, "several failed checks", func() bool { return check.callCount() >= 3 })
	stop()

	if got := strings.Count(logs.String(), "check for a newer release"); got != 1 {
		t.Fatalf("one outage was reported %d times, want 1:\n%s", got, logs.String())
	}
}

// The clause a latch loses if it never clears: after a lookup succeeds, an outage
// that follows it is news again. Without it the watcher would fall silent exactly
// when it began failing again, which is the state its report exists to surface.
func TestWatchReleasesReportsAnOutageAgainAfterItRecovers(t *testing.T) {
	logs, logger := captureLog()
	check := &fakeRelease{err: errors.New("release lookup failed")}

	stop := startWatcherWith(t, check, logger)
	waitFor(t, "the first outage to be reported", func() bool {
		return strings.Count(logs.String(), "check for a newer release") == 1
	})
	check.serve("v" + version.Number)
	waitFor(t, "a successful lookup", func() bool { return check.servedCount() >= 1 })
	check.failWith(errors.New("release lookup failed"))
	waitFor(t, "the second outage to be reported", func() bool {
		return strings.Count(logs.String(), "check for a newer release") == 2
	})
	stop()
}

// The check is on its own goroutine so a slow GitHub cannot delay the presence the
// loop publishes. That holds only while the goroutine obeys its context: one that
// sat on a hung request would leak a goroutine and a socket per daemon run and
// outlive the process it belonged to.
func TestWatchReleasesReturnsWhenTheContextIsCancelled(t *testing.T) {
	logs, logger := captureLog()
	check := &fakeRelease{block: make(chan struct{})}
	defer close(check.block)

	stop := startWatcherWith(t, check, logger)
	waitFor(t, "a lookup to be in flight", func() bool { return check.callCount() >= 1 })
	stop()

	if logs.String() != "" {
		t.Fatalf("a cancelled lookup was logged as a failure:\n%s", logs.String())
	}
}

// Both consumers of this sentence — the journal line and the --check report —
// print it verbatim, so it is asserted here rather than only through them. It
// names both halves because a release moves both of them, and a user who updates
// one has the mismatched pairing this project's version check exists to warn
// about.
func TestNewerReleaseWarningNamesBothHalves(t *testing.T) {
	warning := newerReleaseWarning("v1.12.0")

	for _, want := range []string{
		"v1.12.0",
		"v" + version.Number,
		"cliamp plugins install " + release.Repository + "@v1.12.0",
		release.RawBase + "v1.12.0/install.sh",
	} {
		if !strings.Contains(warning, want) {
			t.Errorf("warning omits %q:\n%s", want, warning)
		}
	}
}

// The report answers whether a newer release exists, and the three answers are
// three lines: an up-to-date daemon, one that is behind, and one that could not
// find out. The last is a warning rather than an "ok", because "I could not
// tell" is not "there is nothing newer".
//
// The patch case is the one that matters most. v1.11.1 against v1.11.0 is
// exactly the release this feature exists to notice, and the handshake's
// comparison, which ignores the patch component, calls those two halves equal.
//
// The status is read as the line's second field rather than by searching for
// the word: the failure detail itself reads "release lookup failed", so a
// substring hunt for "fail" would report the warn case as a hard failure.
func TestReportReleasePrintsOneLinePerAnswer(t *testing.T) {
	tests := []struct {
		name     string
		check    releaseChecker
		status   string
		contains string
	}{
		{
			"a newer patch is reported",
			&fakeRelease{tag: "v1.11.1"},
			"warn",
			"a newer release exists: v1.11.1",
		},
		{
			"a newer minor is reported",
			&fakeRelease{tag: "v1.12.0"},
			"warn",
			"a newer release exists: v1.12.0",
		},
		{
			"this release is reported ok",
			&fakeRelease{tag: "v" + version.Number},
			"ok",
			"v" + version.Number + " is the newest release",
		},
		{
			"a lookup that could not be made warns",
			&fakeRelease{err: errors.New("release lookup failed")},
			"warn",
			"could not check for a newer release: release lookup failed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			reportRelease(context.Background(), test.check, &out)

			report := out.String()
			if !strings.Contains(report, test.contains) {
				t.Fatalf("report = %q, want it to contain %q", report, test.contains)
			}
			fields := strings.Fields(report)
			if len(fields) < 3 {
				t.Fatalf("report = %q, want a probe, a status, and a detail", report)
			}
			if fields[0] != "release" || fields[1] != test.status {
				t.Fatalf("report = %q, want the release probe reported %s", report, test.status)
			}
		})
	}
}

// The release probe is the one line here that could move --check's exit code,
// and the contract in check.go says it must not: a newer release is not a broken
// setup, and a user may be gating a start on this program's exit code. A working
// environment with an available update must still exit 0.
//
// This drives checkReport with the same injected doubles check's own tests use,
// rather than with Check, which would dial Discord, Last.fm, and GitHub for
// real. That is the reason checkReport takes them: the claim under test is that
// the code the release probe returns cannot reach the caller, and it is worth
// nothing if the test proves it on a path that stops at the first network call.
func TestCheckExitsZeroWhenANewerReleaseExists(t *testing.T) {
	socket := serveCheckCliamp(t, version.Number)
	cfg := config.Config{
		ApplicationID: config.DefaultApplicationID,
		CliampSocket:  socket,
		LastFMAPIKey:  "configured-key",
	}

	var out bytes.Buffer
	code := checkReport(context.Background(), cfg, newFakeDiscord(), fakeValidator{}, &fakeRelease{tag: "v1.11.1"}, &out)

	if code != 0 {
		t.Fatalf("exit code = %d with a newer release available, want 0\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "a newer release exists") {
		t.Fatalf("report omits the newer release:\n%s", out.String())
	}
}
