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
	activity := presence.Build(state, presence.Options{LargeImage: "cliamp"}, "https://img/cover.jpg", presence.Links{}, time.Unix(2000, 0))
	if activity.State != "Artist" || activity.StatusDisplayType != 1 {
		t.Fatalf("artist display = %+v", activity)
	}
	if activity.Assets == nil || activity.Assets.LargeImage != "https://img/cover.jpg" {
		t.Fatalf("assets = %+v", activity.Assets)
	}
	if activity.Timestamps == nil || activity.Timestamps.Start != 970 || activity.Timestamps.End != 1210 {
		t.Fatalf("timestamps = %+v", activity.Timestamps)
	}
	if len(activity.Buttons) != 2 || activity.Buttons[0].Label != "View on Last.fm" || activity.Buttons[0].URL != "https://www.last.fm/search?q=Artist+Track" || activity.Buttons[1].Label != "Get Cliamp Music Player" || activity.Buttons[1].URL != "https://www.cliamp.stream/" {
		t.Fatalf("buttons = %+v", activity.Buttons)
	}
}

// An artist or title that needs escaping is URL-encoded into the Last.fm search
// link rather than pasted raw.
func TestPresenceTrackButtonEncodesSearchQuery(t *testing.T) {
	state := playback.State{Status: "playing", Title: "Back in Black", Artist: "AC/DC", Duration: 255}
	activity := presence.Build(state, presence.Options{}, "", presence.Links{}, time.Unix(1000, 0))
	if len(activity.Buttons) == 0 {
		t.Fatalf("buttons = %+v", activity.Buttons)
	}
	if activity.Buttons[0].URL != "https://www.last.fm/search?q=AC%2FDC+Back+in+Black" {
		t.Fatalf("track url = %q", activity.Buttons[0].URL)
	}
}

// An absent or blank artist and an absent title each leave no search to build,
// so the card carries only the app button. A stream is no longer in this list:
// whatever is playing has a name, and a name is enough for the search tier.
func TestPresenceOmitsTrackButtonWhenNotLinkable(t *testing.T) {
	cases := []struct {
		name  string
		state playback.State
	}{
		{"empty artist", playback.State{Status: "playing", Title: "Track"}},
		{"blank artist", playback.State{Status: "playing", Title: "Track", Artist: "   "}},
		{"empty title", playback.State{Status: "playing", Artist: "Artist"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			activity := presence.Build(testCase.state, presence.Options{}, "", presence.Links{}, time.Unix(1000, 0))
			if len(activity.Buttons) != 1 || activity.Buttons[0].Label != "Get Cliamp Music Player" || activity.Buttons[0].URL != "https://www.cliamp.stream/" {
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
	activity := presence.Build(state, presence.Options{LargeImage: "cliamp", LargeText: "Cliamp"}, "", presence.Links{}, time.Unix(1000, 0))
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
	activity := presence.Build(state, presence.Options{}, "", presence.Links{}, time.Now())

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
	activity := presence.Build(state, presence.Options{}, "", presence.Links{}, time.Now())

	if !strings.HasSuffix(activity.Details, "...") || !strings.HasSuffix(activity.State, "...") {
		t.Fatalf("activity = %+v", activity)
	}
	if len([]rune(activity.Details)) != 48 || len([]rune(activity.State)) != 40 {
		t.Fatalf("details/state lengths = %d/%d", len([]rune(activity.Details)), len([]rune(activity.State)))
	}
}

// Discord rejects the entire activity when assets.large_text is shorter than two
// characters, so a one-character album is left out rather than sent and the card
// survives without that one field.
//
// The value is counted in runes because Discord counts characters. "∞" is one
// character and three bytes, so a byte-length check would pass it through and
// the payload would be refused.
func TestPresenceOmitsAlbumTextBelowDiscordsMinimum(t *testing.T) {
	for _, album := range []string{"∞", "🎵"} {
		t.Run(album, func(t *testing.T) {
			state := playback.State{Status: "playing", Title: "Track", Artist: "Artist", Album: album}
			activity := presence.Build(state, presence.Options{LargeImage: "cliamp", LargeText: "Cliamp"}, "", presence.Links{}, time.Now())

			if activity.Assets == nil {
				t.Fatalf("album %q produced no assets; the artwork should still be published", album)
			}
			if activity.Assets.LargeText != "" {
				t.Fatalf("large_text = %q (%d runes); want it omitted, because Discord refuses a one-character value",
					activity.Assets.LargeText, len([]rune(activity.Assets.LargeText)))
			}
			if activity.Assets.LargeImage != "cliamp" {
				t.Fatalf("large_image = %q; want the artwork kept", activity.Assets.LargeImage)
			}
		})
	}
}

// The same floor applies to the two fields beside the artwork, so a
// one-character title or artist is omitted rather than sent.
//
// Omitted, not blanked: an empty value is still a value on the wire and could be
// refused in its own right, which would trade one rejection for another, so the
// payload drops the key instead.
func TestPresenceOmitsTitleAndArtistBelowDiscordsMinimum(t *testing.T) {
	state := playback.State{Status: "playing", Title: "A", Artist: "∞", Album: "Album"}
	activity := presence.Build(state, presence.Options{LargeImage: "cliamp", LargeText: "Cliamp"}, "", presence.Links{}, time.Now())

	if activity.Details != "" {
		t.Fatalf("details = %q; want a one-character title omitted", activity.Details)
	}
	if activity.State != "" {
		t.Fatalf("state = %q; want a one-character artist omitted", activity.State)
	}

	data, err := json.Marshal(activity)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"details", "state"} {
		if _, present := payload[field]; present {
			t.Errorf("payload carries %s with nothing to put in it: %s", field, data)
		}
	}
}

// Two characters is the floor, not a threshold to clear by a margin, so a value
// of exactly two is published unchanged.
func TestPresenceKeepsFieldsAtDiscordsMinimum(t *testing.T) {
	state := playback.State{Status: "playing", Title: "EP", Artist: "EP", Album: "EP"}
	activity := presence.Build(state, presence.Options{LargeImage: "cliamp", LargeText: "Cliamp"}, "", presence.Links{}, time.Now())

	if activity.Details != "EP" || activity.State != "EP" {
		t.Fatalf("details/state = %q/%q; want both kept at two characters", activity.Details, activity.State)
	}
	if activity.Assets == nil || activity.Assets.LargeText != "EP" {
		t.Fatalf("assets = %+v; want large_text kept at two characters", activity.Assets)
	}
}

// The marshalled payload carries the public track metadata and both button
// labels, and never the local path or the plugin's own links.
func TestPresencePayloadContainsOnlyPublicTrackMetadata(t *testing.T) {
	state := playback.State{Status: "playing", Title: "Track", Artist: "Artist"}
	data, err := json.Marshal(presence.Build(state, presence.Options{}, "", presence.Links{}, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "path") || strings.Contains(string(data), "View Plugin") || strings.Contains(string(data), "github.com") || !strings.Contains(string(data), "Get Cliamp Music Player") || !strings.Contains(string(data), "View on Last.fm") {
		t.Fatalf("unexpected activity payload: %s", data)
	}
}

// A provider track links the card to the service it is playing from, and names
// that service on the button. The title, the artwork and the button all open
// the same page; the artist gets the provider's artist search, because no
// artist id exists anywhere in the pipeline to build an exact page from.
func TestPresenceLinksAProviderTrackToItsOwnService(t *testing.T) {
	state := playback.State{
		Status: "playing", Title: "Track", Artist: "Artist",
		Path: "spotify:track:4uLU6hMCjMI75M1A2tKUQC",
	}
	links := presence.Links{
		Provider:        "Spotify",
		ProviderURL:     "https://open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC",
		ArtistSearchURL: "https://open.spotify.com/search/Artist",
	}
	activity := presence.Build(state, presence.Options{}, "https://img/cover.jpg", links, time.Unix(1000, 0))

	if activity.DetailsURL != links.ProviderURL {
		t.Errorf("DetailsURL = %q; want the provider track page", activity.DetailsURL)
	}
	if activity.Assets == nil || activity.Assets.LargeURL != links.ProviderURL {
		t.Errorf("assets = %+v; want large_url set to the provider track page", activity.Assets)
	}
	if activity.StateURL != links.ArtistSearchURL {
		t.Errorf("StateURL = %q; want the provider artist search", activity.StateURL)
	}
	if len(activity.Buttons) != 2 || activity.Buttons[0].Label != "Listen on Spotify" || activity.Buttons[0].URL != links.ProviderURL {
		t.Errorf("buttons = %+v; want Listen on Spotify first", activity.Buttons)
	}
}

// With no provider link derived from the path, the Last.fm pages carry the
// card. They come out of the same track.getInfo response that supplied the
// artwork, so they cost no request the artwork did not already make.
func TestPresenceFallsBackToTheLastFmPages(t *testing.T) {
	state := playback.State{Status: "playing", Title: "Track", Artist: "Artist"}
	links := presence.Links{
		TrackURL:  "https://www.last.fm/music/Artist/_/Track",
		ArtistURL: "https://www.last.fm/music/Artist",
	}
	activity := presence.Build(state, presence.Options{}, "https://img/cover.jpg", links, time.Unix(1000, 0))

	if activity.DetailsURL != links.TrackURL {
		t.Errorf("DetailsURL = %q; want the Last.fm track page", activity.DetailsURL)
	}
	if activity.Assets == nil || activity.Assets.LargeURL != links.TrackURL {
		t.Errorf("assets = %+v; want large_url set to the Last.fm track page", activity.Assets)
	}
	if activity.StateURL != links.ArtistURL {
		t.Errorf("StateURL = %q; want the Last.fm artist page", activity.StateURL)
	}
	if len(activity.Buttons) != 2 || activity.Buttons[0].Label != "View on Last.fm" || activity.Buttons[0].URL != links.TrackURL {
		t.Errorf("buttons = %+v; want the Last.fm track page", activity.Buttons)
	}
}

// The artist follows the track's destination wherever it can. A provider link
// is the service the listener is actually playing from, so its search outranks
// the exact Last.fm artist page: a card whose button reads "Listen on Spotify"
// and whose artist opens Last.fm is pointing two ways at once. The Last.fm page
// takes over for the tracks no provider claims, and the Last.fm search covers
// everything else — so every tier but the last needs neither a key nor an id,
// and the artist stays linkable in more cases than the track is.
func TestPresenceArtistLinkPrecedence(t *testing.T) {
	cases := []struct {
		name  string
		state playback.State
		links presence.Links
		want  string
	}{
		{
			name:  "the provider search wins over the exact Last.fm artist page",
			state: playback.State{Status: "playing", Artist: "Artist"},
			links: presence.Links{
				Provider:        "Spotify",
				ArtistSearchURL: "https://open.spotify.com/search/Artist",
				ArtistURL:       "https://www.last.fm/music/Artist",
			},
			want: "https://open.spotify.com/search/Artist",
		},
		{
			name:  "the exact Last.fm artist page is used when no provider link was derived",
			state: playback.State{Status: "playing", Artist: "Artist"},
			links: presence.Links{ArtistURL: "https://www.last.fm/music/Artist"},
			want:  "https://www.last.fm/music/Artist",
		},
		{
			name:  "the Last.fm search is the final fallback",
			state: playback.State{Status: "playing", Artist: "AC/DC"},
			links: presence.Links{},
			want:  "https://www.last.fm/search?q=AC%2FDC",
		},
		{
			name:  "a stream links its artist too",
			state: playback.State{Status: "playing", Artist: "Artist", Stream: true},
			links: presence.Links{},
			want:  "https://www.last.fm/search?q=Artist",
		},
		{
			name:  "an empty artist has nothing to search for",
			state: playback.State{Status: "playing", Artist: "   "},
			links: presence.Links{},
			want:  "",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			activity := presence.Build(testCase.state, presence.Options{}, "", testCase.links, time.Unix(1000, 0))
			if activity.StateURL != testCase.want {
				t.Errorf("StateURL = %q; want %q", activity.StateURL, testCase.want)
			}
		})
	}
}

// YouTube arrives flagged as a stream because it plays through yt-dlp, and the
// stream short-circuit exists because live radio has no stable track identity.
// A watch URL is a stable identity — steadier than a search query — so a
// derived provider link has to override that flag, or a whole provider is
// excluded from linking.
func TestPresenceLinksAStreamWhenThePathIdentifiesTheTrack(t *testing.T) {
	state := playback.State{
		Status: "playing", Title: "Track", Artist: "Artist", Stream: true,
		Path: "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
	}
	links := presence.Links{
		Provider:    "YouTube",
		ProviderURL: "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
	}
	activity := presence.Build(state, presence.Options{}, "", links, time.Unix(1000, 0))

	if len(activity.Buttons) != 2 || activity.Buttons[0].Label != "Listen on YouTube" || activity.Buttons[0].URL != links.ProviderURL {
		t.Fatalf("buttons = %+v; want Listen on YouTube first", activity.Buttons)
	}
	if activity.DetailsURL != links.ProviderURL {
		t.Errorf("DetailsURL = %q; want the video", activity.DetailsURL)
	}
}

// The stream guard covers the search tier only, so whether a stream gets a
// track button must not depend on the unrelated question of whether a Last.fm
// key is configured. Whatever is playing has a name, and the search works from
// a name alone — so a stream gets the button with no key exactly as it does
// with one, and the card reads the same either way.
func TestPresenceLinksAKeylessStreamToASearch(t *testing.T) {
	state := playback.State{Status: "playing", Title: "Track", Artist: "Artist", Stream: true}
	activity := presence.Build(state, presence.Options{}, "", presence.Links{}, time.Unix(1000, 0))

	if len(activity.Buttons) != 2 {
		t.Fatalf("buttons = %+v; want a track button beside the app one", activity.Buttons)
	}
	if activity.Buttons[0].Label != "View on Last.fm" || activity.Buttons[0].URL != "https://www.last.fm/search?q=Artist+Track" {
		t.Errorf("buttons[0] = %+v; want the Last.fm search", activity.Buttons[0])
	}
}

// The artwork links to the track page whenever one is known, whether the image
// beside it is the track's own art or the fallback asset. The rule that decides
// this cannot be "is there album art": with no Last.fm key there never is, which
// would leave the artwork inert on a Spotify track whose page the daemon knows
// and on every track Last.fm has no image for.
func TestPresenceLinksTheArtworkEvenWithoutAlbumArt(t *testing.T) {
	state := playback.State{
		Status: "playing", Title: "Track", Artist: "Artist", Album: "Album",
		Path: "spotify:track:4uLU6hMCjMI75M1A2tKUQC",
	}
	links := presence.Links{
		Provider:    "Spotify",
		ProviderURL: "https://open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC",
	}
	activity := presence.Build(state, presence.Options{LargeImage: "cliamp"}, "", links, time.Unix(1000, 0))

	if activity.Assets == nil || activity.Assets.LargeImage != "cliamp" {
		t.Fatalf("assets = %+v; want the fallback asset", activity.Assets)
	}
	if activity.Assets.LargeURL != links.ProviderURL {
		t.Errorf("assets.LargeURL = %q; want %q", activity.Assets.LargeURL, links.ProviderURL)
	}
}

// Discord validates these fields for URI syntax alone and rejects the whole
// activity when one is malformed, so a field with nothing to point at has to be
// omitted rather than blanked.
func TestPresenceOmitsAbsentLinkFieldsRatherThanSendingEmptyOnes(t *testing.T) {
	state := playback.State{Status: "playing", Title: "Track", Artist: "   ", Stream: true}
	data, err := json.Marshal(presence.Build(state, presence.Options{}, "https://img/cover.jpg", presence.Links{}, time.Unix(1000, 0)))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"details_url", "state_url", "large_url"} {
		if strings.Contains(string(data), field) {
			t.Errorf("payload carries %s with no link to point it at: %s", field, data)
		}
	}
	if !strings.Contains(string(data), "large_image") {
		t.Errorf("payload dropped the artwork along with the links: %s", data)
	}
}
