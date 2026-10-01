package status_test

// This file drives the status reporter the way the daemon does: it is given a
// state and started against a context, and what it leaves on disk is what the
// Lua plugin reads. Every test therefore asserts on the file rather than on the
// reporter's own account of itself, because the file is the whole interface.

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/status"
)

// document mirrors the file the plugin parses. It is declared here rather than
// exported from the package so the test reads the file the same way the plugin
// does, through JSON, and cannot be satisfied by a field the encoder would have
// dropped.
type document struct {
	Version   int   `json:"v"`
	Beat      int64 `json:"beat"`
	Connected *bool `json:"connected"`
}

func TestReporterWritesTheStateAndABeat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rpc-status.json")
	moment := time.Unix(1_759_316_400, 0)
	reporter := status.New(path, time.Hour, func() time.Time { return moment })
	reporter.Set(status.Connected)

	stop := start(t, reporter)
	defer stop()

	got := await(t, path)
	if got.Connected == nil || !*got.Connected {
		t.Fatalf("connected = %v, want true", got.Connected)
	}
	if got.Beat != moment.Unix() {
		t.Fatalf("beat = %d, want %d", got.Beat, moment.Unix())
	}
}

// The daemon has no opinion about Discord until it has tried to reach it, and
// an opinion it does not hold must not be invented on the way to the file: a
// boolean that defaulted to false would tell the plugin Discord was down every
// time the daemon started with nothing playing.
func TestReporterOmitsTheConnectionUntilItIsKnown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rpc-status.json")
	reporter := status.New(path, time.Hour, time.Now)

	stop := start(t, reporter)
	defer stop()

	got := await(t, path)
	if got.Connected != nil {
		t.Fatalf("connected = %v, want the key to be absent", *got.Connected)
	}
	if got.Version == 0 {
		t.Fatal("document carries no schema version")
	}
}

func TestReporterFollowsTheStateItIsGiven(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rpc-status.json")
	reporter := status.New(path, 10*time.Millisecond, time.Now)
	reporter.Set(status.Connected)

	stop := start(t, reporter)
	defer stop()
	await(t, path)

	reporter.Set(status.Disconnected)
	awaitState(t, path, false)
}

// A stopped daemon is what the plugin reads staleness from, so the file has to
// carry a moving beat for as long as the daemon runs: a beat written once at
// startup would make a healthy daemon indistinguishable from a dead one.
func TestReporterBeatsWhileTheStateHoldsStill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rpc-status.json")
	clock := &fakeClock{moment: time.Unix(1_759_316_400, 0)}
	reporter := status.New(path, 10*time.Millisecond, clock.Now)
	reporter.Set(status.Connected)

	stop := start(t, reporter)
	defer stop()
	first := await(t, path)

	clock.advance(time.Second)
	awaitBeat(t, path, first.Beat)
}

// The plugin reads a stale beat as a daemon that stopped, so a reporter that
// kept writing after its context ended would hold that message off forever.
func TestReporterStopsWritingWhenTheContextIsDone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rpc-status.json")
	clock := &fakeClock{moment: time.Unix(1_759_316_400, 0)}
	reporter := status.New(path, 10*time.Millisecond, clock.Now)

	stop := start(t, reporter)
	await(t, path)
	stop()

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Minute)
	time.Sleep(50 * time.Millisecond)
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("reporter wrote after its context ended: %q then %q", before, after)
	}
}

// The plugin polls on its own clock, so it can read the file at any instant,
// including one where a write is half done. A reader must never see a truncated
// document, which means the write has to land whole or not at all. The beat is
// long here so the only write in flight is the first one.
func TestReporterLeavesNoPartialFileBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rpc-status.json")
	reporter := status.New(path, time.Hour, time.Now)

	stop := start(t, reporter)
	defer stop()
	await(t, path)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "rpc-status.json" {
			t.Fatalf("reporter left %q behind", entry.Name())
		}
	}
}

// The plugin opens the document at a moment of its own choosing, which can be
// one where a write is under way, so a write replaces the file rather than
// truncating and refilling it. A reader that has the earlier document open
// therefore keeps the document it opened, whole, instead of watching it drain
// and refill — which is the difference between a poll that reads a stale state
// it can use and one that reads an empty file it has to guess at.
func TestReporterReplacesTheDocumentRatherThanRefillingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rpc-status.json")
	clock := &fakeClock{moment: time.Unix(1_759_316_400, 0)}
	reporter := status.New(path, 10*time.Millisecond, clock.Now)
	reporter.Set(status.Connected)

	stop := start(t, reporter)
	defer stop()
	first := await(t, path)

	held, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	opened, err := io.ReadAll(held)
	if err != nil {
		t.Fatal(err)
	}

	clock.advance(time.Second)
	awaitBeat(t, path, first.Beat)

	if _, err := held.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	after, err := io.ReadAll(held)
	if err != nil {
		t.Fatal(err)
	}
	if string(opened) != string(after) {
		t.Fatalf("a write refilled the open document: %q became %q", opened, after)
	}
}

// fakeClock is a clock the test moves by hand, so a beat interval can be
// observed without waiting one out. The reporter reads it from its own
// goroutine, so every access takes the lock.
type fakeClock struct {
	mu     sync.Mutex
	moment time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.moment
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.moment = c.moment.Add(d)
}

// start runs a reporter until the returned function stops it, and waits for
// Report to have returned so a caller cannot read the file while a final write
// is still in flight.
func start(t *testing.T, reporter *status.Reporter) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		reporter.Report(ctx)
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("reporter did not stop when its context was cancelled")
		}
	}
}

// await reads the document once the reporter has written one, so a test never
// races the first write.
func await(t *testing.T, path string) document {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			var got document
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("document is not JSON: %v (%q)", err, data)
			}
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("no status document appeared at %s", path)
		}
		time.Sleep(time.Millisecond)
	}
}

// awaitState waits for the written document to carry the connection the caller
// expects, which is how a test observes a state change without knowing when the
// next beat falls.
func awaitState(t *testing.T, path string, want bool) document {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := await(t, path)
		if got.Connected != nil && *got.Connected == want {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("document never reported connected=%v", want)
		}
		time.Sleep(time.Millisecond)
	}
}

// awaitBeat waits for the file to carry a beat later than the one given, which
// is what tells the plugin the daemon is still running.
func awaitBeat(t *testing.T, path string, previous int64) document {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := await(t, path)
		if got.Beat > previous {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("beat stayed at %d", previous)
		}
		time.Sleep(time.Millisecond)
	}
}

// A connection that changes is news, and it is the news the plugin exists to
// show: the daemon learns it inside its event loop and can put it on disk there
// and then. The beat is for the opposite job, proving the daemon is still alive
// when nothing has changed, so it is no reason to hold a change back. The beat
// here is an hour, which is long enough that no write but an immediate one can
// explain the document moving at all.
func TestReporterWritesAChangeWithoutWaitingOutTheBeat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rpc-status.json")
	reporter := status.New(path, time.Hour, time.Now)

	stop := start(t, reporter)
	defer stop()
	await(t, path)

	reporter.Set(status.Connected)
	awaitState(t, path, true)
}
