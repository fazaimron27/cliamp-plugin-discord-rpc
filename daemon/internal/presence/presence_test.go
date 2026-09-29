package presence_test

// This file tests the activity Build assembles from a playback snapshot: the
// fields it derives, the fallbacks it substitutes, the buttons it offers, and
// the truncation it applies before Discord receives the text.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/playback"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/presence"
)

// A playing snapshot with album art produces a listening activity: the artist
// as state, the artwork as the large image, the timeline anchored on StartedAt,
// and the track and app buttons.
func TestPresenceBuildsPlayingActivity(t *testing.T) {
	state := playback.State{Status: "playing", Title: "Track", Artist: "Artist", Album: "Album", Duration: 240, Position: 30, StartedAt: 970}
	activity := presence.Build(state, presence.Options{LargeImage: "cliamp"}, "https://img/cover.jpg", time.Unix(2000, 0))
	if activity.State != "Artist" || activity.StatusDisplayType != 1 {
		t.Fatalf("artist display = %+v", activity)
	}
	if activity.Assets == nil || activity.Assets.LargeImage != "https://img/cover.jpg" {
		t.Fatalf("assets = %+v", activity.Assets)
	}
	if activity.Timestamps == nil || activity.Timestamps.Start != 970 || activity.Timestamps.End != 1210 {
		t.Fatalf("timestamps = %+v", activity.Timestamps)
	}
	if len(activity.Buttons) != 2 || activity.Buttons[0].Label != "View on Last.fm" || activity.Buttons[0].URL != "https://www.last.fm/search?q=Artist+Track" || activity.Buttons[1].Label != "Get Cliamp" || activity.Buttons[1].URL != "https://www.cliamp.stream/" {
		t.Fatalf("buttons = %+v", activity.Buttons)
	}
}

// An artist or title that needs escaping is URL-encoded into the Last.fm search
// link rather than pasted raw.
func TestPresenceTrackButtonEncodesSearchQuery(t *testing.T) {
	state := playback.State{Status: "playing", Title: "Back in Black", Artist: "AC/DC", Duration: 255}
	activity := presence.Build(state, presence.Options{}, "", time.Unix(1000, 0))
	if len(activity.Buttons) == 0 {
		t.Fatalf("buttons = %+v", activity.Buttons)
	}
	if activity.Buttons[0].URL != "https://www.last.fm/search?q=AC%2FDC+Back+in+Black" {
		t.Fatalf("track url = %q", activity.Buttons[0].URL)
	}
}

// A stream, an absent or blank artist, and an absent title each leave no stable
// track to link, so the card carries only the app button.
func TestPresenceOmitsTrackButtonWhenNotLinkable(t *testing.T) {
	cases := []struct {
		name  string
		state playback.State
	}{
		{"stream", playback.State{Status: "playing", Title: "Track", Artist: "Artist", Stream: true}},
		{"empty artist", playback.State{Status: "playing", Title: "Track"}},
		{"blank artist", playback.State{Status: "playing", Title: "Track", Artist: "   "}},
		{"empty title", playback.State{Status: "playing", Artist: "Artist"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			activity := presence.Build(testCase.state, presence.Options{}, "", time.Unix(1000, 0))
			if len(activity.Buttons) != 1 || activity.Buttons[0].Label != "Get Cliamp" || activity.Buttons[0].URL != "https://www.cliamp.stream/" {
				t.Fatalf("buttons = %+v", activity.Buttons)
			}
		})
	}
}

// With no artwork and no artist, the activity falls back to the configured
// asset and to "Unknown artist", while an album still supplies the large image
// text.
func TestPresenceUsesFallbacks(t *testing.T) {
	state := playback.State{Status: "playing", Title: "Track", Album: "Album", Duration: 10, StartedAt: 1000}
	activity := presence.Build(state, presence.Options{LargeImage: "cliamp", LargeText: "Cliamp"}, "", time.Unix(1000, 0))
	if activity.State != "Unknown artist" || activity.Assets.LargeImage != "cliamp" || activity.Assets.LargeText != "Album" {
		t.Fatalf("activity = %+v", activity)
	}
}

// Details are capped at 48 runes and state at 40 for the expanded card, each
// ending in an ellipsis.
func TestPresenceTruncatesVisibleTextForExpandedCard(t *testing.T) {
	state := playback.State{
		Status: "playing",
		Title:  strings.Repeat("Long title ", 8),
		Artist: strings.Repeat("Artist ", 10),
	}
	activity := presence.Build(state, presence.Options{}, "", time.Now())

	if got := []rune(activity.Details); len(got) != 48 || string(got[45:]) != "..." {
		t.Fatalf("details = %q (%d runes)", activity.Details, len(got))
	}
	if got := []rune(activity.State); len(got) != 40 || string(got[37:]) != "..." {
		t.Fatalf("state = %q (%d runes)", activity.State, len(got))
	}
}

// Truncating multi-byte text must not cut a rune in half, so the result still
// decodes and still reaches the rune caps.
func TestPresenceTruncationPreservesUTF8(t *testing.T) {
	state := playback.State{Status: "playing", Title: strings.Repeat("界", 50), Artist: strings.Repeat("音", 42)}
	activity := presence.Build(state, presence.Options{}, "", time.Now())

	if !strings.HasSuffix(activity.Details, "...") || !strings.HasSuffix(activity.State, "...") {
		t.Fatalf("activity = %+v", activity)
	}
	if len([]rune(activity.Details)) != 48 || len([]rune(activity.State)) != 40 {
		t.Fatalf("details/state lengths = %d/%d", len([]rune(activity.Details)), len([]rune(activity.State)))
	}
}

// The marshalled payload carries the public track metadata and both button
// labels, and never the local path or the plugin's own links.
func TestPresencePayloadContainsOnlyPublicTrackMetadata(t *testing.T) {
	state := playback.State{Status: "playing", Title: "Track", Artist: "Artist"}
	data, err := json.Marshal(presence.Build(state, presence.Options{}, "", time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "path") || strings.Contains(string(data), "View Plugin") || strings.Contains(string(data), "github.com") || !strings.Contains(string(data), "Get Cliamp") || !strings.Contains(string(data), "View on Last.fm") {
		t.Fatalf("unexpected activity payload: %s", data)
	}
}
