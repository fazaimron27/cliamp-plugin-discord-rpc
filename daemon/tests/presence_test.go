package tests

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/playback"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/presence"
)

func TestPresenceBuildsPlayingActivity(t *testing.T) {
	state := playback.State{Status: "playing", Title: "Track", Artist: "Artist", Album: "Album", Duration: 240, Position: 30, UpdatedAt: 1000}
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
	if len(activity.Buttons) != 2 || activity.Buttons[0].Label != "Get Cliamp" || activity.Buttons[0].URL != "https://www.cliamp.stream/" || activity.Buttons[1].Label != "View Plugin" || activity.Buttons[1].URL != "https://github.com/fazaimron27/cliamp-plugin-discord-rpc" {
		t.Fatalf("buttons = %+v", activity.Buttons)
	}
}

func TestPresenceUsesFallbacks(t *testing.T) {
	state := playback.State{Status: "playing", Title: "Track", Album: "Album", Duration: 10, UpdatedAt: 1000}
	activity := presence.Build(state, presence.Options{LargeImage: "cliamp", LargeText: "Cliamp"}, "", time.Unix(1000, 0))
	if activity.State != "Unknown artist" || activity.Assets.LargeImage != "cliamp" || activity.Assets.LargeText != "Album" {
		t.Fatalf("activity = %+v", activity)
	}
}

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

func TestPresencePayloadContainsOnlyPublicTrackMetadata(t *testing.T) {
	state := playback.State{Status: "playing", Title: "Track", Artist: "Artist"}
	data, err := json.Marshal(presence.Build(state, presence.Options{}, "", time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "path") || !strings.Contains(string(data), "Get Cliamp") || !strings.Contains(string(data), "View Plugin") {
		t.Fatalf("unexpected activity payload: %s", data)
	}
}
