// Package presence derives Discord activity payloads from playback state.
package presence

import (
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/playback"
)

const (
	activityTypeListening = 2
	statusDisplayState    = 1
	maxFieldBytes         = 128
	maxDetailsRunes       = 48
	maxStateRunes         = 40

	getCliampLabel = "Get Cliamp"
	getCliampURL   = "https://www.cliamp.stream/"
	trackLabel     = "View on Last.fm"
	trackSearchURL = "https://www.last.fm/search"
)

var getCliampButton = Button{Label: getCliampLabel, URL: getCliampURL}

// Options controls the static fallback asset shown when artwork is unavailable.
type Options struct {
	LargeImage string
	LargeText  string
}

// Activity is the SET_ACTIVITY payload sent to Discord.
type Activity struct {
	Type              int         `json:"type"`
	Details           string      `json:"details"`
	State             string      `json:"state"`
	StatusDisplayType int         `json:"status_display_type"`
	Instance          bool        `json:"instance"`
	Assets            *Assets     `json:"assets,omitempty"`
	Timestamps        *Timestamps `json:"timestamps,omitempty"`
	Buttons           []Button    `json:"buttons,omitempty"`
}

// buttons returns the CTA pair for a snapshot: a track link when the metadata
// identifies a song, then the app link. Discord renders at most two buttons,
// and a stream carries no stable track identity to look up.
func buttons(s playback.State) []Button {
	artist := strings.TrimSpace(s.Artist)
	title := strings.TrimSpace(s.Title)
	if s.Stream || artist == "" || title == "" {
		return []Button{getCliampButton}
	}
	query := url.Values{}
	query.Set("q", artist+" "+title)
	return []Button{
		{Label: trackLabel, URL: trackSearchURL + "?" + query.Encode()},
		getCliampButton,
	}
}

// Assets is the payload's asset object: the image Discord shows on the card
// and the text that appears when it is hovered. Build fills it with resolved
// album art, or with the configured fallback asset.
type Assets struct {
	LargeImage string `json:"large_image,omitempty"`
	LargeText  string `json:"large_text,omitempty"`
}

// Timestamps is the payload's timeline object, in Unix seconds. Discord
// advances the bar between updates, so the daemon sends the two endpoints
// rather than republishing a position.
type Timestamps struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// Button is one link button on the card. Discord renders at most two, which is
// the limit buttons builds its pair against.
type Button struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// Build creates a Listening activity. StartedAt is derived from the plugin
// event timestamp and playback position, so ordinary snapshots preserve the
// progress timeline while seeks and resumes establish a new anchor.
func Build(s playback.State, options Options, artworkURL string, now time.Time) *Activity {
	artist := s.Artist
	if strings.TrimSpace(artist) == "" {
		artist = "Unknown artist"
	}
	activity := &Activity{
		Type:              activityTypeListening,
		Details:           truncateRunes(s.Title, maxDetailsRunes),
		State:             truncateRunes(artist, maxStateRunes),
		StatusDisplayType: statusDisplayState,
		Buttons:           buttons(s),
	}

	if artworkURL != "" {
		activity.Assets = &Assets{LargeImage: artworkURL, LargeText: truncate(s.Album, maxFieldBytes)}
	} else if options.LargeImage != "" || s.Album != "" {
		text := options.LargeText
		if s.Album != "" {
			text = s.Album
		}
		activity.Assets = &Assets{LargeImage: options.LargeImage, LargeText: truncate(text, maxFieldBytes)}
	}

	if s.Status == "playing" && s.Duration > 0 {
		start := s.StartedAt
		if start <= 0 {
			position := min(max(s.Position, 0), s.Duration)
			start = now.Unix() - position
		}
		activity.Timestamps = &Timestamps{Start: start, End: start + s.Duration}
	}
	return activity
}

func truncateRunes(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes-3]) + "..."
}

func truncate(value string, maxBytes int) string {
	value = strings.TrimSpace(value)
	if len(value) <= maxBytes {
		return value
	}
	limit := maxBytes - 3
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	if limit == 0 {
		return "..."
	}
	return value[:limit] + "..."
}
