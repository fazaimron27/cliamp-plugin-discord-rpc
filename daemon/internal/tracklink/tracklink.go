// Package tracklink maps a playback path to the public page it belongs to.
package tracklink

// This file holds the two mechanisms that turn a provider's path into a public
// URL, and it holds nothing else: no network, no cache, no dependency beyond
// the standard library. That is deliberate. Every value it is handed comes from
// a provider or from a player, so the whole package is a refusal machine first
// and a translator second — a path that matches nothing here is published
// nowhere, which is what keeps a local filename and a credential-bearing
// stream URL out of a Discord payload.
//
// The two mechanisms are different in kind:
//
//   - URI translation: a spotify:track:<id> URI has no public form of its own,
//     so it is translated into one.
//   - URL pass-through: a YouTube watch URL is already public, so it is
//     published only after its host is checked and its identity rebuilt.
//
// Both are allowlists. A blocklist would fail open the moment a provider is
// added or a rule is missed, and five of Cliamp's providers already put a live
// credential in the path.

import (
	"net/url"
	"strings"
)

const (
	spotifyName      = "Spotify"
	youtubeName      = "YouTube"
	youtubeMusicName = "YouTube Music"

	spotifyTrackPrefix   = "spotify:track:"
	spotifyEpisodePrefix = "spotify:episode:"

	youtubeHost      = "youtube.com"
	youtubeMusicHost = "music.youtube.com"
	youtuBeHost      = "youtu.be"

	// videoIDLength is the only length a YouTube video id has. Requiring it is
	// what makes "v=short" a refusal rather than a published link.
	videoIDLength = 11
)

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
	if link, ok := fromSpotifyURI(path); ok {
		return link, true
	}
	if link, ok := fromVideoURL(path); ok {
		return link, true
	}
	return Link{}, false
}

// fromSpotifyURI translates a Spotify URI into its public page. The id is
// charset-checked before it is placed in the URL, which is what keeps a path
// traversal or a query string from being carried through.
func fromSpotifyURI(path string) (Link, bool) {
	candidates := []struct{ prefix, kind string }{
		{spotifyTrackPrefix, "track"},
		{spotifyEpisodePrefix, "episode"},
	}
	for _, candidate := range candidates {
		id, found := strings.CutPrefix(path, candidate.prefix)
		if !found {
			continue
		}
		if !isSpotifyID(id) {
			return Link{}, false
		}
		return Link{
			Provider: spotifyName,
			URL:      "https://open.spotify.com/" + candidate.kind + "/" + id,
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

// fromVideoURL handles the pass-through mechanism. The URL is rebuilt from the
// video id rather than reused, so a watch URL carrying list=, index= or t= is
// narrowed to the video and the host that is published is one we constructed.
func fromVideoURL(path string) (Link, bool) {
	parsed, err := url.Parse(path)
	if err != nil || parsed.Scheme != "https" {
		return Link{}, false
	}
	host := normaliseHost(parsed.Hostname())

	name, canonicalHost := "", ""
	switch host {
	case youtubeMusicHost:
		name, canonicalHost = youtubeMusicName, youtubeMusicHost
	case youtubeHost, youtuBeHost:
		name, canonicalHost = youtubeName, "www."+youtubeHost
	default:
		return Link{}, false
	}

	id, ok := videoID(parsed, host)
	if !ok {
		return Link{}, false
	}
	return Link{
		Provider: name,
		URL:      "https://" + canonicalHost + "/watch?v=" + id,
	}, true
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
// same name Find puts on a Link, so the two cannot drift apart without the
// route test failing.
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
