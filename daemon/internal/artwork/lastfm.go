// Package artwork resolves public album artwork URLs.
package artwork

// This file is the Last.fm half of artwork resolution: it asks track.getInfo
// for a track's image over HTTPS and remembers each answer with an expiry, so a
// resolved URL is reused and an empty answer is retried rather than refetched
// on every track change. The absence of artwork is reported as an empty string
// with no error, because Last.fm returning nothing is not a failure.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/tracklink"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/version"
)

const userAgent = "cliamp-rpcd/" + version.Number

const (
	maxResponseSize = 1 << 20
	// failureRetry is how long "no artwork yet" stands before the track is asked
	// about again. A failed request and a response carrying no usable image are
	// the same state of knowledge — Last.fm did not supply a URL — so they are
	// held for the same interval.
	failureRetry = 30 * time.Second
	// cacheTTL bounds how long a resolved URL is reused. Far longer than
	// failureRetry, because a URL is knowledge about the track rather than about
	// the moment, and it is also what bounds the map: only tracks looked up
	// within this window can still be held.
	cacheTTL = time.Hour
)

// lookup is what the resolver knows about one track — the public pages Last.fm
// reported for it, or the absence of them — and when that stops being worth
// repeating.
type lookup struct {
	info    TrackInfo
	expires time.Time
}

// TrackInfo is what one Last.fm lookup yields: a public image URL and the
// public pages for the track and its artist. An empty field means Last.fm
// supplied nothing usable for it, which is not an error.
//
// The three arrive together in one response, so reading the two pages costs no
// request the artwork did not already make. They are comparable, which is how
// remember tells a real answer from an empty one.
type TrackInfo struct {
	Image     string
	TrackURL  string
	ArtistURL string
}

// live reports whether an entry is still worth serving. Serving and pruning ask
// the same question through this one method, so the entry it stops serving is
// the entry it drops.
func (l lookup) live(now time.Time) bool {
	return now.Before(l.expires)
}

// LastFM resolves track artwork through Last.fm's track.getInfo endpoint. It
// remembers what it learns with an expiry: a hit is reused, a miss is retried.
type LastFM struct {
	apiKey   string
	endpoint string
	client   *http.Client
	known    map[string]lookup
	now      func() time.Time
}

// Option customizes a Last.fm resolver. The defaults are suitable for normal
// use; options are useful for proxies, custom transports, and tests.
type Option func(*LastFM)

// WithEndpoint overrides the Last.fm API endpoint.
func WithEndpoint(endpoint string) Option {
	return func(resolver *LastFM) { resolver.endpoint = endpoint }
}

// WithHTTPClient overrides the HTTP client used for artwork requests.
func WithHTTPClient(client *http.Client) Option {
	return func(resolver *LastFM) { resolver.client = client }
}

// WithClock overrides the clock that decides when a remembered lookup expires.
func WithClock(now func() time.Time) Option {
	return func(resolver *LastFM) { resolver.now = now }
}

// NewLastFM returns a resolver for the given API key. The options override the
// defaults it sets up: Last.fm's public track.getInfo endpoint, an HTTP client
// with a four-second timeout, and the real clock.
func NewLastFM(apiKey string, options ...Option) *LastFM {
	resolver := &LastFM{
		apiKey:   apiKey,
		endpoint: "https://ws.audioscrobbler.com/2.0/",
		client:   &http.Client{Timeout: 4 * time.Second},
		known:    make(map[string]lookup),
		now:      time.Now,
	}
	for _, option := range options {
		option(resolver)
	}
	return resolver
}

// remember records what a lookup found. An answer naming an image is held for
// cacheTTL; an answer without one is retried after failureRetry, because it
// describes a moment rather than the track.
//
// The expiry is keyed on the image rather than on "any field is set". That
// keeps the request cadence exactly what it was before the two page URLs were
// read: a response that carries pages but no artwork is still retried on the
// retry window, so the URLs ride along on requests that were already going to
// happen rather than causing new ones.
func (r *LastFM) remember(key string, info TrackInfo) {
	r.forget()
	expires := r.now().Add(failureRetry)
	if info.Image != "" {
		expires = r.now().Add(cacheTTL)
	}
	r.known[key] = lookup{info: info, expires: expires}
}

// forget drops every lookup that has expired, so the map holds only live
// entries. It runs on write rather than on a timer because a lookup is the only
// thing that adds one, and the TTLs keep the map small enough for the scan: only
// tracks looked up in the last cacheTTL can be present.
func (r *LastFM) forget() {
	now := r.now()
	for key, entry := range r.known {
		if !entry.live(now) {
			delete(r.known, key)
		}
	}
}

// Resolve returns the public pages Last.fm reports for a track: the largest
// valid HTTPS image, and the track and artist pages that arrive in the same
// response.
func (r *LastFM) Resolve(ctx context.Context, artist, title string) (TrackInfo, error) {
	if r.apiKey == "" || strings.TrimSpace(artist) == "" || strings.TrimSpace(title) == "" {
		return TrackInfo{}, nil
	}
	key := strings.ToLower(strings.TrimSpace(artist) + "\x00" + strings.TrimSpace(title))
	if entry, ok := r.known[key]; ok && entry.live(r.now()) {
		return entry.info, nil
	}

	body, err := r.get(ctx, url.Values{
		"method":      {"track.getInfo"},
		"api_key":     {r.apiKey},
		"artist":      {artist},
		"track":       {title},
		"autocorrect": {"1"},
		"format":      {"json"},
	})
	if err != nil {
		return TrackInfo{}, r.failed(key, err)
	}

	var result struct {
		Track struct {
			URL    string `json:"url"`
			Artist struct {
				URL string `json:"url"`
			} `json:"artist"`
			Album struct {
				Images []struct {
					URL string `json:"#text"`
				} `json:"image"`
			} `json:"album"`
		} `json:"track"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return TrackInfo{}, r.failed(key, fmt.Errorf("decode Last.fm response: %w", err))
	}
	image := ""
	for _, candidate := range result.Track.Album.Images {
		if valid := lastFMURL(candidate.URL); valid != "" {
			image = valid
		}
	}
	info := TrackInfo{
		Image:     image,
		TrackURL:  lastFMURL(result.Track.URL),
		ArtistURL: lastFMURL(result.Track.Artist.URL),
	}
	r.remember(key, info)
	return info, nil
}

// lastFMURL returns raw only when it is a URL this daemon may publish and it
// names a host, and an empty string for anything else. Every URL in a response
// goes through this, so the three fields are held to one rule.
//
// Discord rejects the entire activity when a single field is a malformed URL,
// which would cost the card its artwork as well as the link. A value we cannot
// vouch for is therefore dropped here, where dropping it costs nothing.
//
// The host requirement is this site's own: a Last.fm payload is the one place
// a URL is republished with nothing else checking it, so "https:///x" has to
// be refused here rather than by an allowlist downstream.
func lastFMURL(raw string) string {
	parsed, ok := tracklink.PublishableHTTPS(raw)
	if !ok || parsed.Host == "" {
		return ""
	}
	return raw
}

// failed remembers a lookup that produced no artwork and returns the error
// unchanged for the caller to report.
func (r *LastFM) failed(key string, err error) error {
	r.remember(key, TrackInfo{})
	return err
}

// get performs one Last.fm request and returns its body, capped at
// maxResponseSize. Resolve and Validate both go through it so the request shape
// — URL, header, size limit — and the policy on what an error may say are stated
// once rather than twice.
//
// The errors deliberately do not wrap what they came from. Both the
// request-building error and the transport error embed the request URL, and the
// URL carries the API key; the daemon logs these, so wrapping either would put
// the key in the journal. Validate used to collapse them for exactly that
// reason, ten lines away from Resolve passing the same error straight through.
func (r *LastFM) get(ctx context.Context, query url.Values) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return nil, errors.New("build Last.fm request")
	}
	request.Header.Set("User-Agent", userAgent)
	response, err := r.client.Do(request)
	if err != nil {
		return nil, errors.New("Last.fm request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Last.fm returned HTTP %s", response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseSize))
	if err != nil {
		return nil, errors.New("read Last.fm response")
	}
	return body, nil
}

// probeArtist and probeTrack identify the track Validate asks Last.fm about. Any
// track with a populated record works: the question being answered is whether
// the API accepts the key, which it settles before looking the track up.
const (
	probeArtist = "Cher"
	probeTrack  = "Believe"
)

// Validate performs one request and reports whether Last.fm accepts the
// configured API key. A missing key is the caller's concern, not this method's.
//
// This is a probe rather than a lookup, so it deliberately bypasses r.known. A
// diagnostic has to report the state of the key, not the state of the cache.
//
// A rejected key arrives as an error object inside an HTTP 200 response, so the
// body is the only place the rejection is visible. Resolve cannot see this,
// which is why it cannot tell a bad key from a track with no artwork.
func (r *LastFM) Validate(ctx context.Context) error {
	body, err := r.get(ctx, url.Values{
		"method":  {"track.getInfo"},
		"api_key": {r.apiKey},
		"artist":  {probeArtist},
		"track":   {probeTrack},
		"format":  {"json"},
	})
	if err != nil {
		return err
	}

	var result struct {
		Error   int    `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("decode Last.fm response: %w", err)
	}
	if result.Error != 0 {
		return fmt.Errorf("Last.fm rejected the API key: %s (code %d)", result.Message, result.Error)
	}
	return nil
}
