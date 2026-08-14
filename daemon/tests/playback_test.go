package tests

import (
	"strings"
	"testing"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/playback"
)

func TestPlaybackVisibility(t *testing.T) {
	tests := []struct {
		name  string
		state playback.State
		want  bool
	}{
		{"playing", playback.State{Status: "playing", Title: "Track"}, true},
		{"paused", playback.State{Status: "paused", Title: "Track"}, false},
		{"stopped", playback.State{Status: "stopped", Title: "Track"}, false},
		{"no title", playback.State{Status: "playing"}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.state.IsPlaying(); got != test.want {
				t.Fatalf("IsPlaying() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestPlaybackPresenceKey(t *testing.T) {
	state := playback.State{Status: "playing", Title: "Track", Artist: "Artist", Duration: 200, StartedAt: 900}
	want := state.PresenceKey()
	state.Position = 20
	state.ObservedAt = 1000
	if got := state.PresenceKey(); got != want {
		t.Fatalf("natural position changed presence key: %q != %q", got, want)
	}
	state.StartedAt = 950
	if got := state.PresenceKey(); got == want {
		t.Fatal("changed timeline did not change presence key")
	}
}

func TestPlaybackValidation(t *testing.T) {
	valid := playback.State{Status: "playing", Title: "Track", Duration: 10, Position: 2, Year: 2020}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	invalid := []playback.State{
		{Status: "buffering"},
		{Status: "playing", Duration: -1},
		{Status: "playing", Position: -1},
		{Status: "playing", Year: 10000},
		{Status: "playing", Title: strings.Repeat("x", 4097)},
		{Status: "playing", Title: "bad\x00title"},
	}
	for _, state := range invalid {
		if err := state.Validate(); err == nil {
			t.Fatalf("Validate(%#v) succeeded", state)
		}
	}
}

func TestTrackKeyUsesPrivatePathOnlyForIdentity(t *testing.T) {
	state := playback.State{Path: "spotify:track:secret", Title: "Track", Artist: "Artist", Duration: 10}
	if !strings.Contains(state.TrackKey(), state.Path) {
		t.Fatal("track key does not distinguish paths")
	}
	if strings.Contains(state.PresenceKey(), state.Path) {
		t.Fatal("presence key exposes path")
	}
}
