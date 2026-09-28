package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/config"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/presence"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/version"
)

// recordingDiscord also records cleared activities. The plain fake cannot tell
// a daemon that cleared its presence from one that simply published nothing
// more, and clearing is the whole point of the file transport noticing that
// Cliamp is gone.
type recordingDiscord struct {
	fakeDiscord
	clears int
}

func (f *recordingDiscord) ClearActivity() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clears++
	return nil
}

func (f *recordingDiscord) cleared() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clears
}

// waitFor polls until a matching activity has been published, failing rather
// than hanging.
func (f *fakeDiscord) waitFor(t *testing.T, within time.Duration, match func(presence.Activity) bool) presence.Activity {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for _, activity := range f.activities {
			if match(activity) {
				f.mu.Unlock()
				return activity
			}
		}
		f.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no activity matching the test's condition within %v", within)
	return presence.Activity{}
}

// writeStateDocument writes a state document in the shape the plugin's file
// mode writes, filling in the fields the test does not vary.
func writeStateDocument(t *testing.T, path string, fields map[string]any) {
	t.Helper()
	now := time.Now().Unix()
	document := map[string]any{
		"v":              1,
		"plugin_version": version.Number,
		"status":         "playing",
		"title":          "Track",
		"artist":         "Artist",
		"album":          "Album",
		"path":           "/music/track.flac",
		"year":           1999,
		"duration":       200,
		"position":       10,
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
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// fileConfig is a daemon configured to read the file transport, with a window
// wide enough that nothing in a test outlives it by accident.
func fileConfig(path string) config.Config {
	return config.Config{
		ApplicationID: config.DefaultApplicationID,
		Transport:     config.TransportFile,
		StatePath:     path,
		StateMaxAge:   time.Minute,
		LargeImage:    "cliamp",
		LargeText:     "Cliamp",
	}
}

func TestRunPublishesWhatTheStateFileSays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rpc-state.json")
	writeStateDocument(t, path, map[string]any{"title": "From the file", "artist": "The artist"})

	client := &fakeDiscord{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = run(ctx, fileConfig(path), client, noArtwork{}, time.Now) }()

	// The document is the whole transport: no socket, no subscription, and the
	// presence has to come from what the file says.
	activity := client.waitFor(t, 5*time.Second, func(activity presence.Activity) bool {
		return activity.Details == "From the file"
	})
	if activity.State != "The artist" {
		t.Fatalf("activity = %+v", activity)
	}
}

func TestRunClearsPresenceWhenTheDocumentIsRemoved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rpc-state.json")
	writeStateDocument(t, path, map[string]any{"title": "From the file"})

	client := &recordingDiscord{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = run(ctx, fileConfig(path), client, noArtwork{}, time.Now) }()

	client.waitFor(t, 5*time.Second, func(activity presence.Activity) bool {
		return activity.Details == "From the file"
	})

	// A clean quit removes the document. The presence must follow it off
	// Discord, which is what the IPC transport gets from its connection closing
	// and what this transport has to derive from the file going.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && client.cleared() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if client.cleared() == 0 {
		t.Fatal("the daemon left the removed document's track on Discord")
	}
}

// A heartbeat-only rewrite carries the position the plugin last reported, so
// the daemon has to keep running the playhead from the change time instead of
// re-anchoring it on each beat. Re-anchoring would show up as a new activity,
// because the started-at time is part of what Discord is told.
//
// The beats are 15 seconds apart, as the plugin's are, so the rewrite moves the
// heartbeat further than the tracker's continuity tolerance allows. A rewrite
// inside the same second would prove nothing: the format's heartbeat has
// whole-second resolution, so two writes that close together move nothing.
func TestRunIgnoresAHeartbeatOnlyRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rpc-state.json")
	updatedAt := time.Now().Add(-30 * time.Second).Unix()
	document := func(heartbeat int64) map[string]any {
		return map[string]any{
			"title": "Long track", "position": 30, "duration": 600,
			"updated_at": updatedAt, "heartbeat": heartbeat,
		}
	}
	writeStateDocument(t, path, document(time.Now().Add(-15*time.Second).Unix()))

	client := &fakeDiscord{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = run(ctx, fileConfig(path), client, noArtwork{}, time.Now) }()

	client.waitFor(t, 5*time.Second, func(activity presence.Activity) bool {
		return activity.Details == "Long track"
	})

	// The same document again, fifteen seconds of heartbeats later and with
	// nothing else about it changed.
	writeStateDocument(t, path, document(time.Now().Unix()))
	time.Sleep(300 * time.Millisecond)

	if published := client.published(); published != 1 {
		t.Fatalf("the daemon published %d activities for one track, want 1", published)
	}
}
