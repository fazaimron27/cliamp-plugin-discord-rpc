package tests

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
	image, err := resolver.Resolve(context.Background(), "Artist", "Track")
	if err != nil || image != "https://img/large.jpg" {
		t.Fatalf("Resolve() = %q, %v", image, err)
	}
	image, err = resolver.Resolve(context.Background(), "Artist", "Track")
	if err != nil || image != "https://img/large.jpg" || requests != 1 {
		t.Fatalf("cached Resolve() = %q, %v; requests = %d", image, err, requests)
	}
}

func TestLastFMArtworkDisabledWithoutKey(t *testing.T) {
	image, err := artwork.NewLastFM("").Resolve(context.Background(), "Artist", "Track")
	if err != nil || image != "" {
		t.Fatalf("Resolve() = %q, %v", image, err)
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return nil, errors.New(request.URL.String())
}

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
	if image, err := resolver.Resolve(context.Background(), "Artist", "Track"); err != nil || image != "" {
		t.Fatalf("backoff result = %q, %v", image, err)
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

func TestLastFMValidateRejectsARejectedKey(t *testing.T) {
	// Last.fm answers a bad key with HTTP 200 and an error object, so the
	// status line alone cannot distinguish it from a successful lookup.
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

func TestLastFMValidateIgnoresTheArtworkCacheAndBackoff(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"error":10,"message":"Invalid API key"}`))
	}))
	defer server.Close()

	resolver := artwork.NewLastFM("bad-key", artwork.WithEndpoint(server.URL), artwork.WithHTTPClient(server.Client()))
	// A diagnostic has to answer from the API every time. Serving a cached
	// result, or going quiet inside a backoff window left by an earlier probe,
	// would report the state of the cache rather than the state of the key.
	for attempt := 0; attempt < 2; attempt++ {
		if err := resolver.Validate(context.Background()); err == nil {
			t.Fatal("Validate() accepted a rejected key")
		}
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

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
