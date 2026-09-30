// Package presence derives Discord activity payloads from playback state.
package presence

// This file is the presence payload. The Activity envelope and the Assets,
// Timestamps, and Button structs beneath it mirror the SET_ACTIVITY object
// Discord accepts, and Build fills them from a playback snapshot: the truncated
// text, the fallback asset, the buttons, and the timeline anchor.

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
	lastFMName     = "Last.fm"
	lastFMSearch   = "https://www.last.fm/search"
	viewOnPrefix   = "View on "
)

var getCliampButton = Button{Label: getCliampLabel, URL: getCliampURL}

// Options controls the static fallback asset shown when artwork is unavailable.
type Options struct {
	LargeImage string
	LargeText  string
}

// Links are the public URLs a snapshot resolves to, gathered so the payload
// builder only has to place them.
//
// An empty field is the absence of a link, not a link to nowhere: the field it
// would fill is left out of the payload entirely. Discord validates these
// fields for URI syntax alone and rejects the whole activity when one is
// malformed, so a value that is not known to be a URL must never be sent.
type Links struct {
	// Provider names the service ProviderURL belongs to, and becomes the button
	// label. It is empty when the playback path matched no allowlist.
	Provider string
	// ProviderURL is that service's own page for the track, derived from the
	// playback path.
	ProviderURL string
	// ArtistSearchURL is that service's artist search for this artist's name.
	// No artist ID exists anywhere in the pipeline, so a provider track can
	// offer a search but never an exact artist page.
	ArtistSearchURL string
	// TrackURL and ArtistURL are the pages Last.fm reported for the track and
	// its artist. They arrive with the artwork, so they cost no extra request.
	TrackURL  string
	ArtistURL string
}

// Key renders the links for comparison, so that a change to any one of them
// republishes the card.
//
// It exists because the provider link is derived from the playback path, and
// PresenceKey deliberately excludes the path: without a key that covers the
// derived URLs, a change that altered only the path would leave the previous
// provider's link on the card.
func (l Links) Key() string {
	return strings.Join([]string{l.Provider, l.ProviderURL, l.ArtistSearchURL, l.TrackURL, l.ArtistURL}, "\x00")
}

// Activity is the SET_ACTIVITY payload sent to Discord.
type Activity struct {
	Type              int         `json:"type"`
	Details           string      `json:"details"`
	State             string      `json:"state"`
	DetailsURL        string      `json:"details_url,omitempty"`
	StateURL          string      `json:"state_url,omitempty"`
	StatusDisplayType int         `json:"status_display_type"`
	Instance          bool        `json:"instance"`
	Assets            *Assets     `json:"assets,omitempty"`
	Timestamps        *Timestamps `json:"timestamps,omitempty"`
	Buttons           []Button    `json:"buttons,omitempty"`
}

// buttons returns the CTA pair for a snapshot: a track link when one was
// resolved, then the app link. Discord renders at most two buttons.
func buttons(s playback.State, links Links) []Button {
	if target, name := trackTarget(s, links); target != "" {
		return []Button{{Label: viewOnPrefix + name, URL: target}, getCliampButton}
	}
	return []Button{getCliampButton}
}

// trackTarget returns the URL the track button opens and the name to label it
// with. The provider's own page wins whenever the path yielded one, then the
// Last.fm track page, then Last.fm search.
//
// The label has to move with the destination: a fixed "View on Last.fm" would
// be wrong on every provider track, and wrong in the way the reader cannot
// detect from the card.
//
// Search stays as the last tier on purpose. It is the only one that works with
// neither a key nor a recognised path, so dropping it would leave anyone
// without a Last.fm key with no track button at all. The stream check sits here
// rather than around the whole function because a derived provider link is
// exactly what a stream can supply and a search cannot: YouTube is flagged as a
// stream because it plays through yt-dlp, and its path is a stable video id.
func trackTarget(s playback.State, links Links) (string, string) {
	if links.ProviderURL != "" {
		return links.ProviderURL, links.Provider
	}
	if links.TrackURL != "" {
		return links.TrackURL, lastFMName
	}
	artist := strings.TrimSpace(s.Artist)
	title := strings.TrimSpace(s.Title)
	if s.Stream || artist == "" || title == "" {
		return "", ""
	}
	query := url.Values{}
	query.Set("q", artist+" "+title)
	return lastFMSearch + "?" + query.Encode(), lastFMName
}

// trackPage is the exact page for the track, which is what the title and the
// artwork link to: the provider's own when the path yielded one, otherwise the
// Last.fm track page.
//
// A search page is a button destination, not a link on the title, so this is
// empty when neither exists.
func trackPage(links Links) string {
	if links.ProviderURL != "" {
		return links.ProviderURL
	}
	return links.TrackURL
}

// artistPage is the artist link, by a precedence of its own. An exact artist
// page is the only tier that needs anything the name cannot supply — which is
// why the artist is linkable in more cases than the track is: the provider
// search and the Last.fm search need neither a key nor an id.
func artistPage(s playback.State, links Links) string {
	name := strings.TrimSpace(s.Artist)
	if name == "" {
		return ""
	}
	if links.ArtistURL != "" {
		return links.ArtistURL
	}
	if links.ArtistSearchURL != "" {
		return links.ArtistSearchURL
	}
	query := url.Values{}
	query.Set("q", name)
	return lastFMSearch + "?" + query.Encode()
}

// Assets is the payload's asset object: the image Discord shows on the card
// and the text that appears when it is hovered. Build fills it with resolved
// album art, or with the configured fallback asset.
type Assets struct {
	LargeImage string `json:"large_image,omitempty"`
	LargeText  string `json:"large_text,omitempty"`
	LargeURL   string `json:"large_url,omitempty"`
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
func Build(s playback.State, options Options, artworkURL string, links Links, now time.Time) *Activity {
	artist := s.Artist
	if strings.TrimSpace(artist) == "" {
		artist = "Unknown artist"
	}
	track := trackPage(links)
	activity := &Activity{
		Type:              activityTypeListening,
		Details:           truncateRunes(s.Title, maxDetailsRunes),
		State:             truncateRunes(artist, maxStateRunes),
		DetailsURL:        track,
		StateURL:          artistPage(s, links),
		StatusDisplayType: statusDisplayState,
		Buttons:           buttons(s, links),
	}

	if artworkURL != "" {
		activity.Assets = &Assets{LargeImage: artworkURL, LargeText: truncate(s.Album, maxFieldBytes), LargeURL: track}
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
