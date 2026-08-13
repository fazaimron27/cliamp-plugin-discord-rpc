package tests

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	now := time.Now().Unix()
	data := fmt.Sprintf(`{"v":1,"session":"s","seq":2,"status":"playing","title":"Track","heartbeat":%d,"updated_at":%d}`, now, now)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
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

func TestReadPlaybackStateRejectsOversizedDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 65<<10)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := playback.Read(path); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("error = %v", err)
	}
}

func TestReadPlaybackStateRejectsFutureTimestamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	future := time.Now().Add(time.Hour).Unix()
	data := fmt.Sprintf(`{"v":1,"session":"s","seq":1,"status":"playing","title":"Track","heartbeat":%d,"updated_at":1}`, future)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := playback.Read(path); err == nil || !strings.Contains(err.Error(), "future") {
		t.Fatalf("error = %v", err)
	}
}

func TestReadPlaybackStateRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	link := filepath.Join(dir, "state.json")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := playback.Read(link); err == nil {
		t.Fatal("expected symlink rejection")
	}
}
