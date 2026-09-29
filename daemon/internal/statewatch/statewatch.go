// Package statewatch reads playback state from the file transport: the state
// document the plugin writes with cliamp.fs, for Cliamp builds whose plugin
// events cannot carry a retained playback snapshot.
//
// It is the file counterpart of the IPC subscription, and it presents the same
// interface: a channel of playback.State that stays open for the life of the
// context. Cliamp quitting or restarting is an event on that subscription, not
// the end of it, because the run loop treats a closed channel as a transport to
// reconnect and there is nothing to reconnect here.
package statewatch

// This file is the file transport: it watches the state document the plugin
// writes with cliamp.fs and turns it into the same stream of playback.State the
// IPC subscription delivers, so the run loop never branches on the transport.
//
// The document carries two clocks with different jobs, and that split is the
// point of the format. updated_at moves only when the playback fields do, which
// keeps the interpolated progress bar anchored; heartbeat moves on every write
// whether or not anything changed, and liveness is read from it alone. A track
// that is simply playing on is therefore never mistaken for a Cliamp that has
// stopped reporting.
//
// The file is replaced rather than appended to, so the same path is left behind
// by a clean quit, by a crash, and by a first start. Which of those the loop
// believes is decided in watch, where the reasoning for each is written down.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/playback"
)

const (
	// SchemaVersion is the document version this package reads, and it is
	// exported because the plugin writes it: a document declares its own schema,
	// and the two halves agreeing on this number is the whole of the contract
	// between them.
	//
	// A document declaring another one is refused rather than read loosely: a
	// version this daemon has not been taught about may mean something different
	// by the same field names. The cost of refusing is that the presence goes
	// quiet, which cliamp-rpcd --check reports.
	SchemaVersion = 1
	// settleDelay is how long the watcher waits for a write to stop producing
	// events before reading. The plugin replaces the whole document in one
	// write, and the kernel reports that as several events, so reading on the
	// first one would read it part-written.
	settleDelay = 20 * time.Millisecond
)

// document is the state document the plugin writes. The field names and the
// split between updated_at and heartbeat come from the format v1.4.0 shipped,
// which an existing legacy install already writes.
type document struct {
	Schema        int    `json:"v"`
	PluginVersion string `json:"plugin_version"`
	Status        string `json:"status"`
	Title         string `json:"title"`
	Artist        string `json:"artist"`
	Album         string `json:"album"`
	Path          string `json:"path"`
	Year          int    `json:"year"`
	Duration      int64  `json:"duration"`
	Position      int64  `json:"position"`
	Stream        bool   `json:"stream"`
	// UpdatedAt moves only when the playback fields do. It is what the IPC
	// envelope calls observed_at, and it is what keeps the progress bar honest:
	// the daemon interpolates the playhead from it, so a heartbeat that moved it
	// while the position stood still would re-anchor the bar on every beat.
	UpdatedAt int64 `json:"updated_at"`
	// Heartbeat is liveness only, refreshed whether or not anything changed. It
	// never reaches playback.State.
	Heartbeat int64 `json:"heartbeat"`
}

// snapshot is a document that has been read and checked, with its two jobs kept
// apart: the state that describes what to show, and the heartbeat that says the
// writer is still running.
type snapshot struct {
	state     playback.State
	heartbeat time.Time
}

// lapseIn is how much longer the document is live at the given moment, which is
// until its heartbeat is older than maxAge. It is zero or negative for a
// document that has already lapsed, and it is the one expression behind both
// questions asked of it: whether to believe the document at all, and when to
// stop believing it.
//
// The heartbeat carries whole seconds, as the format does, so a lapsed window
// is not finer than that: what this measures against is the moment the plugin
// last wrote, to the second.
func (s snapshot) lapseIn(now time.Time, maxAge time.Duration) time.Duration {
	return s.heartbeat.Add(maxAge).Sub(now)
}

// liveAt reports whether the document is recent enough at the given moment to
// be believed at all. A heartbeat exactly one window old is not: by then the
// plugin has missed the beats the window was sized to tolerate.
func (s snapshot) liveAt(now time.Time, maxAge time.Duration) bool {
	return s.lapseIn(now, maxAge) > 0
}

// read loads the document at path, reporting the error os.ReadFile would if it
// is not there.
func read(path string) (snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return snapshot{}, err
	}
	return decode(data)
}

// decode maps a document onto the daemon's state, rejecting one that cannot be
// trusted to describe the track.
//
// The shared snapshot rules apply here as much as on the IPC side: what the file
// carries is still a snapshot of the same player, and the daemon is not supposed
// to have to know which transport it arrived by.
func decode(data []byte) (snapshot, error) {
	var parsed document
	if err := json.Unmarshal(data, &parsed); err != nil {
		return snapshot{}, fmt.Errorf("state document is not readable: %w", err)
	}
	if parsed.Schema != SchemaVersion {
		return snapshot{}, fmt.Errorf(
			"state document schema %d is not the schema %d this daemon reads",
			parsed.Schema, SchemaVersion,
		)
	}
	state := playback.State{
		Status:        parsed.Status,
		Title:         parsed.Title,
		Artist:        parsed.Artist,
		Album:         parsed.Album,
		Path:          parsed.Path,
		Year:          parsed.Year,
		Duration:      parsed.Duration,
		Position:      parsed.Position,
		Stream:        parsed.Stream,
		PluginVersion: parsed.PluginVersion,
		ObservedAt:    parsed.UpdatedAt,
	}
	if err := state.Validate(); err != nil {
		return snapshot{}, err
	}
	if parsed.UpdatedAt <= 0 {
		return snapshot{}, errors.New("state document has no updated_at time")
	}
	if parsed.Heartbeat <= 0 {
		return snapshot{}, errors.New("state document has no heartbeat")
	}
	return snapshot{state: state, heartbeat: time.Unix(parsed.Heartbeat, 0)}, nil
}

// Detail is what can be said about the state document as it stands, for a
// diagnostic that has to explain a silent transport.
type Detail struct {
	// Present reports whether the document is on disk at all.
	Present bool
	// Problem is why a document that is on disk could not be read as playback
	// state, and is nil for one that was read.
	Problem error
	// Lapsed reports that the document read cleanly but was already older than
	// the window, so it is not evidence that Cliamp is running.
	Lapsed bool
	// Age is how long ago the plugin last beat.
	Age time.Duration
	// State is what the document said.
	State playback.State
}

// Inspect reads the document at path once and reports what it found, which is
// the question Subscribe cannot answer: it delivers what changes, and the case
// worth diagnosing is the one where nothing does.
func Inspect(path string, maxAge time.Duration) Detail {
	current, err := read(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return Detail{}
	case err != nil:
		return Detail{Present: true, Problem: err}
	}
	now := time.Now()
	return Detail{
		Present: true,
		Age:     now.Sub(current.heartbeat),
		Lapsed:  !current.liveAt(now, maxAge),
		State:   current.state,
	}
}

// Subscribe watches the document at path and delivers each document as a
// playback.State, mirroring the IPC subscription so the run loop does not have
// to branch on the transport.
//
// maxAge bounds how long a document stays live after its last heartbeat. A
// document older than that is not reported as playing, and one that ages out
// after it was delivered is followed by a stopped state. The file transport has
// no close event to play the part the IPC connection's does, so without this a
// Cliamp that crashed would leave its last track on Discord indefinitely.
//
// The returned channel stays open until ctx is cancelled.
//
// The directory is watched rather than the document, because the plugin writes
// with a plain write that replaces the file: a watch on the document itself
// would be watching an inode that the very next write leaves behind, and the
// removal on quit would be missed along with it.
func Subscribe(ctx context.Context, path string, maxAge time.Duration) (<-chan playback.State, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("watch the state file: %w", err)
	}
	if err := watcher.Add(filepath.Dir(path)); err != nil {
		_ = watcher.Close()
		return nil, fmt.Errorf("watch the state file: %w", err)
	}

	states := make(chan playback.State)
	go watch(ctx, watcher, path, maxAge, states)
	return states, nil
}

// watch is the goroutine behind Subscribe. It closes states when it returns.
//
// It reads the document once before entering the loop, so a daemon started
// mid-track shows that track — the same thing the IPC transport gets from the
// retained snapshot — and the watch is armed before that read, so a write cannot
// slip into the gap between the two.
//
// Every read lands in one of four cases, and the case decides what the loop
// reports. A missing document is the plugin's clean quit, and retracts. An
// unreadable one was most likely caught mid-write, and the next write replaces
// it, so a single bad read is not evidence about the track and the state is left
// as it is; that also keeps a document this daemon cannot parse from being
// mistaken for a Cliamp that quit, since the deadline below retracts it if the
// writes have really stopped. A document whose heartbeat has aged out was left
// behind by a crash, so it must not resurrect the last track. A live one is
// delivered, and arms the deadline that will retract it when its beats stop.
//
// A retraction is suppressed while nothing is showing, because this channel
// reports changes: a stopped state for a document that was never reported as
// playing would be inventing one.
//
// The deadline is measured from the heartbeat rather than from now, so a
// document that has already lapsed fires at once instead of lingering for
// another window, which is how time.Timer reads a negative duration. Both timers
// start stopped and are reset as they are armed, which is safe without draining
// them, since a timer's channel is unbuffered.
//
// The watched directory may hold other things Cliamp keeps there, so an event
// naming another path is ignored. An error from the watcher means an event may
// have been dropped without the document being read, so the loop reads it again:
// one file read closes that gap.
func watch(ctx context.Context, watcher *fsnotify.Watcher, path string, maxAge time.Duration, states chan<- playback.State) {
	defer close(states)
	defer func() { _ = watcher.Close() }()

	deadline := time.NewTimer(time.Hour)
	settle := time.NewTimer(time.Hour)
	deadline.Stop()
	settle.Stop()
	defer deadline.Stop()
	defer settle.Stop()
	var settling <-chan time.Time

	var showing bool

	deliver := func(state playback.State) bool {
		select {
		case states <- state:
			return true
		case <-ctx.Done():
			return false
		}
	}

	armDeadline := func(current snapshot) {
		deadline.Reset(current.lapseIn(time.Now(), maxAge))
	}

	retract := func() bool {
		deadline.Stop()
		if !showing {
			return true
		}
		showing = false
		return deliver(playback.State{Status: "stopped", ObservedAt: time.Now().Unix()})
	}

	live := func(current snapshot) bool {
		return current.liveAt(time.Now(), maxAge)
	}

	load := func() bool {
		current, err := read(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			return retract()
		case err != nil:
			return true
		case !live(current):
			return retract()
		default:
			armDeadline(current)
			showing = true
			return deliver(current.state)
		}
	}

	if !load() {
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if filepath.Clean(event.Name) != filepath.Clean(path) {
				continue
			}
			settle.Reset(settleDelay)
			settling = settle.C
		case <-watcher.Errors:
			if !load() {
				return
			}
		case <-settling:
			if !load() {
				return
			}
		case <-deadline.C:
			if !retract() {
				return
			}
		}
	}
}
