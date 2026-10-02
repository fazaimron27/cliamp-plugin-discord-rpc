// Package tracklink maps a playback path to the public URLs it belongs to: the
// page, and where the provider publishes one, the artwork.
package tracklink

// This file holds the three mechanisms that turn a provider's path into a
// public URL, and it holds nothing else: no network, no cache, no dependency
// beyond the standard library. That is deliberate. Every value it is handed
// comes from a provider or from a player, so the whole package is a refusal
// machine first and a translator second — a path that matches nothing here is
// published nowhere, which is what keeps a local filename and a
// credential-bearing stream URL out of a Discord payload.
//
// The three mechanisms are different in kind, and each one guarantees less
// than the one before it:
//
//   - URI translation: a spotify:track:<id> or tidal://track/<id> URI has no
//     public form of its own, so it is translated into one.
//   - URL rebuild: a YouTube watch URL or a NetEase page URL is already
//     public, so it is published only after its host is checked and its
//     identity rebuilt from the id.
//   - Page pass-through: a SoundCloud or Mixcloud page has no id to rebuild
//     from, because the path slug is the identity, so the path is republished
//     after its host, its segment count and its segments are checked.
//
// All three are allowlists. A blocklist would fail open the moment a provider
// is added or a rule is missed, and five of Cliamp's providers already put a
// live credential in the path.

import (
	"fmt"
	"net/url"
	"strings"
)

const (
	spotifyName      = "Spotify"
	youtubeName      = "YouTube"
	youtubeMusicName = "YouTube Music"
	tidalName        = "Tidal"
	yandexName       = "Yandex Music"
	neteaseName      = "NetEase"
	soundcloudName   = "SoundCloud"
	mixcloudName     = "Mixcloud"

	spotifyTrackPrefix   = "spotify:track:"
	spotifyEpisodePrefix = "spotify:episode:"
	tidalTrackPrefix     = "tidal://track/"
	yandexTrackPrefix    = "yandex:track:"

	youtubeHost      = "youtube.com"
	youtubeMusicHost = "music.youtube.com"
	youtuBeHost      = "youtu.be"
	neteaseHost      = "music.163.com"
	soundcloudHost   = "soundcloud.com"
	mixcloudHost     = "mixcloud.com"

	// videoIDLength is the only length a YouTube video id has. Requiring it is
	// what makes "v=short" a refusal rather than a published link.
	videoIDLength = 11

	// youtubeThumbnail is the one thumbnail host and variant this package
	// publishes. maxresdefault is the better image and 404s for any video with
	// no HD source, which would leave the card with no image at all rather than
	// falling through to the asset below it; hqdefault always exists but is a
	// 4:3 canvas with the frame letterboxed inside it. mqdefault is the one
	// that both always exists and is clean 16:9.
	youtubeThumbnail = "https://i.ytimg.com/vi/%s/mqdefault.jpg"

	// neteaseSongRoute is the one fragment route that addresses a song. The
	// album and djradio routes share the host and the id parameter, so the
	// route is checked rather than the parameter alone.
	neteaseSongRoute = "/song"

	neteasePage = "https://music.163.com/song?id=%s"
)

// PublishableHTTPS parses raw and reports whether it is a URL this daemon may
// put on the card: HTTPS, and carrying no credential in its authority. The
// parsed URL comes back with the answer so a caller needing more of it does
// not parse twice.
//
// This is the one rule the five publishing sites share, and it lives here
// because this package is where a value that must not be published is refused.
// It is applied to the value rather than to what survives of it: the two
// rebuild mechanisms below and the page pass-through all consult it before
// they look at anything else, and so do the two artwork sources in the artwork
// package. Leaving a rebuild site to drop the credential by construction would
// make the guarantee a property of that rebuild instead of a property of the
// rule — true today, and silently untrue the moment a mechanism republishes a
// value it was handed.
//
// A caller that needs more says so where the extra requirement is visible:
// Last.fm's pages must also name a host, and the player's artwork must also
// sit on the CDN allowlist.
func PublishableHTTPS(raw string) (*url.URL, bool) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil {
		return nil, false
	}
	return parsed, true
}

// Link is a provider track's public identity: where it lives, and what to call
// the provider when the destination is named on the card.
type Link struct {
	Provider string
	URL      string
}

// Find reports the provider that owns path and the exact public URL for the
// track. ok is false for every path that must not be published: a local
// filesystem path, a radio stream, a self-hosted stream URL carrying a
// credential, and any provider not on either allowlist.
func Find(path string) (Link, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Link{}, false
	}
	if link, ok := fromURIPrefix(path); ok {
		return link, true
	}
	if link, ok := fromVideoURL(path); ok {
		return link, true
	}
	if link, ok := fromNeteaseURL(path); ok {
		return link, true
	}
	if link, ok := fromPageURL(path); ok {
		return link, true
	}
	return Link{}, false
}

// uriTranslations is the translation mechanism's allowlist: the opaque prefix
// a provider hands over, the provider it belongs to, the page it is translated
// into, and the check its id has to pass. A prefix that matches and an id that
// fails is a refusal rather than a reason to try the next entry, which is what
// keeps spotify:track:abc?x=1 from reaching a looser row below it.
var uriTranslations = []struct {
	prefix   string
	provider string
	page     string
	validID  func(string) bool
}{
	{spotifyTrackPrefix, spotifyName, "https://open.spotify.com/track/%s", isSpotifyID},
	{spotifyEpisodePrefix, spotifyName, "https://open.spotify.com/episode/%s", isSpotifyID},
	{tidalTrackPrefix, tidalName, "https://tidal.com/browse/track/%s", isNumericID},
	{yandexTrackPrefix, yandexName, "https://music.yandex.ru/track/%s", isNumericID},
}

// fromURIPrefix translates a provider URI into its public page. The id is
// checked before it is placed in the URL, which is what keeps a path traversal
// or a query string from being carried through.
func fromURIPrefix(path string) (Link, bool) {
	for _, translation := range uriTranslations {
		id, found := strings.CutPrefix(path, translation.prefix)
		if !found {
			continue
		}
		if !translation.validID(id) {
			return Link{}, false
		}
		return Link{
			Provider: translation.provider,
			URL:      fmt.Sprintf(translation.page, id),
		}, true
	}
	return Link{}, false
}

// isSpotifyID reports whether id is a bare Spotify identifier. Spotify ids are
// base62, so anything outside that charset is refused rather than escaped: a
// value that needs escaping was never an id.
func isSpotifyID(id string) bool {
	if id == "" {
		return false
	}
	for _, character := range id {
		if !isASCIIAlphanumeric(character) {
			return false
		}
	}
	return true
}

// isNumericID reports whether id is a bare Tidal or Yandex identifier, which
// are decimal. It is also what refuses the ":albumId" suffix a Yandex path can
// carry: client.plainID strips that from *like* ids and toPlaylistTracks never
// applies it, so a colon can reach the path, and a colon is not a digit.
func isNumericID(id string) bool {
	if id == "" {
		return false
	}
	for _, character := range id {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

// fromVideoURL handles the URL rebuild mechanism for video paths. The URL is
// rebuilt from the video id rather than reused, so a watch URL carrying list=,
// index= or t= is narrowed to the video and the host that is published is one
// we constructed.
func fromVideoURL(path string) (Link, bool) {
	name, canonicalHost, id, ok := videoPath(path)
	if !ok {
		return Link{}, false
	}
	return Link{
		Provider: name,
		URL:      "https://" + canonicalHost + "/watch?v=" + id,
	}, true
}

// videoPath extracts a video's provider name, canonical host and id from a
// path. Find and Artwork both go through it, so the id has exactly one
// validation site and a provider cannot become linkable without becoming
// artwork-able in the same change.
func videoPath(path string) (name, canonicalHost, id string, ok bool) {
	parsed, ok := PublishableHTTPS(path)
	if !ok {
		return "", "", "", false
	}
	host := normaliseHost(parsed.Hostname())

	switch host {
	case youtubeMusicHost:
		name, canonicalHost = youtubeMusicName, youtubeMusicHost
	case youtubeHost, youtuBeHost:
		name, canonicalHost = youtubeName, "www."+youtubeHost
	default:
		return "", "", "", false
	}

	id, ok = videoID(parsed, host)
	if !ok {
		return "", "", "", false
	}
	return name, canonicalHost, id, true
}

// fromNeteaseURL handles the rebuild mechanism for NetEase. The published URL
// is constructed from the validated id rather than reused, so the fragment
// route, a parameter beside the id, and the host itself cannot ride along.
func fromNeteaseURL(path string) (Link, bool) {
	parsed, ok := PublishableHTTPS(path)
	if !ok {
		return Link{}, false
	}
	if normaliseHost(parsed.Hostname()) != neteaseHost {
		return Link{}, false
	}
	id, ok := neteaseID(parsed)
	if !ok {
		return Link{}, false
	}
	return Link{Provider: neteaseName, URL: fmt.Sprintf(neteasePage, id)}, true
}

// neteaseID reads the song id from either form: the "#/song?id=<id>" fragment
// cliamp builds, and the plain "?id=<id>" that the site accepts and that
// survives a client which mangles fragments. A fragment addressing anything
// else is refused rather than read, and so is an id-free path on another
// route, because publishing a song page for an album's id is exactly the
// wrong-page failure this package exists to refuse.
func neteaseID(parsed *url.URL) (string, bool) {
	if parsed.Fragment != "" {
		route, err := url.Parse(parsed.Fragment)
		if err != nil || route.Path != neteaseSongRoute {
			return "", false
		}
		return neteaseSongID(route.Query().Get("id"))
	}
	if parsed.Path != "" && parsed.Path != "/" && parsed.Path != neteaseSongRoute {
		return "", false
	}
	return neteaseSongID(parsed.Query().Get("id"))
}

// neteaseSongID reports the id if it is one, and the empty string otherwise.
func neteaseSongID(id string) (string, bool) {
	if !isNumericID(id) {
		return "", false
	}
	return id, true
}

// pageProviders is the pass-through mechanism's allowlist: the hosts whose
// page URL is itself the track's identity, the name and canonical host to
// publish, and the path segments that are one of the provider's own tabs or
// namespaces rather than a track. A segment count alone does not draw that
// line — soundcloud.com/<user>/tracks is a profile tab and is two segments
// too — so the reserved words do, which is how yt-dlp draws it as well.
var pageProviders = []struct {
	host           string
	provider       string
	canonicalHost  string
	reservedFirst  []string
	reservedSecond []string
}{
	{
		host:          soundcloudHost,
		provider:      soundcloudName,
		canonicalHost: soundcloudHost,
		reservedFirst: []string{"stations"},
		reservedSecond: []string{
			"tracks", "albums", "sets", "reposts", "likes", "spotlight",
			"comments",
		},
	},
	{
		host:          mixcloudHost,
		provider:      mixcloudName,
		canonicalHost: "www." + mixcloudHost,
		reservedSecond: []string{
			"stream", "uploads", "favorites", "listens", "playlists",
		},
	},
}

// fromPageURL handles the pass-through mechanism. The published URL is built
// from the two validated segments rather than from the raw path, so a query, a
// fragment, a percent-encoding and a credential in the authority all have
// nowhere to sit. What it cannot construct is the segments themselves, which
// the provider chose — that is the whole of why this mechanism guarantees less
// than the two above it.
func fromPageURL(path string) (Link, bool) {
	parsed, ok := PublishableHTTPS(path)
	if !ok {
		return Link{}, false
	}
	host := normaliseHost(parsed.Hostname())
	for _, provider := range pageProviders {
		if provider.host != host {
			continue
		}
		first, second, ok := pageSegments(parsed)
		if !ok {
			return Link{}, false
		}
		if containsString(provider.reservedFirst, first) {
			return Link{}, false
		}
		if containsString(provider.reservedSecond, second) {
			return Link{}, false
		}
		return Link{
			Provider: provider.provider,
			URL: "https://" + provider.canonicalHost + "/" + first +
				"/" + second,
		}, true
	}
	return Link{}, false
}

// pageSegments splits a page path into the two segments that identify a track.
// Exactly two is the rule, and it is what refuses a SoundCloud set —
// soundcloud.com/<user>/sets/<slug> — which the reserved-word check alone
// would not catch.
func pageSegments(parsed *url.URL) (first, second string, ok bool) {
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) != 2 {
		return "", "", false
	}
	if !isSlug(segments[0]) || !isSlug(segments[1]) {
		return "", "", false
	}
	return segments[0], segments[1], true
}

// isSlug reports whether segment is a bare path identifier. The charset is
// narrower than either provider's own, because this mechanism republishes a
// string the provider chose and the accept-list is therefore the whole
// guarantee. Path is the decoded path, so holding to a bare charset is also
// what refuses an escape: %20 would arrive as a space, and %2F as a separator.
// A dot is outside the charset, which is what keeps ".." out with it.
func isSlug(segment string) bool {
	if segment == "" {
		return false
	}
	for _, character := range segment {
		if isASCIIAlphanumeric(character) {
			continue
		}
		if character != '-' && character != '_' {
			return false
		}
	}
	return true
}

// containsString reports whether values holds wanted.
func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// Artwork returns the public thumbnail for a video path, and false for every
// path this package will not link.
//
// It is the cheapest tier of the artwork ladder and the only one that costs
// nothing: a construction from a part videoPath has already validated, with no
// network, no cache and no host allowlist to keep. The variant is chosen for
// always existing — see youtubeThumbnail.
func Artwork(path string) (string, bool) {
	_, _, id, ok := videoPath(strings.TrimSpace(path))
	if !ok {
		return "", false
	}
	return fmt.Sprintf(youtubeThumbnail, id), true
}

// normaliseHost lowercases the host and drops a leading www. or m., mirroring
// the normalisation cliamp applies in playlist.IsYouTubeURL. Keep the two in
// step: a host this accepts and the player does not, or the reverse, is a
// difference nobody would look for.
func normaliseHost(host string) string {
	host = strings.ToLower(host)
	host = strings.TrimPrefix(host, "www.")
	host = strings.TrimPrefix(host, "m.")
	return host
}

// videoID extracts the video identity: the v parameter for the youtube hosts,
// and the path for youtu.be, which carries the id in the path instead.
func videoID(parsed *url.URL, host string) (string, bool) {
	id := parsed.Query().Get("v")
	if host == youtuBeHost {
		id = parsed.Path
	}
	id = strings.Trim(id, "/")
	if !isVideoID(id) {
		return "", false
	}
	return id, true
}

// isVideoID reports whether id is a bare YouTube video identifier.
func isVideoID(id string) bool {
	if len(id) != videoIDLength {
		return false
	}
	for _, character := range id {
		if !isASCIIAlphanumeric(character) && character != '-' && character != '_' {
			return false
		}
	}
	return true
}

func isASCIIAlphanumeric(character rune) bool {
	switch {
	case character >= 'a' && character <= 'z':
		return true
	case character >= 'A' && character <= 'Z':
		return true
	case character >= '0' && character <= '9':
		return true
	}
	return false
}

// artistSearchRoutes maps a provider to its artist search page. The key is the
// same name Find puts on a Link, so renaming or removing a route fails the
// artist search test, which drives Find and then ArtistSearch.
//
// The other direction is unguarded: a provider added to Find with no route
// here would publish a provider track link beside a Last.fm artist link, and
// no test would fail. Add the route with the provider.
var artistSearchRoutes = map[string]func(string) string{
	spotifyName: func(name string) string {
		return "https://open.spotify.com/search/" + url.PathEscape(name)
	},
	youtubeName: func(name string) string {
		return "https://www.youtube.com/results?search_query=" + url.QueryEscape(name)
	},
	youtubeMusicName: func(name string) string {
		return "https://music.youtube.com/search?q=" + url.QueryEscape(name)
	},
	soundcloudName: func(name string) string {
		return "https://soundcloud.com/search?q=" + url.QueryEscape(name)
	},
}

// ArtistSearch returns the provider's artist search page for name. It is the
// keyless tier of the artist link: no id exists to build an exact artist page
// from, so the name is searched for instead. A provider with no route, and an
// empty name, both report false.
func (l Link) ArtistSearch(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}
	route, ok := artistSearchRoutes[l.Provider]
	if !ok {
		return "", false
	}
	return route(name), true
}
