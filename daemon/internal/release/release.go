// Package release asks GitHub which release of this project is the newest one.
package release

// This file is the lookup alone: one request, one value. It holds no policy about
// what the answer means — whether the daemon asking is behind, and what to tell
// the user about it, is the daemon package's business, because only a running
// daemon knows which half it is.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/version"
)

// defaultBaseURL is the repository this project releases from.
const defaultBaseURL = "https://github.com/fazaimron27/cliamp-plugin-discord-rpc"

// latestPath is GitHub's own "newest release" endpoint. It answers with a redirect
// to the release's tag URL rather than with a body, and it excludes drafts and
// prereleases by GitHub's definition rather than by a filter here.
const latestPath = "/releases/latest"

// tagMarker is the part of the redirect target that precedes the tag.
const tagMarker = "/releases/tag/"

// defaultTimeout bounds one lookup. It is the same four seconds the artwork
// request uses: both are a single HTTP round trip on a path the daemon can do
// without, and neither is worth holding a goroutine open for longer.
const defaultTimeout = 4 * time.Second

// Checker asks GitHub for the newest released tag.
type Checker struct {
	baseURL string
	client  *http.Client
}

// Option customizes a Checker. The defaults are this project's repository and a
// four-second timeout; the options exist so a test can point the lookup at a
// server it controls and give up on it quickly.
type Option func(*Checker)

// WithBaseURL overrides the repository the lookup is made against, and is the seam
// every test in this package uses. A trailing slash is dropped so the path below
// does not double it.
func WithBaseURL(baseURL string) Option {
	return func(checker *Checker) { checker.baseURL = strings.TrimSuffix(baseURL, "/") }
}

// WithTimeout overrides how long one lookup may take.
func WithTimeout(timeout time.Duration) Option {
	return func(checker *Checker) { checker.client.Timeout = timeout }
}

// New returns a Checker for this project's releases.
func New(options ...Option) Checker {
	checker := Checker{baseURL: defaultBaseURL}
	checker.client = &http.Client{
		Timeout:       defaultTimeout,
		CheckRedirect: noFollow,
	}
	for _, option := range options {
		option(&checker)
	}
	return checker
}

// noFollow stops the client at the redirect, which is where the answer is:
// /releases/latest replies with a Location naming the tag and no body, so
// following it would fetch a page nobody reads. The redirect is the mechanism
// rather than an incidental detail of it, so it is not something a caller can
// configure away.
func noFollow(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

// Latest returns the newest released tag, such as "v1.12.0".
//
// Only a real release counts: /releases/latest is GitHub's newest non-draft,
// non-prerelease release, so a project that publishes a prerelease does not send
// its users to it.
//
// The errors deliberately do not wrap what they came from, following the artwork
// lookup: the transport error embeds the request URL, and this program logs its
// errors, so wrapping would put a URL in the journal for a failure the URL does
// not explain.
func (c Checker) Latest(ctx context.Context) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+latestPath, nil)
	if err != nil {
		return "", errors.New("build release request")
	}
	request.Header.Set("User-Agent", version.UserAgent)
	response, err := c.client.Do(request)
	if err != nil {
		return "", errors.New("release lookup failed")
	}
	defer response.Body.Close()

	tag, ok := releaseTag(response.Header.Get("Location"))
	if !ok {
		return "", fmt.Errorf("release lookup returned HTTP %s, which names no release tag", response.Status)
	}
	return tag, nil
}

// releaseTag reads the tag out of the URL /releases/latest redirects to,
// reporting false when the Location names no release tag or does not hold a
// version.
//
// The status code is deliberately not consulted. What makes an answer usable is
// that it points at a release tag this program can order, and a check on the code
// as well would be a second condition to keep in step with a redirect that GitHub
// is free to render as 301 or 302. A response carrying no such Location — a 404,
// or an interstitial — fails here, which is the whole point: "I could not tell"
// must not be reported as "there is nothing newer".
func releaseTag(location string) (string, bool) {
	index := strings.LastIndex(location, tagMarker)
	if index < 0 {
		return "", false
	}
	tag := strings.Trim(location[index+len(tagMarker):], "/")
	if !version.IsVersion(tag) {
		return "", false
	}
	return tag, true
}
