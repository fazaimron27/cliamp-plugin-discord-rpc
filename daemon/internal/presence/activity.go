// Package presence derives Discord activity payloads from playback state.
package presence

import (
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
)

var staticButtons = []Button{
	{Label: "Get Cliamp", URL: "https://www.cliamp.stream/"},
	{Label: "View Plugin", URL: "https://github.com/fazaimron27/cliamp-plugin-discord-rpc"},
}

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

type Assets struct {
	LargeImage string `json:"large_image,omitempty"`
	LargeText  string `json:"large_text,omitempty"`
}

type Timestamps struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

type Button struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// Build creates a Listening activity. Timestamp anchors come from the state
// document so heartbeat-only writes do not restart the progress bar.
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
		Buttons:           append([]Button(nil), staticButtons...),
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
		position := min(max(s.Position, 0), s.Duration)
		start := s.UpdatedAt - position
		if start <= 0 {
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
