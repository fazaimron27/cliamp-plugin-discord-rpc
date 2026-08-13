// Package playback reads and validates state produced by the CLIamp plugin.
package playback

import (
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
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
	Path      string `json:"path"`
	Stream    bool   `json:"stream"`
	Heartbeat int64  `json:"heartbeat"`
	UpdatedAt int64  `json:"updated_at"`
}

// Read decodes and validates one plugin state document.
func Read(path string) (State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}, err
	}
	var current State
	if err := json.Unmarshal(data, &current); err != nil {
		return State{}, err
	}
	if current.Version != 1 || current.Session == "" || current.Sequence < 1 {
		return State{}, errors.New("unsupported or incomplete state document")
	}
	return current, nil
}

// Revision uniquely identifies a write. Sequence alone is insufficient because
// the Lua plugin resets it whenever CLIamp starts a new session.
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
