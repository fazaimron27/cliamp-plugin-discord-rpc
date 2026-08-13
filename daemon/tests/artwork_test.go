package tests

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
