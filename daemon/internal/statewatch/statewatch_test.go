package statewatch

// This file tests the file transport: how the state document is decoded into
// the daemon's playback state, how the heartbeat dates it, and how a
// subscription reports what the document says. The document's writer, the Lua
// plugin, is exercised separately in transport_plugin_test.go.
//
// The heartbeat is what makes a document evidence that Cliamp is running. A
// document is live only while its beat is inside the window, and a beat exactly
// one window old has already lapsed. The plugin rewrites the beat without
// moving the playhead, so a document is dated by updated_at and never by the
// beat.
//
// A subscription reports changes to what the daemon displays and nothing else,
// which is why it stays quiet with no document yet and with one long lapsed.

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

// A document that varies every field is decoded field by field. ObservedAt must
// come from the plugin's updated_at, the change time the IPC envelope carries
// for the other transport, and not from the heartbeat: a heartbeat-only rewrite
// would otherwise look like playhead movement and re-anchor the progress bar on
// every beat. The path becomes the daemon's track key, which is what keeps a
// resumed track's timeline rather than restarting its progress bar.
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
	if state.Path != "/music/track.flac" {
		t.Errorf("path = %q", state.Path)
	}
	if want := time.Unix(1005, 0); !current.heartbeat.Equal(want) {
		t.Errorf("heartbeat = %v, want %v", current.heartbeat, want)
	}
}

// A document the daemon cannot trust is refused with an error naming the field
// it objected to: a missing heartbeat, change time, or status; a status the
// daemon has no such state for; and a schema version it does not read, whether
// absent or from the future. Bytes that are not a document at all are refused
// the same way.
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

// Inspect reports what state the document is in and why it cannot be used: no
// document at all, a live one, one left behind by a crash, and one in a schema
// the daemon does not read. A lapsed document reports its age, which is what
// tells the user how long ago Cliamp stopped rather than leaving it to be
// inferred from the flag. An unknown schema reports the problem, which is the
// case the run loop cannot explain on its own: it stays quiet, and the reason
// is visible only by reading the document.
func TestInspectSaysWhyADocumentCannotBeUsed(t *testing.T) {
	t.Run("nothing there", func(t *testing.T) {
		detail := Inspect(statePath(t), time.Minute)
		if detail.Present || detail.Problem != nil {
			t.Fatalf("detail = %+v", detail)
		}
	})

	t.Run("a live document", func(t *testing.T) {
		path := statePath(t)
		writeDocument(t, path, map[string]any{"title": "Playing"})

		detail := Inspect(path, time.Minute)
		if !detail.Present || detail.Problem != nil || detail.Lapsed {
			t.Fatalf("detail = %+v", detail)
		}
		if detail.State.Title != "Playing" {
			t.Fatalf("state = %+v", detail.State)
		}
		if detail.Age > 5*time.Second || detail.Age < 0 {
			t.Fatalf("age = %v", detail.Age)
		}
	})

	t.Run("left behind by a crash", func(t *testing.T) {
		path := statePath(t)
		old := time.Now().Add(-time.Hour).Unix()
		writeDocument(t, path, map[string]any{"heartbeat": old, "updated_at": old})

		detail := Inspect(path, time.Minute)
		if !detail.Present || !detail.Lapsed {
			t.Fatalf("detail = %+v", detail)
		}
		if detail.Age < 55*time.Minute {
			t.Fatalf("age = %v, want about an hour", detail.Age)
		}
	})

	t.Run("a document from a schema we do not know", func(t *testing.T) {
		path := statePath(t)
		writeDocument(t, path, map[string]any{"v": 2})

		detail := Inspect(path, time.Minute)
		if !detail.Present || detail.Problem == nil {
			t.Fatalf("detail = %+v", detail)
		}
		if !strings.Contains(detail.Problem.Error(), "schema") {
			t.Fatalf("problem = %v", detail.Problem)
		}
	})
}

// A document already on disk is delivered as soon as the subscription starts. A
// daemon started mid-track must show the track, which is what the IPC transport
// gets from retention and what reading the file at startup gives.
func TestSubscribeDeliversTheDocumentAlreadyOnDisk(t *testing.T) {
	path := statePath(t)
	writeDocument(t, path, map[string]any{"title": "Already Playing", "position": 12})

	states, err := Subscribe(context.Background(), path, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	state := receive(t, states, 3*time.Second)
	if state.Title != "Already Playing" || state.Position != 12 {
		t.Fatalf("state = %+v", state)
	}
}

// Rewriting the document with a new title delivers the change on the same
// subscription, so a subscription tracks the document rather than reading it
// once.
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

// A document left behind by a crash is older than the window, so it is not
// evidence that Cliamp is running and is not reported as playing. It is not
// reported as stopped either: nothing was showing, and this channel reports
// changes to what the daemon shows, of which there is none.
func TestSubscribeIgnoresADocumentThatAlreadyAgedOut(t *testing.T) {
	path := statePath(t)
	old := time.Now().Add(-time.Hour).Unix()
	writeDocument(t, path, map[string]any{"heartbeat": old, "updated_at": old})

	states, err := Subscribe(context.Background(), path, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	expectQuiet(t, states, 300*time.Millisecond)
}

// lapseIn and liveAt are exact at the window's boundary, which the integration
// tests cannot pin down: a heartbeat exactly one window old is not evidence that
// Cliamp is running, so a document at that age is not delivered as playing,
// while one a nanosecond inside the window still is.
func TestReadingLapsesWithItsHeartbeat(t *testing.T) {
	beat := time.Unix(1000, 0)
	current := reading{heartbeat: beat}
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

	if current.liveAt(beat.Add(maxAge), maxAge) {
		t.Error("a document exactly one window old was still counted as live")
	}
	if !current.liveAt(beat.Add(maxAge-time.Nanosecond), maxAge) {
		t.Error("a document just inside the window was counted as lapsed")
	}
}

// A document whose heartbeat stops advancing lapses and is reported as stopped,
// after first being reported as playing. The window has to clear the format's
// one-second heartbeat granularity: the document carries whole seconds, so a
// window of a few hundred milliseconds can already have lapsed by the time the
// test subscribes, which would make this a race rather than a test.
func TestSubscribeReportsStoppedWhenTheHeartbeatStops(t *testing.T) {
	path := statePath(t)
	writeDocument(t, path, map[string]any{"title": "Playing"})

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

// Removing the document reports stopped and keeps the subscription watching.
// The plugin removes the document on a clean quit, so removal is an event the
// daemon must act on rather than a reason to stop watching, and a Cliamp that
// recreates the document must be picked up by the same subscription.
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

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if state := receive(t, states, 3*time.Second); state.IsPlaying() {
		t.Fatalf("a removed document was still reported as playing: %+v", state)
	}

	writeDocument(t, path, map[string]any{"title": "Restarted"})
	if state := receive(t, states, 3*time.Second); state.Title != "Restarted" {
		t.Fatalf("state after removal = %+v", state)
	}
}

// With no directory to watch, Subscribe fails rather than blocking, so the run
// loop retries with backoff until Cliamp creates the directory instead of
// reporting a dead transport.
func TestSubscribeFailsWhileTheDirectoryIsMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent", "rpc-state.json")

	if _, err := Subscribe(context.Background(), path, time.Minute); err == nil {
		t.Fatal("Subscribe succeeded with no directory to watch")
	}
}

// Cancelling the context closes the state channel, so an interrupted daemon's
// subscription ends instead of staying open and watching a document nobody
// displays.
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

// With no document published yet the subscription stays quiet: there is no
// state to report, and inventing one would clear an activity the daemon had
// already published.
func TestSubscribeStaysQuietWhenThereIsNoDocumentYet(t *testing.T) {
	states, err := Subscribe(context.Background(), statePath(t), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	expectQuiet(t, states, 300*time.Millisecond)
}
