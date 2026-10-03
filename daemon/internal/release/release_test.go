package release_test

// This file tests the newest-release lookup through its public API: what it
// reads out of GitHub's redirect, which answers it refuses, and what the request
// it makes actually is.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/release"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/version"
)

// serveRelease stands up a server that answers every request with one status and
// one Location, and returns a Checker pointed at it. The whole lookup is one
// request, so a handler with no routing in it is the entire fake.
func serveRelease(t *testing.T, status int, location string) release.Checker {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if location != "" {
			w.Header().Set("Location", location)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	return release.New(release.WithBaseURL(server.URL))
}

// The answer is in the redirect, not in a body: /releases/latest replies with a
// Location naming the release's tag, so the tag is the last path segment of a URL
// that is not this program's own.
func TestReleaseLatestReadsTheRedirectTarget(t *testing.T) {
	checker := serveRelease(t, http.StatusFound,
		"https://github.com/fazaimron27/cliamp-plugin-discord-rpc/releases/tag/v1.12.0")

	tag, err := checker.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if tag != "v1.12.0" {
		t.Fatalf("Latest = %q, want %q", tag, "v1.12.0")
	}
}

// The exclusion of drafts and prereleases is GitHub's, not this project's, so the
// path is the assertion rather than a filter that could later be dropped from a
// list here. The user agent is asserted too: it is the one string in the request
// that names this program, and it belongs to the version package rather than
// being written out again in this one.
func TestReleaseLatestAsksForTheNewestReleaseOnly(t *testing.T) {
	var path, agent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, agent = r.URL.Path, r.Header.Get("User-Agent")
		w.Header().Set("Location", "https://github.com/fazaimron27/cliamp-plugin-discord-rpc/releases/tag/v1.12.0")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()

	if _, err := release.New(release.WithBaseURL(server.URL)).Latest(context.Background()); err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if path != "/releases/latest" {
		t.Errorf("request path = %q, want %q", path, "/releases/latest")
	}
	if agent != version.UserAgent {
		t.Errorf("User-Agent = %q, want %q", agent, version.UserAgent)
	}
}

// Every answer that does not name a release tag is an error rather than a
// smaller-than-nothing. Reporting "no newer release" for a GitHub that answered
// something else would let --check print a state it never established.
func TestReleaseLatestRefusesAnswersItCannotOrder(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		location string
	}{
		{"no redirect at all", http.StatusOK, ""},
		{"a missing release", http.StatusNotFound, ""},
		{"a redirect that is not a release", http.StatusFound, "https://github.com/fazaimron27"},
		{"a redirect to a release with no tag", http.StatusFound, "https://github.com/fazaimron27/cliamp-plugin-discord-rpc/releases"},
		{"a tag that is not a version", http.StatusFound, "https://github.com/fazaimron27/cliamp-plugin-discord-rpc/releases/tag/nightly"},
		{"a tag with a build suffix", http.StatusFound, "https://github.com/fazaimron27/cliamp-plugin-discord-rpc/releases/tag/v1.12.0-rc1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tag, err := serveRelease(t, test.status, test.location).Latest(context.Background())
			if err == nil {
				t.Fatalf("Latest = %q, want an error", tag)
			}
		})
	}
}

// The timeout is what keeps a GitHub that accepts the connection and then says
// nothing from holding the goroutine open. The client carries it, so the test
// drives it with a duration rather than waiting out the real one.
func TestReleaseLatestGivesUpOnAGitHubThatNeverAnswers(t *testing.T) {
	blocked := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blocked
	}))
	defer server.Close()
	defer close(blocked)

	checker := release.New(
		release.WithBaseURL(server.URL),
		release.WithTimeout(20*time.Millisecond),
	)
	if tag, err := checker.Latest(context.Background()); err == nil {
		t.Fatalf("Latest = %q, want an error", tag)
	}
}

// atomFeed is the shape GitHub's releases.atom serves. The namespace is in the
// fixture deliberately: a parser that only works without one would pass against
// a simplified document and fail against the real feed.
const atomFeed = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Release notes</title>
  <entry>
    <link rel="alternate" type="text/html" href="https://github.com/fazaimron27/cliamp-plugin-discord-rpc/releases/tag/v1.12.0"/>
  </entry>
  <entry>
    <link rel="alternate" type="text/html" href="https://github.com/fazaimron27/cliamp-plugin-discord-rpc/releases/tag/v1.12.0-rc1"/>
  </entry>
  <entry>
    <link rel="alternate" type="text/html" href="https://github.com/fazaimron27/cliamp-plugin-discord-rpc/releases/tag/nightly"/>
  </entry>
  <entry>
    <link rel="alternate" type="text/html" href="https://github.com/fazaimron27/cliamp-plugin-discord-rpc/releases/tag/v1.11.0"/>
  </entry>
</feed>`

// serveFeed stands up a server answering one body at every path, and returns a
// Checker pointed at it.
func serveFeed(t *testing.T, status int, body string) release.Checker {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return release.New(release.WithBaseURL(server.URL))
}

// A draft or a prerelease is not something to install, so a tag that cannot be
// ordered is dropped here rather than offered to a caller that would have to
// guess. The order is the feed's, which GitHub publishes newest first.
func TestReleaseTagsReadsTheFeedAndDropsWhatItCannotOrder(t *testing.T) {
	checker := serveFeed(t, http.StatusOK, atomFeed)

	tags, err := checker.Tags(context.Background())
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	want := []string{"v1.12.0", "v1.11.0"}
	if len(tags) != len(want) {
		t.Fatalf("Tags = %v, want %v", tags, want)
	}
	for index := range want {
		if tags[index] != want[index] {
			t.Fatalf("Tags = %v, want %v", tags, want)
		}
	}
}

// The path is the assertion, not a filter that could later be dropped from a
// list here: /releases.atom is the endpoint the API's sixty-per-hour-per-IP
// limit does not apply to.
func TestReleaseTagsAsksForTheAtomFeed(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		fmt.Fprint(w, atomFeed)
	}))
	defer server.Close()

	if _, err := release.New(release.WithBaseURL(server.URL)).Tags(context.Background()); err != nil {
		t.Fatalf("Tags: %v", err)
	}
	if path != "/releases.atom" {
		t.Errorf("request path = %q, want %q", path, "/releases.atom")
	}
}

// A redirect is not an answer here, the way it is for Latest. With the no-follow
// client it would arrive as an empty body, which parses to zero entries, and a
// rollback would report that no older release exists rather than following the
// feed it was pointed at.
func TestReleaseTagsFollowsARedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/releases.atom" {
			http.Redirect(w, r, "/feed", http.StatusMovedPermanently)
			return
		}
		fmt.Fprint(w, atomFeed)
	}))
	defer server.Close()

	tags, err := release.New(release.WithBaseURL(server.URL)).Tags(context.Background())
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	if len(tags) != 2 {
		t.Fatalf("Tags = %v, want the two released tags", tags)
	}
}

// A document that is not the feed must not read as "there is no older release":
// the caller would then tell a user to name a tag by hand for a GitHub that was
// merely answering something else. A feed that parses but holds nothing usable is
// not this case, and the last one below covers it.
func TestReleaseTagsRefusesWhatIsNotAFeed(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"a missing feed", http.StatusNotFound, ""},
		{"an interstitial", http.StatusOK, "<html><body>rate limited</body></html>"},
		{"an empty feed", http.StatusOK, `<feed xmlns="http://www.w3.org/2005/Atom"></feed>`},
		{"entries naming no tag", http.StatusOK, `<feed xmlns="http://www.w3.org/2005/Atom"><entry><link href="https://github.com/fazaimron27/cliamp-plugin-discord-rpc/releases"/></entry></feed>`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tags, err := serveFeed(t, test.status, test.body).Tags(context.Background())
			if err == nil {
				t.Fatalf("Tags = %v, want an error", tags)
			}
		})
	}
}

// A feed holding only prereleases is a real answer — there are no releases to
// step to — so it is an empty list rather than an error, and the caller words it.
func TestReleaseTagsReturnsNothingUsableAsAnEmptyList(t *testing.T) {
	feed := `<feed xmlns="http://www.w3.org/2005/Atom"><entry><link href="https://github.com/fazaimron27/cliamp-plugin-discord-rpc/releases/tag/v1.12.0-rc1"/></entry></feed>`

	tags, err := serveFeed(t, http.StatusOK, feed).Tags(context.Background())
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	if len(tags) != 0 {
		t.Fatalf("Tags = %v, want none", tags)
	}
}
