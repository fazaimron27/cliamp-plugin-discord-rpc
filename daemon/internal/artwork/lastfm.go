// Package artwork resolves public album artwork URLs.
package artwork

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxResponseSize = 1 << 20

// LastFM resolves track artwork through Last.fm's track.getInfo endpoint.
// Empty results are cached too, preventing repeated misses for the same track.
type LastFM struct {
	apiKey   string
	endpoint string
	client   *http.Client
	cache    map[string]string
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

func NewLastFM(apiKey string, options ...Option) *LastFM {
	resolver := &LastFM{
		apiKey:   apiKey,
		endpoint: "https://ws.audioscrobbler.com/2.0/",
		client:   &http.Client{Timeout: 4 * time.Second},
		cache:    make(map[string]string),
	}
	for _, option := range options {
		option(resolver)
	}
	return resolver
}

// Resolve returns the largest valid HTTPS image in Last.fm's response.
func (r *LastFM) Resolve(ctx context.Context, artist, title string) (string, error) {
	if r.apiKey == "" || strings.TrimSpace(artist) == "" || strings.TrimSpace(title) == "" {
		return "", nil
	}
	key := strings.ToLower(strings.TrimSpace(artist) + "\x00" + strings.TrimSpace(title))
	if image, ok := r.cache[key]; ok {
		return image, nil
	}

	query := url.Values{
		"method":      {"track.getInfo"},
		"api_key":     {r.apiKey},
		"artist":      {artist},
		"track":       {title},
		"autocorrect": {"1"},
		"format":      {"json"},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "cliamp-rpcd/1.1.0")
	response, err := r.client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Last.fm returned HTTP %s", response.Status)
	}

	var result struct {
		Track struct {
			Album struct {
				Images []struct {
					URL string `json:"#text"`
				} `json:"image"`
			} `json:"album"`
		} `json:"track"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseSize)).Decode(&result); err != nil {
		return "", err
	}
	image := ""
	for _, candidate := range result.Track.Album.Images {
		parsed, err := url.Parse(candidate.URL)
		if err == nil && parsed.Scheme == "https" && parsed.Host != "" {
			image = candidate.URL
		}
	}
	r.cache[key] = image
	return image, nil
}
