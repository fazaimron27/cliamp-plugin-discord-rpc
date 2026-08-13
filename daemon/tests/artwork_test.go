package tests

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

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
