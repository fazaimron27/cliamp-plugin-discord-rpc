package artwork_test

// This file tests the merge that decides which source supplies each field of a
// track's artwork. The rule under test is that the image is the first source
// with one, while the track and artist pages always come from Last.fm: a
// source that wins the image must not take the pages with it.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/artwork"
)

// lastFMReturning serves a track.getInfo response carrying the given body, so a
// test can choose which of the three fields Last.fm supplies.
func lastFMReturning(t *testing.T, body string) *artwork.LastFM {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return artwork.NewLastFM("key", artwork.WithEndpoint(server.URL), artwork.WithHTTPClient(server.Client()))
}

// countingSource is a Source that answers with a fixed TrackInfo and records
// how often it was asked, which is how a short-circuit is made visible.
type countingSource struct {
	info  artwork.TrackInfo
	err   error
	calls int
}

func (c *countingSource) Resolve(context.Context, artwork.Request) (artwork.TrackInfo, error) {
	c.calls++
	return c.info, c.err
}

// TestResolverAlwaysTakesThePagesFromLastFM pins the merge's whole reason for
// existing. The pages exist in no other source, so they must reach the answer
// whatever supplied the image.
func TestResolverAlwaysTakesThePagesFromLastFM(t *testing.T) {
	resolver := artwork.Resolver{
		LastFM: lastFMReturning(t, `{"track":{"url":"https://www.last.fm/music/A/_/T","artist":{"url":"https://www.last.fm/music/A"}}}`),
	}
	info, err := resolver.Resolve(context.Background(), artwork.Request{Path: "spotify:track:abc", Artist: "A", Title: "T"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if info.TrackURL != "https://www.last.fm/music/A/_/T" {
		t.Errorf("TrackURL = %q; want the Last.fm page", info.TrackURL)
	}
	if info.ArtistURL != "https://www.last.fm/music/A" {
		t.Errorf("ArtistURL = %q; want the Last.fm artist page", info.ArtistURL)
	}
}

// TestResolverWithoutASourceIsEmpty keeps the seam honest: a resolver with
// nothing behind it answers with nothing rather than failing, which is what a
// daemon running with no API key relies on.
func TestResolverWithoutASourceIsEmpty(t *testing.T) {
	resolver := artwork.Resolver{LastFM: artwork.NewLastFM("")}
	info, err := resolver.Resolve(context.Background(), artwork.Request{Path: "/music/song.flac", Artist: "A", Title: "T"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if info != (artwork.TrackInfo{}) {
		t.Errorf("Resolve() = %+v; want an empty answer", info)
	}
}

// TestResolverPrefersTheDerivedThumbnail covers the derived tier: a video path
// supplies the image, and it outranks the image Last.fm reported.
func TestResolverPrefersTheDerivedThumbnail(t *testing.T) {
	resolver := artwork.Resolver{
		Derived: func(path string) (string, bool) {
			if path != "https://www.youtube.com/watch?v=dQw4w9WgXcQ" {
				return "", false
			}
			return "https://i.ytimg.com/vi/dQw4w9WgXcQ/mqdefault.jpg", true
		},
		LastFM: lastFMReturning(t, `{"track":{"url":"https://www.last.fm/music/A/_/T","album":{"image":[{"#text":"https://img/lastfm.jpg"}]}}}`),
	}
	info, err := resolver.Resolve(context.Background(), artwork.Request{
		Path:   "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		Artist: "A",
		Title:  "T",
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if info.Image != "https://i.ytimg.com/vi/dQw4w9WgXcQ/mqdefault.jpg" {
		t.Errorf("Image = %q; want the derived thumbnail", info.Image)
	}
}

// TestResolverDerivedTierDoesNotMoveTheLinks pins the thing that must not change
// when a track gains a thumbnail: the pages are the same ones it would have
// had with no derived source at all.
func TestResolverDerivedTierDoesNotMoveTheLinks(t *testing.T) {
	body := `{"track":{"url":"https://www.last.fm/music/A/_/T","artist":{"url":"https://www.last.fm/music/A"},"album":{"image":[{"#text":"https://img/lastfm.jpg"}]}}}`
	request := artwork.Request{Path: "https://www.youtube.com/watch?v=dQw4w9WgXcQ", Artist: "A", Title: "T"}

	without, err := artwork.Resolver{LastFM: lastFMReturning(t, body)}.Resolve(context.Background(), request)
	if err != nil {
		t.Fatalf("Resolve() without a derived source error = %v", err)
	}
	with, err := artwork.Resolver{
		Derived: func(string) (string, bool) {
			return "https://i.ytimg.com/vi/dQw4w9WgXcQ/mqdefault.jpg", true
		},
		LastFM: lastFMReturning(t, body),
	}.Resolve(context.Background(), request)
	if err != nil {
		t.Fatalf("Resolve() with a derived source error = %v", err)
	}
	if with.TrackURL != without.TrackURL || with.ArtistURL != without.ArtistURL {
		t.Errorf("the derived tier moved a link: %+v vs %+v", with, without)
	}
	if with.Image == without.Image {
		t.Errorf("the derived tier changed nothing; Image = %q in both", with.Image)
	}
}

// TestResolverDerivedTierShortCircuitsThePlayer pins the cost rule: a path the
// derived source answers must not also cost a round trip.
func TestResolverDerivedTierShortCircuitsThePlayer(t *testing.T) {
	player := &countingSource{info: artwork.TrackInfo{Image: "https://i.scdn.co/image/x"}}
	resolver := artwork.Resolver{
		Derived: func(string) (string, bool) {
			return "https://i.ytimg.com/vi/dQw4w9WgXcQ/mqdefault.jpg", true
		},
		Player: player,
		LastFM: artwork.NewLastFM(""),
	}
	if _, err := resolver.Resolve(context.Background(), artwork.Request{Path: "https://www.youtube.com/watch?v=dQw4w9WgXcQ"}); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if player.calls != 0 {
		t.Errorf("the player was asked %d times after the derived source answered; want 0", player.calls)
	}
}

// TestResolverPlayerImageDoesNotShadowTheLinks is the regression this whole design
// exists to prevent. A whole-source precedence would let the player's image
// carry the answer with it and drop both Last.fm pages, which costs Spotify and
// Mixcloud their exact track and artist links the moment they gained artwork.
//
// It also carries the spec's third named test — that source 3 always runs, even
// when an image came from elsewhere. The two asserted URLs exist nowhere but in
// the stub server's body, so a resolvable TrackURL is itself the proof that the
// Last.fm request was issued; there is no second thing to assert.
func TestResolverPlayerImageDoesNotShadowTheLinks(t *testing.T) {
	resolver := artwork.Resolver{
		Player: &countingSource{info: artwork.TrackInfo{Image: "https://i.scdn.co/image/x"}},
		LastFM: lastFMReturning(t, `{"track":{"url":"https://www.last.fm/music/A/_/T","artist":{"url":"https://www.last.fm/music/A"}}}`),
	}
	info, err := resolver.Resolve(context.Background(), artwork.Request{Path: "spotify:track:abc", Artist: "A", Title: "T"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if info.Image != "https://i.scdn.co/image/x" {
		t.Errorf("Image = %q; want the player's artwork", info.Image)
	}
	if info.TrackURL != "https://www.last.fm/music/A/_/T" {
		t.Errorf("TrackURL = %q; the player's image shadowed the Last.fm page", info.TrackURL)
	}
	if info.ArtistURL != "https://www.last.fm/music/A" {
		t.Errorf("ArtistURL = %q; the player's image shadowed the Last.fm artist page", info.ArtistURL)
	}
}

// TestResolverPlayerImageSurvivesALastFMFailure pins the merge under failure. The
// artwork does not depend on Last.fm, so a transient Last.fm error must cost
// the links rather than the image. The consequence is deliberate and is worth
// knowing: the daemon publishes what it was given, so artworkInfo.Image is no
// longer empty and the fifteen-second retry that used to recover the links
// stops until the track changes.
func TestResolverPlayerImageSurvivesALastFMFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	lastfm := artwork.NewLastFM("key", artwork.WithEndpoint(server.URL), artwork.WithHTTPClient(server.Client()))

	resolver := artwork.Resolver{
		Player: &countingSource{info: artwork.TrackInfo{Image: "https://i.scdn.co/image/x"}},
		LastFM: lastfm,
	}
	info, err := resolver.Resolve(context.Background(), artwork.Request{Path: "spotify:track:abc", Artist: "A", Title: "T"})
	if err == nil {
		t.Fatal("Resolve() error = nil; want the Last.fm failure reported")
	}
	if info.Image != "https://i.scdn.co/image/x" {
		t.Errorf("Image = %q; want the player's artwork despite the Last.fm failure", info.Image)
	}
}

// TestResolverIsUngatedByTheAPIKey pins the shape of the keyless card. Neither new
// tier consults the key, so a daemon with none still gains both.
func TestResolverIsUngatedByTheAPIKey(t *testing.T) {
	request := artwork.Request{Path: "spotify:track:abc", Artist: "A", Title: "T"}

	derived := artwork.Resolver{
		Derived: func(string) (string, bool) {
			return "https://i.ytimg.com/vi/dQw4w9WgXcQ/mqdefault.jpg", true
		},
		LastFM: artwork.NewLastFM(""),
	}
	info, err := derived.Resolve(context.Background(), request)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if info.Image == "" {
		t.Error("the derived tier did not fire without an API key")
	}

	player := artwork.Resolver{
		Player: &countingSource{info: artwork.TrackInfo{Image: "https://i.scdn.co/image/x"}},
		LastFM: artwork.NewLastFM(""),
	}
	info, err = player.Resolve(context.Background(), request)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if info.Image == "" {
		t.Error("the player tier did not fire without an API key")
	}
	if info.TrackURL != "" || info.ArtistURL != "" {
		t.Errorf("a keyless resolver produced links: %+v", info)
	}
}
