// Package playback validates playback snapshots published by the Cliamp plugin.
package playback

// This file holds the playback snapshot both halves of the project agree on.
// State's fields and JSON tags are the wire contract the Lua plugin publishes to
// and the daemon accepts; Validate rejects payloads outside it, and IsPlaying,
// TrackKey, and PresenceKey derive the three values the rest of the daemon asks
// a snapshot for.

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

const maxPlaybackSeconds = int64(365 * 24 * time.Hour / time.Second)

// State is the retained playback snapshot published over Cliamp IPC.
type State struct {
	Status   string `json:"status"`
	Title    string `json:"title"`
	Artist   string `json:"artist"`
	Album    string `json:"album"`
	Path     string `json:"path"`
	Year     int    `json:"year"`
	Duration int64  `json:"duration"`
	Position int64  `json:"position"`
	Stream   bool   `json:"stream"`
	// PluginVersion is the release that published this snapshot. It is reporting
	// metadata for mismatched-pairing detection, not part of the track, so it is
	// absent from PresenceKey and TrackKey. Older plugins omit it.
	PluginVersion string `json:"plugin_version"`
	ObservedAt    int64  `json:"-"`
	StartedAt     int64  `json:"-"`
}

// Validate rejects malformed or unreasonably large plugin payloads.
func (s State) Validate() error {
	if s.Status != "playing" && s.Status != "paused" && s.Status != "stopped" {
		return errors.New("playback snapshot contains an invalid status")
	}
	if len(s.Status) > 32 || len(s.Title) > 4096 || len(s.Artist) > 4096 || len(s.Album) > 4096 || len(s.Path) > 16384 || len(s.PluginVersion) > 32 {
		return errors.New("playback snapshot contains oversized fields")
	}
	if strings.ContainsRune(s.Title, 0) || strings.ContainsRune(s.Artist, 0) || strings.ContainsRune(s.Album, 0) || strings.ContainsRune(s.Path, 0) || strings.ContainsRune(s.PluginVersion, 0) {
		return errors.New("playback snapshot contains invalid text")
	}
	if s.Duration < 0 || s.Duration > maxPlaybackSeconds || s.Position < 0 || s.Position > maxPlaybackSeconds || s.Year < 0 || s.Year > 9999 {
		return errors.New("playback snapshot contains invalid numeric fields")
	}
	return nil
}

// IsPlaying reports whether the snapshot should currently be visible.
func (s State) IsPlaying() bool {
	return s.Status == "playing" && strings.TrimSpace(s.Title) != ""
}

// TrackKey identifies the source track without exposing its path to Discord.
func (s State) TrackKey() string {
	return strings.Join([]string{s.Path, s.Title, s.Artist, s.Album, strconv.FormatInt(s.Duration, 10)}, "\x00")
}

// PresenceKey contains only values that affect the Discord activity. PluginVersion
// is deliberately excluded: a plugin upgrade changes no rendered field, so
// including it would force a needless activity update on every upgrade.
func (s State) PresenceKey() string {
	if !s.IsPlaying() {
		return "clear"
	}
	return strings.Join([]string{
		s.Status, s.Title, s.Artist, s.Album,
		strconv.FormatInt(s.Duration, 10), strconv.FormatInt(s.StartedAt, 10),
		strconv.FormatBool(s.Stream),
	}, "\x00")
}
