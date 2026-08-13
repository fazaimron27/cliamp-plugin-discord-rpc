// Package playback reads and validates state produced by the Cliamp plugin.
package playback

import (
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
)

const (
	maxStateSize       = 64 << 10
	maxPlaybackSeconds = int64(365 * 24 * time.Hour / time.Second)
	maxFutureSkew      = 5 * time.Minute
)

// State is the version 1 JSON contract shared with discord-rpc.lua.
type State struct {
	Version   int    `json:"v"`
	Session   string `json:"session"`
	Sequence  int64  `json:"seq"`
	Status    string `json:"status"`
	Title     string `json:"title"`
	Artist    string `json:"artist"`
	Album     string `json:"album"`
	Year      int    `json:"year"`
	Duration  int64  `json:"duration"`
	Position  int64  `json:"position"`
	Stream    bool   `json:"stream"`
	Heartbeat int64  `json:"heartbeat"`
	UpdatedAt int64  `json:"updated_at"`
}

// Read decodes and validates one plugin state document.
func Read(path string) (State, error) {
	file, err := openState(path)
	if err != nil {
		return State{}, err
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxStateSize+1))
	if err != nil {
		return State{}, err
	}
	if len(data) > maxStateSize {
		return State{}, errors.New("state document too large")
	}
	var current State
	if err := json.Unmarshal(data, &current); err != nil {
		return State{}, err
	}
	if err := validate(current); err != nil {
		return State{}, err
	}
	return current, nil
}

func validate(s State) error {
	if s.Version != 1 || s.Session == "" || s.Sequence < 1 {
		return errors.New("unsupported or incomplete state document")
	}
	if len(s.Session) > 256 || len(s.Status) > 32 || len(s.Title) > 4096 || len(s.Artist) > 4096 || len(s.Album) > 4096 {
		return errors.New("state document contains oversized fields")
	}
	if s.Status != "playing" && s.Status != "paused" && s.Status != "stopped" {
		return errors.New("state document contains an invalid status")
	}
	if s.Duration < 0 || s.Duration > maxPlaybackSeconds || s.Position < 0 || s.Position > maxPlaybackSeconds || s.Year < 0 || s.Year > 9999 || s.Heartbeat <= 0 || s.UpdatedAt <= 0 {
		return errors.New("state document contains invalid numeric fields")
	}
	latest := time.Now().Add(maxFutureSkew)
	if time.Unix(s.Heartbeat, 0).After(latest) || time.Unix(s.UpdatedAt, 0).After(latest) {
		return errors.New("state timestamps are too far in the future")
	}
	return nil
}

// Revision uniquely identifies a write. Sequence alone is insufficient because
// the Lua plugin resets it whenever Cliamp starts a new session.
func (s State) Revision() string {
	return s.Session + ":" + strconv.FormatInt(s.Sequence, 10)
}

// IsPlaying reports whether state should currently be visible on Discord.
// Paused playback is hidden because Discord cannot freeze activity timestamps.
func (s State) IsPlaying(maxAge time.Duration, now time.Time) bool {
	if s.Status != "playing" || strings.TrimSpace(s.Title) == "" || s.Heartbeat <= 0 {
		return false
	}
	age := now.Sub(time.Unix(s.Heartbeat, 0))
	return age <= maxAge
}

// PresenceKey contains only values that can change the Discord activity.
// Heartbeat and sequence are intentionally excluded, making liveness-only
// writes free while still allowing stale state to clear at evaluation time.
func (s State) PresenceKey(maxAge time.Duration, now time.Time) string {
	if !s.IsPlaying(maxAge, now) {
		return "clear"
	}
	return strings.Join([]string{
		s.Status, s.Title, s.Artist, s.Album,
		strconv.FormatInt(s.Duration, 10), strconv.FormatInt(s.Position, 10),
		strconv.FormatInt(s.UpdatedAt, 10),
	}, "\x00")
}
