package daemon

// This file exercises the run loop's file transport: presence built from the
// state document, presence cleared when a clean quit removes it, and a
// heartbeat-only rewrite that must not be mistaken for a new activity.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/config"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/diag"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/presence"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/version"
)

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

// Presence comes entirely from the state document here: no socket, no
// subscription, so the activity has to carry what the file says.
func TestRunPublishesWhatTheStateFileSays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rpc-state.json")
	writeStateDocument(t, path, map[string]any{"title": "From the file", "artist": "The artist"})

	client := newFakeDiscord()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = run(ctx, fileConfig(path), client, noArtwork{}, diag.Discard(), diag.Discard(), time.Now, presenceRefresh)
	}()

	activity := client.waitForActivity(t, 5*time.Second, func(activity presence.Activity) bool {
		return activity.Details == "From the file"
	})
	if activity.State != "The artist" {
		t.Fatalf("activity = %+v", activity)
	}
}

// A clean quit removes the document, and the presence must follow it off
// Discord. The IPC transport gets that from its connection closing; this one
// has to derive it from the file going.
func TestRunClearsPresenceWhenTheDocumentIsRemoved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rpc-state.json")
	writeStateDocument(t, path, map[string]any{"title": "From the file"})

	client := newFakeDiscord()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = run(ctx, fileConfig(path), client, noArtwork{}, diag.Discard(), diag.Discard(), time.Now, presenceRefresh)
	}()

	client.waitForActivity(t, 5*time.Second, func(activity presence.Activity) bool {
		return activity.Details == "From the file"
	})

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the daemon to clear the removed document's track", func() bool {
		return client.clearCount() > 0
	})
}

// A heartbeat-only rewrite carries the position the plugin last reported, so
// the daemon has to keep running the playhead from the change time instead of
// re-anchoring it on each beat. Re-anchoring would show up as a new activity,
// because the started-at time is part of what Discord is told.
//
// The beats are 15 seconds apart, as the plugin's are, so a rewrite moves the
// heartbeat further than the tracker's continuity tolerance allows. A rewrite
// inside the same second would prove nothing: the format's heartbeat has
// whole-second resolution, so two writes that close together move nothing. The
// test therefore rewrites the same document, with nothing else about it changed.
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

	client := newFakeDiscord()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = run(ctx, fileConfig(path), client, noArtwork{}, diag.Discard(), diag.Discard(), time.Now, presenceRefresh)
	}()

	client.waitForActivity(t, 5*time.Second, func(activity presence.Activity) bool {
		return activity.Details == "Long track"
	})

	writeStateDocument(t, path, document(time.Now().Unix()))
	time.Sleep(300 * time.Millisecond)

	if published := client.activityCount(); published != 1 {
		t.Fatalf("the daemon published %d activities for one track, want 1", published)
	}
}
