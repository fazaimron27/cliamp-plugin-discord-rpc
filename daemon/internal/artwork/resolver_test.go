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
