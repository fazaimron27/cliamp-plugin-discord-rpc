package artwork

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestLastFMForgetsExpiredLookupsInsteadOfGrowingForever asserts on what the
// resolver retains, which is why it is written inside the package rather than
// in the external test package beside it: unbounded growth is a property of the
// memory held, and nothing in the public API reports that. Before this was
// guarded, both lookup maps were written for the daemon's lifetime and never
// pruned, so a track that failed once held an entry until the process exited.
func TestLastFMForgetsExpiredLookupsInsteadOfGrowingForever(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"track":{"album":{"image":[{"#text":"https://img/large.jpg"}]}}}`))
	}))
	defer server.Close()

	now := time.Unix(1000, 0)
	resolver := NewLastFM("key",
		WithEndpoint(server.URL),
		WithHTTPClient(server.Client()),
		WithClock(func() time.Time { return now }),
	)

	if _, err := resolver.Resolve(context.Background(), "First", "Track"); err != nil {
		t.Fatal(err)
	}
	if held := len(resolver.known); held != 1 {
		t.Fatalf("lookups held = %d, want 1", held)
	}

	// The next day: past any expiry the resolver applies, so the first track can
	// no longer be worth holding. The second lookup is what has to notice.
	now = now.Add(24 * time.Hour)
	if _, err := resolver.Resolve(context.Background(), "Second", "Track"); err != nil {
		t.Fatal(err)
	}
	if held := len(resolver.known); held != 1 {
		t.Fatalf("lookups held = %d, want 1: an expired entry was never dropped", held)
	}
}
