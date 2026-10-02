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

	// minFieldRunes is Discord's floor for the text on an activity. Details,
	// state and the large-image hover text are each refused below two
	// characters, and a refused field refuses the whole activity -- so a value
	// this short is left out of the payload rather than sent.
	//
	// Counted in runes because Discord counts characters rather than bytes. "∞"
	// is one character and three bytes, so a byte-length check would let it
	// through to be rejected.
	minFieldRunes = 2

	getCliampLabel = "Get Cliamp Music Player"
	getCliampURL   = "https://www.cliamp.stream/"
	lastFMName     = "Last.fm"
	lastFMSearch   = "https://www.last.fm/search"

	// listenOnPrefix and viewOnPrefix are the two verbs the track button can
	// carry, one per kind of destination: a provider page is somewhere the track
	// is played, a Last.fm page is only ever read. See trackTarget.
	listenOnPrefix = "Listen on "
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
	// Provider names the service ProviderURL belongs to. Only the name is carried
	// here: the verb in front of it belongs to the destination rather than to the
	// service, and trackTarget adds it. It is empty when the playback path
	// matched no allowlist.
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
	Details           string      `json:"details,omitempty"`
	State             string      `json:"state,omitempty"`
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
	if target, label := trackTarget(s, links); target != "" {
		return []Button{{Label: label, URL: target}, getCliampButton}
	}
	return []Button{getCliampButton}
}

// trackTarget returns the URL the track button opens and the label to put on
// it. The provider's own page wins whenever the path yielded one, then the
// Last.fm track page, then Last.fm search.
//
// The label moves with the destination in both of its parts. In the name,
// because a fixed "View on Last.fm" would be wrong on every provider track, and
// wrong in the way the reader cannot detect from the card. In the verb, because
// the two destinations are different kinds of place: a provider page is where
// the listener goes to play the track again, so the track is listened on there,
// while a Last.fm page has no playback at all and is only ever read. "Listen on
// Last.fm" would be a plain falsehood — and the "View" it replaces was the
// other mistake, a card that says "Listening to" and then offers a song to look
// at.
//
// Every provider tracklink accepts is a service you play from, which is why the
// provider tier takes the listening verb unconditionally rather than consulting
// a list.
//
// Search stays as the last tier on purpose. It is the only one that works with
// neither a key nor a recognised path, so dropping it would leave anyone
// without a Last.fm key with no track button at all.
//
// There is deliberately no stream check. There used to be one, and it made the
// button depend on whether a Last.fm key was configured: a stream reached the
// Last.fm page tier whenever a key supplied a page, and only fell through to
// the guard when it did not. Whatever is playing has a name, a name is all the
// search tier needs, and YouTube — flagged as a stream because it plays through
// yt-dlp — is a case where linking is plainly right. So the button is offered
// to a stream on the same terms as to anything else.
func trackTarget(s playback.State, links Links) (target, label string) {
	if links.ProviderURL != "" {
		return links.ProviderURL, listenOnPrefix + links.Provider
	}
	if links.TrackURL != "" {
		return links.TrackURL, viewOnPrefix + lastFMName
	}
	artist := strings.TrimSpace(s.Artist)
	title := strings.TrimSpace(s.Title)
	if artist == "" || title == "" {
		return "", ""
	}
	query := url.Values{}
	query.Set("q", artist+" "+title)
	return lastFMSearch + "?" + query.Encode(), viewOnPrefix + lastFMName
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

// artistPage is the artist link, by a precedence of its own: the provider's
// own search first, then the exact Last.fm artist page, then the Last.fm search.
//
// The provider outranks the exact page on purpose. A provider link means the
// listener is playing from that service, and its search is the page they can do
// something with; the Last.fm artist page is the better link only for a track
// no provider claims. Ordering this the other way would put the artist on
// Last.fm whenever the key was configured, which is nearly always, leaving the
// provider tier to run for almost nobody — and pointing the artist somewhere
// other than the title and the button beside it.
//
// Only the first tier needs an id, and no artist id exists anywhere in the
// pipeline, so the artist is linkable in more cases than the track is: the
// provider search and the Last.fm search need neither a key nor an id.
func artistPage(s playback.State, links Links) string {
	name := strings.TrimSpace(s.Artist)
	if name == "" {
		return ""
	}
	if links.ArtistSearchURL != "" {
		return links.ArtistSearchURL
	}
	if links.ArtistURL != "" {
		return links.ArtistURL
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
		activity.Assets = &Assets{LargeImage: artworkURL, LargeText: truncateBytes(s.Album, maxFieldBytes), LargeURL: track}
	} else if options.LargeImage != "" || s.Album != "" {
		text := options.LargeText
		if s.Album != "" {
			text = s.Album
		}
		activity.Assets = &Assets{LargeImage: options.LargeImage, LargeText: truncateBytes(text, maxFieldBytes), LargeURL: track}
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

// truncateRunes caps a text field at maxRunes, keeping anything already
// shorter. A value below Discord's floor is dropped outright.
func truncateRunes(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) < minFieldRunes {
		return ""
	}
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes-3]) + "..."
}

// truncateBytes caps a text field at maxBytes on a rune boundary, keeping
// anything already shorter. A value below Discord's floor is dropped outright.
//
// The cap is in bytes because Discord's is: assets.large_text is limited by the
// size of the field on the wire, not by how many characters it draws, so a
// value built from multi-byte characters gives up characters to fit. Walking
// back to a rune boundary is what keeps the result decodable — a cut at an
// arbitrary byte offset can land inside a character, and the payload would then
// be refused for being malformed rather than simply long.
//
// Neither name in this pair is the default. truncateRunes caps the fields
// Discord limits by character count, and which cap a field falls under is a
// property of the field, so the caller has to know which one it needs.
func truncateBytes(value string, maxBytes int) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) < minFieldRunes {
		return ""
	}
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
