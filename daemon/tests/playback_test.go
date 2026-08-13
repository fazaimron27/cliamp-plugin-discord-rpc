package tests

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/playback"
)

func TestPlaybackVisibility(t *testing.T) {
	now := time.Unix(1000, 0)
	tests := []struct {
		name  string
		state playback.State
		want  bool
	}{
		{"playing", playback.State{Status: "playing", Title: "Track", Heartbeat: 1000}, true},
		{"paused", playback.State{Status: "paused", Title: "Track", Heartbeat: 1000}, false},
		{"stopped", playback.State{Status: "stopped", Title: "Track", Heartbeat: 1000}, false},
		{"stale", playback.State{Status: "playing", Title: "Track", Heartbeat: 900}, false},
		{"no title", playback.State{Status: "playing", Heartbeat: 1000}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.state.IsPlaying(45*time.Second, now); got != test.want {
				t.Fatalf("IsPlaying() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestHeartbeatDoesNotChangePresence(t *testing.T) {
	now := time.Unix(1000, 0)
	state := playback.State{Status: "playing", Title: "Track", Artist: "Artist", Heartbeat: 990, UpdatedAt: 900}
	want := state.PresenceKey(45*time.Second, now)
	state.Heartbeat = 1000
	state.Sequence++
	if got := state.PresenceKey(45*time.Second, now); got != want {
		t.Fatalf("heartbeat changed presence key: %q != %q", got, want)
	}
}

func TestReadPlaybackState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"v":1,"session":"s","seq":2,"status":"playing","title":"Track"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := playback.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision() != "s:2" {
		t.Fatalf("Revision() = %q", state.Revision())
	}
}
