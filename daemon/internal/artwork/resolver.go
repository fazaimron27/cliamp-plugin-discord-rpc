package artwork

// This file composes the sources a track's artwork can come from. The merge is
// per field rather than per source, and that is the whole reason the type
// exists: the image is taken from the first source that has one, while the
// track and artist pages always come from Last.fm, because it is the only
// source of them. A source that returned early with its own TrackInfo would
// silently drop both pages, which costs Spotify, Mixcloud and the podcast
// providers their exact track and artist links the moment they gain an image.

import "context"

// Request identifies the track whose artwork is wanted. It carries the path
// because two of the sources key on it, and the names because Last.fm can only
// be asked by name.
type Request struct {
	Path   string
	Artist string
	Title  string
}

// Source yields the artwork one origin can supply for a track.
type Source interface {
	Resolve(context.Context, Request) (TrackInfo, error)
}

// Resolver merges the artwork sources for one track: the image from the first
// of Derived, Player and LastFM that has one, and the pages always from LastFM.
type Resolver struct {
	// Derived is the path-derived source, a pure function of the playback path
	// with no I/O. It is consulted before the player, whose answer costs a round
	// trip; Last.fm is still asked first, because its answer is the only source
	// of the pages. A nil value contributes nothing, which is what an
	// unconfigured daemon wants.
	Derived func(string) (string, bool)
	// Player is the artwork the player reports for the track itself. Its answer
	// costs a round trip, so Derived short-circuits it. A nil value contributes
	// nothing.
	Player Source
	// LastFM supplies the track and artist pages, and an image when neither of
	// the others has one.
	LastFM *LastFM
}

// Resolve merges the sources for one track, reporting the merged answer through
// report as it becomes known rather than only once it is complete.
//
// report is called once, and twice when the playback path answers: a path the
// derived source can read is reported before Last.fm is asked, and reported
// again when the pages arrive. That first report is the whole reason the seam
// exists. The thumbnail is a parse of the path and costs no request, while the
// paths that supply one are exactly the paths whose card renders the provider
// link in place of both Last.fm pages, so waiting for Last.fm would hold a free
// answer behind a request whose response that card does not use.
//
// Every call carries a complete merged answer rather than one source's own
// contribution, so the per-field precedence above holds for each of them and no
// report can shadow the pages. A path the derived source does not answer is
// reported once, at the end, because there is nothing to publish before then.
// The player stays behind Last.fm for the same reason: it is consulted for
// every path, and a socket nobody is listening on costs its timeout, so moving
// it first would charge that timeout to every track to save a round trip on the
// ones that get an image.
//
// Last.fm's error is reported alongside the merged answer rather than instead
// of it, because the two are about different things: the pages depend on
// Last.fm and the image does not, so a transient failure costs the links rather
// than the artwork. The caller reports the error and publishes what it was
// given.
func (r Resolver) Resolve(ctx context.Context, request Request, report func(TrackInfo, error)) {
	if image, ok := r.derived(request.Path); ok {
		report(TrackInfo{Image: image}, nil)
		lastfm, err := r.LastFM.Resolve(ctx, request.Artist, request.Title)
		report(TrackInfo{Image: image, TrackURL: lastfm.TrackURL, ArtistURL: lastfm.ArtistURL}, err)
		return
	}

	lastfm, err := r.LastFM.Resolve(ctx, request.Artist, request.Title)
	merged := TrackInfo{Image: lastfm.Image, TrackURL: lastfm.TrackURL, ArtistURL: lastfm.ArtistURL}

	if image := r.playerImage(ctx, request); image != "" {
		merged.Image = image
	}
	report(merged, err)
}

// derived reports the thumbnail the playback path identifies, if any.
func (r Resolver) derived(path string) (string, bool) {
	if r.Derived == nil {
		return "", false
	}
	return r.Derived(path)
}

// playerImage asks the player for its own artwork, treating a failure as
// absence. A build that predates state.get, a socket nobody is listening on and
// a refused URL are all the same state of knowledge — the player supplied
// nothing usable — and none of them is worth a log line on every track change,
// which is what a pre-v2.0.0 build would produce.
func (r Resolver) playerImage(ctx context.Context, request Request) string {
	if r.Player == nil {
		return ""
	}
	info, err := r.Player.Resolve(ctx, request)
	if err != nil {
		return ""
	}
	return info.Image
}
