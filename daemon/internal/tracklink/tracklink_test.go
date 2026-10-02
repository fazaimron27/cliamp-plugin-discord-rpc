package tracklink

// This file tests the refusal first and the translation second. Most of what it
// asserts is that a path produces no link at all — every value a provider or a
// player has actually handed this daemon, including the five self-hosted
// providers whose stream URLs carry a live credential.

import (
	"strings"
	"testing"
)

// refusedPaths is the corpus every refusal guard in this package iterates. It
// is one list rather than one per entry point on purpose: the property worth
// holding is that a path Find refuses, Artwork refuses too, and that property
// is only total if both are driven by the same corpus.
var refusedPaths = []struct {
	name string
	path string
}{
	{"empty path", ""},
	{"whitespace only", "   "},
	{"local filesystem path", "/home/faza/Music/AC-DC/Back in Black.flac"},
	{"relative filesystem path", "Music/song.mp3"},
	{"navidrome stream carries a credential", "https://music.example.com/rest/stream?id=1&u=faza&t=deadbeef"},
	{"plex stream carries a token", "https://plex.example.com/library/parts/9/file.flac?X-Plex-Token=secret"},
	{"jellyfin stream carries an api key", "https://jf.example.com/media/Items/track-1/Download?api_key=new-token"},
	{"audiobookshelf stream carries a token", "https://abs.example.com/api/items/i1/file/1?token=auth"},
	{"radio stream has no stable identity", "https://stream.example.com/live.mp3"},
	{"tidal with no id", "tidal://track/"},
	{"tidal with a non-numeric id", "tidal://track/abc"},
	{"tidal album is not a track", "tidal://album/12345"},
	{"yandex with no id", "yandex:track:"},
	{"yandex with a non-numeric id", "yandex:track:abc"},
	{"yandex with an album suffix is refused", "yandex:track:123:456"},
	{"lyrion is self-hosted", "lyrion://track/12345"},
	{"netease with a non-numeric id", "https://music.163.com/#/song?id=abc"},
	{"netease with no id", "https://music.163.com/#/song"},
	{"netease album page is not a track", "https://music.163.com/#/album?id=12345"},
	{"netease dj radio is not a track", "https://music.163.com/#/djradio?id=12345"},
	{"netease path is not the song route", "https://music.163.com/album?id=12345"},
	{"netease over http is refused", "http://music.163.com/song?id=12345"},
	{"netease lookalike host", "https://music.163.com.evil.example/song?id=12345"},
	{"netease with a credential in the authority", "https://user:secret@music.163.com/#/song?id=12345"},
	{"soundcloud profile tab is not a track", "https://soundcloud.com/artist/tracks"},
	{"soundcloud set is not a track", "https://soundcloud.com/artist/sets/summer"},
	{"soundcloud stations namespace is refused", "https://soundcloud.com/stations/track"},
	{"soundcloud with one segment", "https://soundcloud.com/artist"},
	{"soundcloud with three segments", "https://soundcloud.com/artist/song/extra"},
	{"soundcloud with a path traversal", "https://soundcloud.com/../evil"},
	{"soundcloud with a percent-encoded segment", "https://soundcloud.com/artist/a%20b"},
	{"soundcloud with an encoded separator", "https://soundcloud.com/artist%2Fevil/song"},
	{"soundcloud with a credential in the authority", "https://user:secret@soundcloud.com/artist/song"},
	{"soundcloud over http is refused", "http://soundcloud.com/artist/song"},
	{"soundcloud lookalike host", "https://soundcloud.com.evil.example/artist/song"},
	{"mixcloud profile tab is not a track", "https://www.mixcloud.com/artist/stream"},
	{"mixcloud with one segment", "https://www.mixcloud.com/artist"},
	{"mixcloud with three segments", "https://www.mixcloud.com/artist/show/extra"},
	{"mixcloud beta host is not on the allowlist", "https://beta.mixcloud.com/artist/show"},
	{"mixcloud over http is refused", "http://www.mixcloud.com/artist/show"},
	{"bandcamp has no provider entry", "https://artist.bandcamp.com/track/song"},
	{"spotify album is not a track", "spotify:album:1DFixLWuPkv3KT3TnV35m3"},
	{"spotify track with no id", "spotify:track:"},
	{"spotify track with a path traversal", "spotify:track:../../../evil"},
	{"spotify track with a query", "spotify:track:abc?x=1"},
	{"spotify track with a fragment", "spotify:track:abc#x"},
	{"youtube over http is refused", "http://www.youtube.com/watch?v=dQw4w9WgXcQ"},
	{"youtube with a javascript scheme", "javascript:alert(1)"},
	{"youtube lookalike host", "https://youtube.com.evil.example/watch?v=dQw4w9WgXcQ"},
	{"youtube with no video id", "https://www.youtube.com/watch"},
	{"youtube with a truncated video id", "https://www.youtube.com/watch?v=short"},
	{"youtube with a video id outside the charset", "https://www.youtube.com/watch?v=dQw4w9WgXc."},
	{"youtube with a credential in the authority", "https://user:secret@www.youtube.com/watch?v=dQw4w9WgXcQ"},
	{"youtube music over http is refused", "http://music.youtube.com/watch?v=dQw4w9WgXcQ"},
}

// TestFindRefusesEveryPathWeMustNotPublish is the guard that matters most. Each
// case is a value a provider or a player has actually handed this daemon, and
// every one of them must come back unlinked: a local filesystem path, a radio
// stream with no stable identity, and above all the self-hosted stream URLs,
// which carry a live credential in the path itself.
func TestFindRefusesEveryPathWeMustNotPublish(t *testing.T) {
	for _, testCase := range refusedPaths {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := Find(testCase.path)
			if ok {
				t.Fatalf("Find(%q) = %+v, ok=true; want ok=false", testCase.path, got)
			}
			if got.URL != "" {
				t.Fatalf("Find(%q) returned a URL %q alongside ok=false", testCase.path, got.URL)
			}
		})
	}
}

// TestArtworkRefusesEveryPathFindRefuses is the guard that keeps the two entry
// points from drifting apart. They share one parser, so a path that yields no
// link must yield no thumbnail: if this fails, a credential-bearing path or a
// local filename has found a second way onto the card.
func TestArtworkRefusesEveryPathFindRefuses(t *testing.T) {
	for _, testCase := range refusedPaths {
		t.Run(testCase.name, func(t *testing.T) {
			if got, ok := Artwork(testCase.path); ok {
				t.Errorf("Artwork(%q) = %q, ok=true; want ok=false", testCase.path, got)
			}
		})
	}
}

// TestArtworkDerivesAThumbnailOnlyForVideoPaths covers the derived tier: a
// video path yields the thumbnail for its own id, through the same host
// normalisation and the same narrowing Find applies.
func TestArtworkDerivesAThumbnailOnlyForVideoPaths(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{
			name: "youtube watch",
			path: "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
			want: "https://i.ytimg.com/vi/dQw4w9WgXcQ/mqdefault.jpg",
		},
		{
			name: "youtu.be short link",
			path: "https://youtu.be/dQw4w9WgXcQ",
			want: "https://i.ytimg.com/vi/dQw4w9WgXcQ/mqdefault.jpg",
		},
		{
			name: "youtube music shares the video id",
			path: "https://music.youtube.com/watch?v=dQw4w9WgXcQ",
			want: "https://i.ytimg.com/vi/dQw4w9WgXcQ/mqdefault.jpg",
		},
		{
			name: "a playlist and a timestamp do not reach the thumbnail",
			path: "https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=PLabcdefg&index=4&t=42s",
			want: "https://i.ytimg.com/vi/dQw4w9WgXcQ/mqdefault.jpg",
		},
		{
			name: "mobile host is normalised",
			path: "https://m.youtube.com/watch?v=dQw4w9WgXcQ",
			want: "https://i.ytimg.com/vi/dQw4w9WgXcQ/mqdefault.jpg",
		},
		{
			name: "an uppercase host is normalised",
			path: "https://WWW.YouTube.COM/watch?v=dQw4w9WgXcQ",
			want: "https://i.ytimg.com/vi/dQw4w9WgXcQ/mqdefault.jpg",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := Artwork(testCase.path)
			if !ok {
				t.Fatalf("Artwork(%q) ok=false; want a thumbnail", testCase.path)
			}
			if got != testCase.want {
				t.Fatalf("Artwork(%q) = %q; want %q", testCase.path, got, testCase.want)
			}
		})
	}
}

// TestFindNeverPublishesACredentialSubstring is the second half of the guard:
// refusal is not enough on its own, because a future change could start
// returning a URL it derived from a credential-bearing path. The tokens below
// are the markers each self-hosted server puts in its stream URL.
func TestFindNeverPublishesACredentialSubstring(t *testing.T) {
	credentials := []string{"deadbeef", "secret-token", "auth-token", "new-token", "faza"}
	paths := []string{
		"https://music.example.com/rest/stream?id=1&u=faza&t=deadbeef",
		"https://plex.example.com/library/parts/9/file.flac?X-Plex-Token=secret-token",
		"https://jf.example.com/media/Items/track-1/Download?api_key=new-token",
		"https://abs.example.com/api/items/i1/file/1?token=auth-token",
	}
	for _, path := range paths {
		link, _ := Find(path)
		for _, credential := range credentials {
			if strings.Contains(link.URL, credential) {
				t.Errorf("Find(%q) leaked %q into %q", path, credential, link.URL)
			}
		}
	}
}

// TestFindTranslatesOpaqueURIs covers the translation mechanism: a URI with no
// public form of its own — spotify:track:<id>, tidal://track/<id>,
// yandex:track:<id> — is translated into one after its id is checked.
func TestFindTranslatesOpaqueURIs(t *testing.T) {
	cases := []struct {
		name string
		path string
		want Link
	}{
		{
			name: "spotify track",
			path: "spotify:track:4uLU6hMCjMI75M1A2tKUQC",
			want: Link{Provider: "Spotify", URL: "https://open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC"},
		},
		{
			name: "spotify episode",
			path: "spotify:episode:512ojhOuo1ktJprKbVcKyQ",
			want: Link{Provider: "Spotify", URL: "https://open.spotify.com/episode/512ojhOuo1ktJprKbVcKyQ"},
		},
		{
			name: "tidal track",
			path: "tidal://track/123456789",
			want: Link{Provider: "Tidal", URL: "https://tidal.com/browse/track/123456789"},
		},
		{
			name: "yandex track",
			path: "yandex:track:12345678",
			want: Link{Provider: "Yandex Music", URL: "https://music.yandex.ru/track/12345678"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := Find(testCase.path)
			if !ok {
				t.Fatalf("Find(%q) ok=false; want a link", testCase.path)
			}
			if got != testCase.want {
				t.Fatalf("Find(%q) = %+v; want %+v", testCase.path, got, testCase.want)
			}
		})
	}
}

// TestFindPassesThroughYouTubeURLsNarrowedToTheVideo covers the URL mechanism
// and the narrowing rule: a watch URL can carry playlist and tracking
// parameters, and only the video identity may be republished.
func TestFindPassesThroughYouTubeURLsNarrowedToTheVideo(t *testing.T) {
	cases := []struct {
		name string
		path string
		want Link
	}{
		{
			name: "youtube watch",
			path: "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
			want: Link{Provider: "YouTube", URL: "https://www.youtube.com/watch?v=dQw4w9WgXcQ"},
		},
		{
			name: "playlist and time are dropped",
			path: "https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=PLabcdefg&index=4&t=42s",
			want: Link{Provider: "YouTube", URL: "https://www.youtube.com/watch?v=dQw4w9WgXcQ"},
		},
		{
			name: "parameters before the video id still resolve",
			path: "https://www.youtube.com/watch?list=PLabcdefg&v=dQw4w9WgXcQ",
			want: Link{Provider: "YouTube", URL: "https://www.youtube.com/watch?v=dQw4w9WgXcQ"},
		},
		{
			name: "mobile host is normalised",
			path: "https://m.youtube.com/watch?v=dQw4w9WgXcQ",
			want: Link{Provider: "YouTube", URL: "https://www.youtube.com/watch?v=dQw4w9WgXcQ"},
		},
		{
			name: "uppercase host is normalised",
			path: "https://WWW.YouTube.COM/watch?v=dQw4w9WgXcQ",
			want: Link{Provider: "YouTube", URL: "https://www.youtube.com/watch?v=dQw4w9WgXcQ"},
		},
		{
			name: "youtu.be short link",
			path: "https://youtu.be/dQw4w9WgXcQ",
			want: Link{Provider: "YouTube", URL: "https://www.youtube.com/watch?v=dQw4w9WgXcQ"},
		},
		{
			name: "youtube music keeps its host",
			path: "https://music.youtube.com/watch?v=dQw4w9WgXcQ",
			want: Link{Provider: "YouTube Music", URL: "https://music.youtube.com/watch?v=dQw4w9WgXcQ"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := Find(testCase.path)
			if !ok {
				t.Fatalf("Find(%q) ok=false; want a link", testCase.path)
			}
			if got != testCase.want {
				t.Fatalf("Find(%q) = %+v; want %+v", testCase.path, got, testCase.want)
			}
		})
	}
}

// TestFindRebuildsNeteaseURLs covers the second instance of the rebuild
// mechanism. The id is in a fragment in the form cliamp builds and in the
// query in the form the site also accepts, and the published URL is
// constructed from the id either way, so neither the fragment route nor any
// parameter beside it can ride along.
func TestFindRebuildsNeteaseURLs(t *testing.T) {
	cases := []struct {
		name string
		path string
		want Link
	}{
		{
			name: "the fragment form cliamp builds",
			path: "https://music.163.com/#/song?id=17241424",
			want: Link{Provider: "NetEase", URL: "https://music.163.com/song?id=17241424"},
		},
		{
			name: "the fragment-free form",
			path: "https://music.163.com/song?id=17241424",
			want: Link{Provider: "NetEase", URL: "https://music.163.com/song?id=17241424"},
		},
		{
			name: "parameters beside the id are dropped",
			path: "https://music.163.com/#/song?id=17241424&userid=1&from=search",
			want: Link{Provider: "NetEase", URL: "https://music.163.com/song?id=17241424"},
		},
		{
			name: "an uppercase host is normalised",
			path: "https://Music.163.COM/#/song?id=17241424",
			want: Link{Provider: "NetEase", URL: "https://music.163.com/song?id=17241424"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := Find(testCase.path)
			if !ok {
				t.Fatalf("Find(%q) ok=false; want a link", testCase.path)
			}
			if got != testCase.want {
				t.Fatalf("Find(%q) = %+v; want %+v", testCase.path, got, testCase.want)
			}
		})
	}
}

// TestFindPassesThroughPageURLsAfterNarrowing covers the third mechanism. A
// SoundCloud or Mixcloud page has no id to rebuild from, so the path itself is
// republished — after its host is checked against the allowlist, its segment
// count is fixed at two, both segments are held to a bare charset, and the
// query and the fragment are dropped entirely.
func TestFindPassesThroughPageURLsAfterNarrowing(t *testing.T) {
	cases := []struct {
		name string
		path string
		want Link
	}{
		{
			name: "soundcloud page",
			path: "https://soundcloud.com/artist/song",
			want: Link{Provider: "SoundCloud", URL: "https://soundcloud.com/artist/song"},
		},
		{
			name: "the query is dropped",
			path: "https://soundcloud.com/artist/song?in=other/sets&si=abc",
			want: Link{Provider: "SoundCloud", URL: "https://soundcloud.com/artist/song"},
		},
		{
			name: "the mobile host is normalised",
			path: "https://m.soundcloud.com/artist/song",
			want: Link{Provider: "SoundCloud", URL: "https://soundcloud.com/artist/song"},
		},
		{
			name: "the uppercase host is normalised",
			path: "https://SOUNDCLOUD.COM/artist/song",
			want: Link{Provider: "SoundCloud", URL: "https://soundcloud.com/artist/song"},
		},
		{
			name: "an underscore is a slug character",
			path: "https://soundcloud.com/ac_dc/back_in_black",
			want: Link{Provider: "SoundCloud", URL: "https://soundcloud.com/ac_dc/back_in_black"},
		},
		{
			name: "mixcloud page",
			path: "https://www.mixcloud.com/artist/show",
			want: Link{Provider: "Mixcloud", URL: "https://www.mixcloud.com/artist/show"},
		},
		{
			name: "mixcloud trailing slash is normalised away",
			path: "https://www.mixcloud.com/artist/show/",
			want: Link{Provider: "Mixcloud", URL: "https://www.mixcloud.com/artist/show"},
		},
		{
			name: "the fragment is dropped",
			path: "https://www.mixcloud.com/artist/show#t=1m",
			want: Link{Provider: "Mixcloud", URL: "https://www.mixcloud.com/artist/show"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := Find(testCase.path)
			if !ok {
				t.Fatalf("Find(%q) ok=false; want a link", testCase.path)
			}
			if got != testCase.want {
				t.Fatalf("Find(%q) = %+v; want %+v", testCase.path, got, testCase.want)
			}
		})
	}
}

// TestArtistSearchEscapesTheName covers the one string this package composes
// rather than translates: the artist name comes from metadata and can contain
// separators that would otherwise break the route.
func TestArtistSearchEscapesTheName(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{
			name: "Spotify",
			path: "spotify:track:4uLU6hMCjMI75M1A2tKUQC",
			want: "https://open.spotify.com/search/AC%2FDC",
		},
		{
			name: "YouTube",
			path: "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
			want: "https://www.youtube.com/results?search_query=AC%2FDC",
		},
		{
			name: "YouTube Music",
			path: "https://music.youtube.com/watch?v=dQw4w9WgXcQ",
			want: "https://music.youtube.com/search?q=AC%2FDC",
		},
		{
			name: "SoundCloud",
			path: "https://soundcloud.com/artist/song",
			want: "https://soundcloud.com/search?q=AC%2FDC",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			link, ok := Find(testCase.path)
			if !ok {
				t.Fatalf("Find(%q) ok=false; want a link", testCase.path)
			}
			got, hasSearch := link.ArtistSearch("AC/DC")
			if !hasSearch {
				t.Fatalf("%s has no artist search; want one", testCase.name)
			}
			if got != testCase.want {
				t.Fatalf("%s artist search = %q; want %q", testCase.name, got, testCase.want)
			}
		})
	}
}

// TestArtistSearchRefusesAnEmptyName covers the one input it cannot compose a
// route for: a track with no artist has nothing to search for.
func TestArtistSearchRefusesAnEmptyName(t *testing.T) {
	link, ok := Find("spotify:track:4uLU6hMCjMI75M1A2tKUQC")
	if !ok {
		t.Fatal("Find(spotify track) ok=false; want a link")
	}
	for _, name := range []string{"", "   "} {
		if got, hasSearch := link.ArtistSearch(name); hasSearch {
			t.Errorf("ArtistSearch(%q) = %q, true; want false", name, got)
		}
	}
}

// linkablePaths is one path per shape Find accepts. It is the accept side of
// refusedPaths, and it exists for the guard below rather than for its own
// assertions: every row must still be accepted, and the provider it yields
// must either offer an artist search or be named in routeLessProviders.
//
// Adding a provider to Find means adding its row here. That one duplicated
// path per provider is deliberate — the assertions on the exact URL belong to
// the test for the mechanism that builds it, while this list only asks which
// providers exist, which is the question the guard needs answered.
var linkablePaths = []struct {
	name string
	path string
}{
	{"spotify track", "spotify:track:4uLU6hMCjMI75M1A2tKUQC"},
	{"spotify episode", "spotify:episode:512ojhOuo1ktJprKbVcKyQ"},
	{"youtube watch", "https://www.youtube.com/watch?v=dQw4w9WgXcQ"},
	{"youtube music watch", "https://music.youtube.com/watch?v=dQw4w9WgXcQ"},
	{"tidal track", "tidal://track/123456789"},
	{"yandex track", "yandex:track:12345678"},
	{"netease song", "https://music.163.com/#/song?id=17241424"},
	{"soundcloud page", "https://soundcloud.com/artist/song"},
	{"mixcloud page", "https://www.mixcloud.com/artist/show"},
}

// routeLessProviders names the providers that deliberately have no artist
// search route, each with the reason. It is a list rather than a hole in the
// guard: a provider that is neither routed nor named here fails it, and a
// provider named here still has to say why. Four of the five providers this
// work links have no confirmed route, so this list starts empty and fills.
var routeLessProviders = map[string]string{
	tidalName:    "no search route confirmed; the artist falls back to Last.fm",
	yandexName:   "no search route confirmed; the artist falls back to Last.fm",
	neteaseName:  "no search route confirmed; the artist falls back to Last.fm",
	mixcloudName: "no search route confirmed; the artist falls back to Last.fm",
}

// TestEveryLinkableProviderHasAnArtistRoute is the guard that closes the one
// direction the two tables could drift in. The artist search test drives Find
// and then ArtistSearch, so a route that is removed or renamed fails there; a
// provider added to Find with no route failed nothing at all, and would put a
// provider track link on the card beside a Last.fm artist link.
func TestEveryLinkableProviderHasAnArtistRoute(t *testing.T) {
	for _, testCase := range linkablePaths {
		t.Run(testCase.name, func(t *testing.T) {
			link, ok := Find(testCase.path)
			if !ok {
				t.Fatalf("Find(%q) ok=false; want a link", testCase.path)
			}
			if _, hasRoute := link.ArtistSearch("AC/DC"); hasRoute {
				return
			}
			reason, documented := routeLessProviders[link.Provider]
			if !documented {
				t.Fatalf("%s has no artist route and is not in routeLessProviders", link.Provider)
			}
			if strings.TrimSpace(reason) == "" {
				t.Fatalf("%s is in routeLessProviders with no reason given", link.Provider)
			}
		})
	}
}

// maxButtonLabel is Discord's cap on a rich presence button label, taken from
// the Activity Button object in its gateway documentation: 1-32 characters.
// labelVerb is the longest prefix internal/presence puts in front of a provider
// name, so the name is the part with room to spare — and a name's length is
// decided here.
//
// It is a guarded constraint rather than a measured one: the SET_ACTIVITY reply
// is a blind echo, so nothing in this repository can observe what Discord does
// with a label it dislikes. A documented cap that nothing checks is a cap that
// holds until someone adds a provider.
const (
	maxButtonLabel = 32
	labelVerb      = "Listen on "
)

// TestEveryLinkableProviderNameFitsTheButtonLabel keeps provider names inside
// the room a button label has. It drives the same corpus as the route guard
// above, so a provider added to Find is measured in the same commit.
func TestEveryLinkableProviderNameFitsTheButtonLabel(t *testing.T) {
	for _, testCase := range linkablePaths {
		t.Run(testCase.name, func(t *testing.T) {
			link, ok := Find(testCase.path)
			if !ok {
				t.Fatalf("Find(%q) ok=false; want a link", testCase.path)
			}
			label := labelVerb + link.Provider
			if got := len([]rune(label)); got > maxButtonLabel {
				t.Fatalf("button label %q is %d characters; Discord caps a label at %d", label, got, maxButtonLabel)
			}
		})
	}
}
