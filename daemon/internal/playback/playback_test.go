package playback_test

// This file tests the snapshot contract state.go defines: which states count as
// playing, what a track key and a presence key cover, and which payloads
// Validate accepts or rejects.

import (
	"strings"
	"testing"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/playback"
)

// IsPlaying is true only for a playing snapshot that names a title; paused,
// stopped, and titleless snapshots are all not playing.
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

// A presence key follows the timeline rather than the playhead: advancing the
// position or the observation time leaves it unchanged, while a new StartedAt
// moves it.
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

// The stream flag is part of the presence key, so a track that starts or stops
// streaming republishes rather than reusing the previous activity.
func TestPlaybackPresenceKeyTracksStreamFlag(t *testing.T) {
	state := playback.State{Status: "playing", Title: "Track", Artist: "Artist", Duration: 200, StartedAt: 900}
	want := state.PresenceKey()
	state.Stream = true
	if got := state.PresenceKey(); got == want {
		t.Fatal("changed stream flag did not change presence key")
	}
}

// The plugin_version field is optional: a well-formed value validates and a
// plugin old enough to omit the field still validates, while an oversized or
// NUL-bearing value does not.
func TestPlaybackAcceptsPluginVersion(t *testing.T) {
	valid := playback.State{Status: "playing", Title: "Track", PluginVersion: "1.6.1"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (playback.State{Status: "playing", Title: "Track"}).Validate(); err != nil {
		t.Fatal(err)
	}
	invalid := []playback.State{
		{Status: "playing", Title: "Track", PluginVersion: strings.Repeat("x", 33)},
		{Status: "playing", Title: "Track", PluginVersion: "bad\x00version"},
	}
	for _, state := range invalid {
		if err := state.Validate(); err == nil {
			t.Fatalf("Validate(%#v) succeeded", state)
		}
	}
}

// A plugin upgrade changes no rendered field, so the plugin version is not part
// of the presence key and setting it leaves the key unchanged.
func TestPlaybackPresenceKeyIgnoresPluginVersion(t *testing.T) {
	state := playback.State{Status: "playing", Title: "Track", Artist: "Artist", Duration: 200, StartedAt: 900}
	want := state.PresenceKey()
	state.PluginVersion = "1.7.0"
	if got := state.PresenceKey(); got != want {
		t.Fatalf("plugin version changed presence key: %q != %q", got, want)
	}
}

// Validate accepts a well-formed snapshot and rejects an unknown status, a
// negative duration or position, an out-of-range year, an oversized title, and
// a NUL in a text field.
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

// Validate holds every text field it reads to both of its rules — a length
// bound and no NUL — so each field gets a row of its own rather than a place in
// one of two hand-written enumerations.
//
// The rows are the point rather than the count. Artist, album and path were
// refused by nothing in this file before: the two rules were written as two
// separate chains, and a field that fell out of the second would leave a hole
// that every test still passed through.
//
// Status is absent deliberately. Both of its rules sit behind the allowlist
// above them, which admits three literals of five to seven characters, so a
// status that is oversized or carries a NUL is already refused one line
// earlier. Its row would assert rejection and prove nothing about the text
// rules.
//
// The title and plugin-version rows restate cases the tests above already
// carry. They are here because a table that carves exceptions out of itself is
// the thing it replaced.
func TestPlaybackValidationChecksEveryTextField(t *testing.T) {
	cases := []struct {
		name string
		bad  func(*playback.State)
	}{
		{"an oversized title", func(s *playback.State) { s.Title = strings.Repeat("x", 4097) }},
		{"a NUL in the title", func(s *playback.State) { s.Title = "bad\x00title" }},
		{"an oversized artist", func(s *playback.State) { s.Artist = strings.Repeat("x", 4097) }},
		{"a NUL in the artist", func(s *playback.State) { s.Artist = "bad\x00artist" }},
		{"an oversized album", func(s *playback.State) { s.Album = strings.Repeat("x", 4097) }},
		{"a NUL in the album", func(s *playback.State) { s.Album = "bad\x00album" }},
		{"an oversized path", func(s *playback.State) { s.Path = strings.Repeat("x", 16385) }},
		{"a NUL in the path", func(s *playback.State) { s.Path = "bad\x00path" }},
		{"an oversized plugin version", func(s *playback.State) { s.PluginVersion = strings.Repeat("x", 33) }},
		{"a NUL in the plugin version", func(s *playback.State) { s.PluginVersion = "bad\x00version" }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			state := playback.State{Status: "playing", Title: "Track"}
			testCase.bad(&state)
			if err := state.Validate(); err == nil {
				t.Fatal("Validate() accepted the field; want it refused")
			}
		})
	}
}

// The track key is built from the private path so two tracks with the same
// metadata stay distinct, while the presence key never contains the path and so
// cannot leak it into a Discord payload.
func TestTrackKeyUsesPrivatePathOnlyForIdentity(t *testing.T) {
	state := playback.State{Path: "spotify:track:secret", Title: "Track", Artist: "Artist", Duration: 10}
	if !strings.Contains(state.TrackKey(), state.Path) {
		t.Fatal("track key does not distinguish paths")
	}
	if strings.Contains(state.PresenceKey(), state.Path) {
		t.Fatal("presence key exposes path")
	}
}
