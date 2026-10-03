// Package release asks GitHub which release of this project is the newest one.
package release

// This file is the lookup alone: one request, one value. It holds no policy about
// what the answer means — whether the daemon asking is behind, and what to tell
// the user about it, is the daemon package's business, because only a running
// daemon knows which half it is.

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/version"
)

// Repository is the GitHub repository this project releases from.
const Repository = "fazaimron27/cliamp-plugin-discord-rpc"

// defaultBaseURL is the repository's own site, which is where the newest-release
// redirect is asked for.
const defaultBaseURL = "https://github.com/" + Repository

// RawBase is the raw host a release's own files are fetched from. The trailing
// slash is part of it: every use appends a tag and a file name.
const RawBase = "https://raw.githubusercontent.com/" + Repository + "/"

// latestPath is GitHub's own "newest release" endpoint. It answers with a redirect
// to the release's tag URL rather than with a body, and it excludes drafts and
// prereleases by GitHub's definition rather than by a filter here.
const latestPath = "/releases/latest"

// feedPath is GitHub's Atom feed of releases. It is the same information
// /releases/latest carries, for every release rather than the newest one, and it
// is not subject to the API's per-IP rate limit.
const feedPath = "/releases.atom"

// tagMarker is the part of the redirect target that precedes the tag.
const tagMarker = "/releases/tag/"

// defaultTimeout bounds one lookup. It is the same four seconds the artwork
// request uses: both are a single HTTP round trip on a path the daemon can do
// without, and neither is worth holding a goroutine open for longer.
const defaultTimeout = 4 * time.Second

// Checker asks GitHub for this project's releases and for a release's own files.
type Checker struct {
	baseURL string
	rawURL  string
	client  *http.Client
	follow  *http.Client
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

// WithRawURL overrides the raw host a release's files are fetched from, and is
// the seam the fetch tests use. A trailing slash is added when it is missing, so
// a caller may pass either spelling.
func WithRawURL(rawURL string) Option {
	return func(checker *Checker) {
		checker.rawURL = strings.TrimSuffix(rawURL, "/") + "/"
	}
}

// WithTimeout overrides how long one lookup may take, on both clients.
func WithTimeout(timeout time.Duration) Option {
	return func(checker *Checker) {
		checker.client.Timeout = timeout
		checker.follow.Timeout = timeout
	}
}

// New returns a Checker for this project's releases.
//
// The two clients differ in one respect, and it is deliberate. The redirect
// /releases/latest answers with is the answer, so that client stops at it. The
// feed and a release's files are bodies, where a redirect is a way of naming the
// real URL rather than a result: a client that stopped there would hand back an
// empty body, which for a script means running nothing and reporting success.
func New(options ...Option) Checker {
	checker := Checker{baseURL: defaultBaseURL, rawURL: RawBase}
	checker.client = &http.Client{
		Timeout:       defaultTimeout,
		CheckRedirect: noFollow,
	}
	checker.follow = &http.Client{Timeout: defaultTimeout}
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

// atomFeed is the slice of GitHub's releases.atom this program reads. The tag is
// not a field of its own there: it is the last segment of each entry's link, which
// is why the URL is what gets parsed and releaseTag is what reads it.
type atomFeed struct {
	Entries []struct {
		Links []struct {
			Href string `xml:"href,attr"`
		} `xml:"link"`
	} `xml:"entry"`
}

// Tags returns this project's released tags, in the order the feed lists them,
// which is newest first.
//
// The feed is read rather than the API because the API allows sixty requests per
// hour per IP, shared with every other user behind the same address, and this is
// called by a command a user may run twice in a minute. releases.atom is a
// document encoding/xml reads from the standard library and needs no credential.
//
// A tag that cannot be ordered is dropped rather than returned: the only reason
// this list is read is to choose something to install, and releaseTag already
// refuses the values a caller must not guess about.
//
// A document that is not the feed — one whose entries name no release tag at all
// — is an error, and so is a feed with no entries. "I could not tell" must not be
// reported as "there is no older release" — but a feed that parses and holds only
// unusable entries is an empty list, because that is a real answer with a real
// wording.
func (c Checker) Tags(ctx context.Context) ([]string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+feedPath, nil)
	if err != nil {
		return nil, errors.New("build release feed request")
	}
	request.Header.Set("User-Agent", version.UserAgent)
	response, err := c.follow.Do(request)
	if err != nil {
		return nil, errors.New("release feed lookup failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release feed lookup returned HTTP %s", response.Status)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, errors.New("release feed lookup failed")
	}
	var feed atomFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, errors.New("release feed lookup returned a document that is not a feed")
	}
	tags := make([]string, 0, len(feed.Entries))
	named := false
	for _, entry := range feed.Entries {
		for _, link := range entry.Links {
			if _, ok := tagIn(link.Href); !ok {
				continue
			}
			named = true
			if tag, ok := releaseTag(link.Href); ok {
				tags = append(tags, tag)
			}
			break
		}
	}
	if !named {
		return nil, errors.New("release feed lookup returned no release tag")
	}
	return tags, nil
}

// tagIn reads the last path segment following tagMarker out of a URL, reporting
// false when the URL names no release tag at all. It is the weaker half of
// releaseTag, and exists separately because "this document names releases" and
// "this tag can be ordered" are different questions: a feed of prereleases answers
// the first and not the second.
func tagIn(url string) (string, bool) {
	index := strings.LastIndex(url, tagMarker)
	if index < 0 {
		return "", false
	}
	return strings.Trim(url[index+len(tagMarker):], "/"), true
}

// scriptName is the installer a release publishes at its own tag.
const scriptName = "install.sh"

// Script fetches the installer a release publishes at its own tag.
//
// The release's own script is run rather than a copy carried in this binary, so
// there is one implementation of the download, attestation, and checksum rules
// and a fix to it reaches users on their next update. What that costs is a
// network round trip before the update can begin, which is why the caller checks
// the tools the script needs first.
//
// The body is refused when it is empty or only whitespace. An empty script is the
// one failure that would otherwise be invisible: sh runs it, it does nothing, and
// it exits 0, so a release reported as installed would not have been.
func (c Checker) Script(ctx context.Context, tag string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.rawURL+tag+"/"+scriptName, nil)
	if err != nil {
		return nil, errors.New("build installer request")
	}
	request.Header.Set("User-Agent", version.UserAgent)
	response, err := c.follow.Do(request)
	if err != nil {
		return nil, errors.New("installer download failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("installer download returned HTTP %s", response.Status)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, errors.New("installer download failed")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, errors.New("installer download returned an empty script")
	}
	return body, nil
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
	tag, ok := tagIn(location)
	if !ok || !version.IsVersion(tag) {
		return "", false
	}
	return tag, true
}
