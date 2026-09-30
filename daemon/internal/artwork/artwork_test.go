package artwork_test

// This file tests the Last.fm artwork resolver through its public API: which
// image in a response is chosen and how long it is reused, what a failure
// retries, and the guarantee that no returned error carries the API key.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/artwork"
)

// A response offering an http image and an https image resolves to the https
// one, and a second lookup for the same track is served from the cache rather
// than the server.
func TestLastFMArtworkResolutionAndCache(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Query().Get("artist") != "Artist" || r.URL.Query().Get("track") != "Track" {
			t.Errorf("unexpected query: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"track":{"album":{"image":[{"#text":"http://img/insecure.jpg"},{"#text":"https://img/large.jpg"}]}}}`))
	}))
	defer server.Close()

	resolver := artwork.NewLastFM("key", artwork.WithEndpoint(server.URL), artwork.WithHTTPClient(server.Client()))
	info, err := resolver.Resolve(context.Background(), "Artist", "Track")
	if err != nil || info.Image != "https://img/large.jpg" {
		t.Fatalf("Resolve() = %+v, %v", info, err)
	}
	info, err = resolver.Resolve(context.Background(), "Artist", "Track")
	if err != nil || info.Image != "https://img/large.jpg" || requests != 1 {
		t.Fatalf("cached Resolve() = %+v, %v; requests = %d", info, err, requests)
	}
}

// A resolver built with no API key returns no artwork and no error, so the
// daemon can run without Last.fm configured.
func TestLastFMArtworkDisabledWithoutKey(t *testing.T) {
	info, err := artwork.NewLastFM("").Resolve(context.Background(), "Artist", "Track")
	if err != nil || info.Image != "" {
		t.Fatalf("Resolve() = %+v, %v", info, err)
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return nil, errors.New(request.URL.String())
}

// The transport fails with the request URL as its error, and the error Resolve
// returns must not carry the URL, the query, or the API key it held.
func TestLastFMTransportErrorDoesNotExposeRequestURL(t *testing.T) {
	const secret = "secret-api-key"
	client := &http.Client{Transport: failingTransport{}}
	resolver := artwork.NewLastFM(secret, artwork.WithHTTPClient(client))

	_, err := resolver.Resolve(context.Background(), "Private Artist", "Private Track")
	if err == nil {
		t.Fatal("expected transport error")
	}
	for _, sensitive := range []string{secret, "Private+Artist", "Private+Track", "api_key", "https://"} {
		if strings.Contains(err.Error(), sensitive) {
			t.Fatalf("error exposes %q: %v", sensitive, err)
		}
	}
}

type countingFailureTransport struct {
	requests int
}

func (transport *countingFailureTransport) RoundTrip(*http.Request) (*http.Response, error) {
	transport.requests++
	return nil, errors.New("offline")
}

// A failed lookup is remembered for a retry window: the next Resolve for the
// same track makes no request and returns no artwork, and only after the clock
// passes the window does it ask again.
func TestLastFMFailuresHaveRetryBackoff(t *testing.T) {
	now := time.Unix(1000, 0)
	transport := &countingFailureTransport{}
	resolver := artwork.NewLastFM("key",
		artwork.WithHTTPClient(&http.Client{Transport: transport}),
		artwork.WithClock(func() time.Time { return now }),
	)

	if _, err := resolver.Resolve(context.Background(), "Artist", "Track"); err == nil {
		t.Fatal("expected initial error")
	}
	if info, err := resolver.Resolve(context.Background(), "Artist", "Track"); err != nil || info.Image != "" {
		t.Fatalf("backoff result = %+v, %v", info, err)
	}
	if transport.requests != 1 {
		t.Fatalf("requests during backoff = %d", transport.requests)
	}

	now = now.Add(time.Minute)
	if _, err := resolver.Resolve(context.Background(), "Artist", "Track"); err == nil {
		t.Fatal("expected retry error")
	}
	if transport.requests != 2 {
		t.Fatalf("requests after backoff = %d", transport.requests)
	}
}

// Validate against a server that answers a lookup reports success and sends
// track.getInfo with the configured key, which is what a working key looks like.
func TestLastFMValidateAcceptsAWorkingKey(t *testing.T) {
	var query url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		_, _ = w.Write([]byte(`{"track":{"album":{"image":[{"#text":"https://img/large.jpg"}]}}}`))
	}))
	defer server.Close()

	resolver := artwork.NewLastFM("good-key", artwork.WithEndpoint(server.URL), artwork.WithHTTPClient(server.Client()))
	if err := resolver.Validate(context.Background()); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
	if query.Get("method") != "track.getInfo" || query.Get("api_key") != "good-key" {
		t.Fatalf("Validate sent %q", query.Encode())
	}
}

// Last.fm answers a bad key with HTTP 200 and an error object rather than an
// HTTP failure, so the status line alone cannot distinguish it from a
// successful lookup. Validate has to read the body, and this proves the
// rejection is reported with Last.fm's code.
func TestLastFMValidateRejectsARejectedKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"error":10,"message":"Invalid API key - You must be granted a valid key by last.fm"}`))
	}))
	defer server.Close()

	resolver := artwork.NewLastFM("bad-key", artwork.WithEndpoint(server.URL), artwork.WithHTTPClient(server.Client()))
	err := resolver.Validate(context.Background())
	if err == nil {
		t.Fatal("Validate() accepted a rejected key")
	}
	if !strings.Contains(err.Error(), "10") {
		t.Fatalf("error omits Last.fm's code: %v", err)
	}
}

// A diagnostic has to answer from the API every time: serving a cached result,
// or going quiet inside a backoff window left by an earlier probe, would report
// the state of the cache rather than the state of the key. Two probes against a
// rejected key must therefore make two requests.
func TestLastFMValidateIgnoresTheArtworkCacheAndBackoff(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"error":10,"message":"Invalid API key"}`))
	}))
	defer server.Close()

	resolver := artwork.NewLastFM("bad-key", artwork.WithEndpoint(server.URL), artwork.WithHTTPClient(server.Client()))
	for attempt := 0; attempt < 2; attempt++ {
		if err := resolver.Validate(context.Background()); err == nil {
			t.Fatal("Validate() accepted a rejected key")
		}
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

// A transport error must not put the API key in the error Validate returns,
// since the daemon logs it.
func TestLastFMValidateDoesNotExposeTheKeyOnTransportError(t *testing.T) {
	const secret = "secret-api-key"
	resolver := artwork.NewLastFM(secret, artwork.WithHTTPClient(&http.Client{Transport: failingTransport{}}))

	err := resolver.Validate(context.Background())
	if err == nil {
		t.Fatal("expected transport error")
	}
	for _, sensitive := range []string{secret, "api_key", "https://"} {
		if strings.Contains(err.Error(), sensitive) {
			t.Fatalf("error exposes %q: %v", sensitive, err)
		}
	}
}

// The issue's repro: an endpoint the resolver cannot build a request from. The
// error is surfaced to the daemon's log, so it must not carry the credentials
// the URL was about to hold.
func TestLastFMResolveDoesNotExposeTheKeyOnAnUnbuildableRequest(t *testing.T) {
	const secret = "SUPERSECRETKEY123"
	resolver := artwork.NewLastFM(secret, artwork.WithEndpoint("://bad"))

	_, err := resolver.Resolve(context.Background(), "Private Artist", "Private Track")
	if err == nil {
		t.Fatal("expected an unbuildable endpoint to fail")
	}
	for _, sensitive := range []string{secret, "Private+Artist", "Private+Track", "api_key"} {
		if strings.Contains(err.Error(), sensitive) {
			t.Fatalf("error exposes %q: %v", sensitive, err)
		}
	}
}

// An empty response is not knowledge that a track has no artwork; it is a
// moment when Last.fm had none to give, so it is retried like a failure rather
// than held for the daemon's lifetime. Holding it that long made one transient
// miss permanent while a hard failure was retried after 30 seconds, the error
// path being the more forgiving of the two. Inside the retry window the empty
// answer stands, so a miss costs one request rather than one per track change.
func TestLastFMEmptyResultIsRetriedLikeAFailure(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			_, _ = w.Write([]byte(`{"track":{"album":{"image":[]}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"track":{"album":{"image":[{"#text":"https://img/large.jpg"}]}}}`))
	}))
	defer server.Close()

	now := time.Unix(1000, 0)
	resolver := artwork.NewLastFM("key",
		artwork.WithEndpoint(server.URL),
		artwork.WithHTTPClient(server.Client()),
		artwork.WithClock(func() time.Time { return now }),
	)

	if info, err := resolver.Resolve(context.Background(), "Artist", "Track"); err != nil || info.Image != "" {
		t.Fatalf("first Resolve() = %+v, %v", info, err)
	}
	if info, err := resolver.Resolve(context.Background(), "Artist", "Track"); err != nil || info.Image != "" {
		t.Fatalf("Resolve() inside the window = %+v, %v", info, err)
	}
	if requests != 1 {
		t.Fatalf("requests inside the window = %d, want 1", requests)
	}

	now = now.Add(time.Minute)
	info, err := resolver.Resolve(context.Background(), "Artist", "Track")
	if err != nil || info.Image != "https://img/large.jpg" {
		t.Fatalf("Resolve() after the window = %+v, %v; the empty result was never retried", info, err)
	}
}

// The two answers are not equally durable, and should not be: a URL is
// knowledge about the track, while an empty answer describes a moment. One
// expiry for both would re-fetch every track on every retry window, so a
// resolved URL must survive past the retry window without another request.
func TestLastFMResolvedArtworkOutlivesTheRetryWindow(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"track":{"album":{"image":[{"#text":"https://img/large.jpg"}]}}}`))
	}))
	defer server.Close()

	now := time.Unix(1000, 0)
	resolver := artwork.NewLastFM("key",
		artwork.WithEndpoint(server.URL),
		artwork.WithHTTPClient(server.Client()),
		artwork.WithClock(func() time.Time { return now }),
	)

	if _, err := resolver.Resolve(context.Background(), "Artist", "Track"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	info, err := resolver.Resolve(context.Background(), "Artist", "Track")
	if err != nil || info.Image != "https://img/large.jpg" {
		t.Fatalf("Resolve() = %+v, %v", info, err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1: resolved artwork was refetched inside its lifetime", requests)
	}
}

// The track and artist pages ride along with the artwork. Last.fm reports all
// three in the one track.getInfo response the resolver was already making, so
// reading the two pages costs no request the image did not already make.
func TestLastFMResolveReturnsTheTrackAndArtistPages(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"track":{"url":"https://www.last.fm/music/Artist/_/Track","artist":{"url":"https://www.last.fm/music/Artist"},"album":{"image":[{"#text":"https://img/large.jpg"}]}}}`))
	}))
	defer server.Close()

	resolver := artwork.NewLastFM("key", artwork.WithEndpoint(server.URL), artwork.WithHTTPClient(server.Client()))
	info, err := resolver.Resolve(context.Background(), "Artist", "Track")
	if err != nil {
		t.Fatal(err)
	}
	if info.Image != "https://img/large.jpg" {
		t.Errorf("Image = %q; want the artwork", info.Image)
	}
	if info.TrackURL != "https://www.last.fm/music/Artist/_/Track" {
		t.Errorf("TrackURL = %q; want the track page", info.TrackURL)
	}
	if info.ArtistURL != "https://www.last.fm/music/Artist" {
		t.Errorf("ArtistURL = %q; want the artist page", info.ArtistURL)
	}
	if requests != 1 {
		t.Errorf("requests = %d, want 1: the pages must cost no extra request", requests)
	}
}

// The cache entry carries the pages, not just the image. Serving them on a miss
// alone would show the links on a track's first play and then drop them for the
// rest of the hour — invisible in a short manual test, which is what makes this
// the subtlest way for the change to be wrong. The second lookup here is served
// from the cache and must still report the pages.
func TestLastFMResolveServesThePagesFromTheCache(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"track":{"url":"https://www.last.fm/music/Artist/_/Track","artist":{"url":"https://www.last.fm/music/Artist"},"album":{"image":[{"#text":"https://img/large.jpg"}]}}}`))
	}))
	defer server.Close()

	resolver := artwork.NewLastFM("key", artwork.WithEndpoint(server.URL), artwork.WithHTTPClient(server.Client()))
	if _, err := resolver.Resolve(context.Background(), "Artist", "Track"); err != nil {
		t.Fatal(err)
	}
	info, err := resolver.Resolve(context.Background(), "Artist", "Track")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1: the second lookup should have been cached", requests)
	}
	if info.TrackURL != "https://www.last.fm/music/Artist/_/Track" {
		t.Errorf("cached TrackURL = %q; want the track page", info.TrackURL)
	}
	if info.ArtistURL != "https://www.last.fm/music/Artist" {
		t.Errorf("cached ArtistURL = %q; want the artist page", info.ArtistURL)
	}
}

// A page the resolver cannot vouch for is dropped and the ones it can are kept.
// Discord rejects the entire activity when one field is a malformed URL, so a
// bad page must not be able to cost the card its image or its other link.
//
// The two cases are the discriminator, not a check that nothing is returned: an
// http page and a page that is not a URL at all must each be dropped while the
// valid page beside them survives. Asserting only that bad values are absent
// would also pass if the resolver returned no pages at all.
func TestLastFMResolveKeepsAPageItCanVouchForAndDropsTheOthers(t *testing.T) {
	cases := []struct {
		name       string
		track      string
		artist     string
		wantTrack  string
		wantArtist string
	}{
		{
			name:       "an http track page is dropped while the artist page survives",
			track:      "http://www.last.fm/music/Artist/_/Track",
			artist:     "https://www.last.fm/music/Artist",
			wantTrack:  "",
			wantArtist: "https://www.last.fm/music/Artist",
		},
		{
			name:       "a malformed artist page is dropped while the track page survives",
			track:      "https://www.last.fm/music/Artist/_/Track",
			artist:     "not a url",
			wantTrack:  "https://www.last.fm/music/Artist/_/Track",
			wantArtist: "",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"track":{"url":"` + testCase.track +
					`","artist":{"url":"` + testCase.artist +
					`"},"album":{"image":[{"#text":"https://img/large.jpg"}]}}}`))
			}))
			defer server.Close()

			resolver := artwork.NewLastFM("key", artwork.WithEndpoint(server.URL), artwork.WithHTTPClient(server.Client()))
			info, err := resolver.Resolve(context.Background(), "Artist", "Track")
			if err != nil {
				t.Fatal(err)
			}
			if info.Image != "https://img/large.jpg" {
				t.Errorf("Image = %q; the artwork must survive a rejected page", info.Image)
			}
			if info.TrackURL != testCase.wantTrack {
				t.Errorf("TrackURL = %q; want %q", info.TrackURL, testCase.wantTrack)
			}
			if info.ArtistURL != testCase.wantArtist {
				t.Errorf("ArtistURL = %q; want %q", info.ArtistURL, testCase.wantArtist)
			}
		})
	}
}
