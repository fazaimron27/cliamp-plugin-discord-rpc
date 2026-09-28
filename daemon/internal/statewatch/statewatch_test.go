package statewatch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/playback"
)

// documentFor builds a state document, filling in everything the caller does
// not vary. It is written as the plugin would write it: a single JSON object.
func documentFor(t *testing.T, fields map[string]any) []byte {
	t.Helper()
	now := time.Now().Unix()
	document := map[string]any{
		"v":              1,
		"plugin_version": "1.8.0",
		"status":         "playing",
		"title":          "Track",
		"artist":         "Artist",
		"album":          "Album",
		"path":           "/music/track.flac",
		"year":           1999,
		"duration":       240,
		"position":       30,
		"stream":         false,
		"updated_at":     now,
		"heartbeat":      now,
	}
	for key, value := range fields {
		document[key] = value
	}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeDocument(t *testing.T, path string, fields map[string]any) {
	t.Helper()
	if err := os.WriteFile(path, documentFor(t, fields), 0o644); err != nil {
		t.Fatal(err)
	}
}

// statePath prepares a directory holding nothing, and returns the path of the
// document that would live in it.
func statePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "rpc-state.json")
}

// receive waits for one state, failing the test rather than hanging.
func receive(t *testing.T, states <-chan playback.State, within time.Duration) playback.State {
	t.Helper()
	select {
	case state, ok := <-states:
		if !ok {
			t.Fatal("the state channel closed")
		}
		return state
	case <-time.After(within):
		t.Fatalf("no state arrived within %v", within)
		return playback.State{}
	}
}

// expectQuiet asserts that nothing arrives, so a test can tell "no delivery"
// apart from "a delivery the test did not look at".
func expectQuiet(t *testing.T, states <-chan playback.State, within time.Duration) {
	t.Helper()
	select {
	case state := <-states:
		t.Fatalf("expected no state, got %+v", state)
	case <-time.After(within):
	}
}

func TestDecodeMapsTheDocumentOntoTheDaemonState(t *testing.T) {
	current, err := decode(documentFor(t, map[string]any{
		"status": "paused", "title": "Other", "artist": "Someone", "position": 61,
		"year": 2001, "duration": 300, "stream": true,
		"plugin_version": "1.7.0", "updated_at": int64(1000), "heartbeat": int64(1005),
	}))
	if err != nil {
		t.Fatal(err)
	}
	state := current.state

	// ObservedAt is the plugin's own change time, which is what the IPC envelope
	// carries for the other transport. The heartbeat must not become it: a
	// heartbeat-only rewrite would then look like playhead movement and the
	// progress bar would re-anchor on every beat.
	if state.ObservedAt != 1000 {
		t.Errorf("observed at = %d, want the document's updated_at 1000", state.ObservedAt)
	}
	if state.PluginVersion != "1.7.0" {
		t.Errorf("plugin version = %q", state.PluginVersion)
	}
	if state.Status != "paused" || state.Title != "Other" || state.Artist != "Someone" {
		t.Errorf("state = %+v", state)
	}
	if state.Position != 61 || state.Duration != 300 || state.Year != 2001 || !state.Stream {
		t.Errorf("state = %+v", state)
	}
	// The path reaches the daemon's track key, which is what keeps a resumed
	// track's timeline rather than restarting its progress bar.
	if state.Path != "/music/track.flac" {
		t.Errorf("path = %q", state.Path)
	}
	if want := time.Unix(1005, 0); !current.heartbeat.Equal(want) {
		t.Errorf("heartbeat = %v, want %v", current.heartbeat, want)
	}
}

func TestDecodeRejectsDocumentsItCannotTrust(t *testing.T) {
	tests := []struct {
		name   string
		fields map[string]any
		wants  string
	}{
		{"no heartbeat", map[string]any{"heartbeat": 0}, "heartbeat"},
		{"no change time", map[string]any{"updated_at": 0}, "updated_at"},
		{"no status", map[string]any{"status": ""}, "status"},
		{"status from nowhere", map[string]any{"status": "buffering"}, "status"},
		{"no schema", map[string]any{"v": 0}, "schema"},
		{"a schema from the future", map[string]any{"v": 7}, "schema"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decode(documentFor(t, test.fields))
			if err == nil {
				t.Fatal("the document was accepted")
			}
			if !strings.Contains(err.Error(), test.wants) {
				t.Errorf("error %q does not name %q", err, test.wants)
			}
		})
	}

	t.Run("not a document", func(t *testing.T) {
		if _, err := decode([]byte("{ this is not json")); err == nil {
			t.Fatal("unparseable content was accepted")
		}
	})
}

func TestSubscribeDeliversTheDocumentAlreadyOnDisk(t *testing.T) {
	path := statePath(t)
	writeDocument(t, path, map[string]any{"title": "Already Playing", "position": 12})

	states, err := Subscribe(context.Background(), path, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// A daemon started mid-track must show the track, which is what the IPC
	// transport gets from retention and what reading the file on startup gives.
	state := receive(t, states, 3*time.Second)
	if state.Title != "Already Playing" || state.Position != 12 {
		t.Fatalf("state = %+v", state)
	}
}

func TestSubscribeDeliversChanges(t *testing.T) {
	path := statePath(t)
	writeDocument(t, path, map[string]any{"title": "First"})

	states, err := Subscribe(context.Background(), path, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if state := receive(t, states, 3*time.Second); state.Title != "First" {
		t.Fatalf("first state = %+v", state)
	}

	writeDocument(t, path, map[string]any{"title": "Second"})
	if state := receive(t, states, 3*time.Second); state.Title != "Second" {
		t.Fatalf("changed state = %+v", state)
	}
}

func TestSubscribeIgnoresADocumentThatAlreadyAgedOut(t *testing.T) {
	path := statePath(t)
	old := time.Now().Add(-time.Hour).Unix()
	writeDocument(t, path, map[string]any{"heartbeat": old, "updated_at": old})

	states, err := Subscribe(context.Background(), path, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// A document left behind by a crash must not be reported as playing: it is
	// older than the window, so it is not evidence that Cliamp is running. It is
	// not reported as stopped either — nothing was showing, and this channel
	// reports changes to what the daemon shows, of which there is none.
	expectQuiet(t, states, 300*time.Millisecond)
}

func TestSnapshotLapsesWithItsHeartbeat(t *testing.T) {
	beat := time.Unix(1000, 0)
	current := snapshot{heartbeat: beat}
	maxAge := 45 * time.Second

	tests := []struct {
		name string
		now  time.Time
		want time.Duration
	}{
		{"just written", beat, maxAge},
		{"part way through the window", beat.Add(30 * time.Second), 15 * time.Second},
		{"exactly at the window's end", beat.Add(maxAge), 0},
		{"past it", beat.Add(60 * time.Second), -15 * time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := current.lapseIn(test.now, maxAge); got != test.want {
				t.Errorf("lapse = %v, want %v", got, test.want)
			}
		})
	}

	// The boundary is the case the integration tests cannot pin down: a
	// heartbeat exactly one window old is not evidence that Cliamp is running,
	// so a document at that age must not be delivered as playing.
	if current.liveAt(beat.Add(maxAge), maxAge) {
		t.Error("a document exactly one window old was still counted as live")
	}
	if !current.liveAt(beat.Add(maxAge-time.Nanosecond), maxAge) {
		t.Error("a document just inside the window was counted as lapsed")
	}
}

func TestSubscribeReportsStoppedWhenTheHeartbeatStops(t *testing.T) {
	path := statePath(t)
	writeDocument(t, path, map[string]any{"title": "Playing"})

	// The window has to clear the format's one-second heartbeat granularity: the
	// document carries whole seconds, so a window of a few hundred milliseconds
	// can already have lapsed by the time the test subscribes, which would make
	// this a race rather than a test.
	maxAge := 3 * time.Second
	states, err := Subscribe(context.Background(), path, maxAge)
	if err != nil {
		t.Fatal(err)
	}
	if state := receive(t, states, 10*time.Second); !state.IsPlaying() {
		t.Fatalf("the fresh document was not reported as playing: %+v", state)
	}
	state := receive(t, states, 10*time.Second)
	if state.IsPlaying() {
		t.Fatalf("a document whose heartbeat aged out was reported as playing: %+v", state)
	}
}

func TestSubscribeTreatsARemovedDocumentAsStoppedAndKeepsWatching(t *testing.T) {
	path := statePath(t)
	writeDocument(t, path, map[string]any{"title": "Playing"})

	states, err := Subscribe(context.Background(), path, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if state := receive(t, states, 3*time.Second); !state.IsPlaying() {
		t.Fatalf("the document was not delivered: %+v", state)
	}

	// The plugin removes the document on a clean quit, so removal is an event
	// the daemon must act on rather than a reason to stop watching.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if state := receive(t, states, 3*time.Second); state.IsPlaying() {
		t.Fatalf("a removed document was still reported as playing: %+v", state)
	}

	// Cliamp restarting recreates it, and the same subscription must carry on.
	writeDocument(t, path, map[string]any{"title": "Restarted"})
	if state := receive(t, states, 3*time.Second); state.Title != "Restarted" {
		t.Fatalf("state after removal = %+v", state)
	}
}

func TestSubscribeFailsWhileTheDirectoryIsMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent", "rpc-state.json")

	// Nothing to watch yet. Failing lets the run loop retry with backoff until
	// Cliamp creates the directory, rather than reporting a dead transport.
	if _, err := Subscribe(context.Background(), path, time.Minute); err == nil {
		t.Fatal("Subscribe succeeded with no directory to watch")
	}
}

func TestSubscribeStopsOnCancellation(t *testing.T) {
	path := statePath(t)
	writeDocument(t, path, map[string]any{})

	ctx, cancel := context.WithCancel(context.Background())
	states, err := Subscribe(ctx, path, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	receive(t, states, 3*time.Second)

	cancel()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-states:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("the state channel stayed open after cancellation")
		}
	}
}

func TestSubscribeStaysQuietWhenThereIsNoDocumentYet(t *testing.T) {
	states, err := Subscribe(context.Background(), statePath(t), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// Cliamp has not published anything: there is no state to report, and
	// inventing one would clear an activity the daemon had already published.
	expectQuiet(t, states, 300*time.Millisecond)
}
