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
	// The shared rules for a snapshot apply here too: whatever the file carries
	// is still a snapshot of the same player, and the daemon must not have to
	// know which transport it arrived by.
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
func Subscribe(ctx context.Context, path string, maxAge time.Duration) (<-chan playback.State, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("watch the state file: %w", err)
	}
	// The directory is watched rather than the document, because the plugin
	// writes with a plain write that replaces the file: a watch on the document
	// itself would be watching an inode that the very next write leaves behind,
	// and the removal on quit would be missed as well.
	if err := watcher.Add(filepath.Dir(path)); err != nil {
		_ = watcher.Close()
		return nil, fmt.Errorf("watch the state file: %w", err)
	}

	states := make(chan playback.State)
	go watch(ctx, watcher, path, maxAge, states)
	return states, nil
}

// watch is the goroutine behind Subscribe. It closes states when it returns.
func watch(ctx context.Context, watcher *fsnotify.Watcher, path string, maxAge time.Duration, states chan<- playback.State) {
	defer close(states)
	defer func() { _ = watcher.Close() }()

	// Go 1.23 made a timer's channel unbuffered, so Stop and Reset are safe to
	// call at any time without draining what they were armed for.
	deadline := time.NewTimer(time.Hour)
	settle := time.NewTimer(time.Hour)
	deadline.Stop()
	settle.Stop()
	defer deadline.Stop()
	defer settle.Stop()
	var settling <-chan time.Time

	// showing is whether the daemon was last told about a live track. A retract
	// with nothing showing has nothing to retract: this channel reports
	// changes, and reporting "stopped" for a document that was never reported
	// as playing would be inventing one.
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
		// Measured from the heartbeat, not from now, so a document that has
		// already lapsed fires at once instead of lingering for another window.
		// time.Timer treats a negative duration that way.
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

	// live reports whether the document's heartbeat is recent enough to be
	// evidence that Cliamp is still running.
	live := func(current snapshot) bool {
		return current.liveAt(time.Now(), maxAge)
	}

	load := func() bool {
		current, err := read(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			// The plugin removes the document on a clean quit.
			return retract()
		case err != nil:
			// Present but unreadable, a document caught mid-write most likely:
			// the next write replaces it, so one bad read is not evidence about
			// the track. Leaving the state as it is also means a document this
			// daemon cannot parse never gets mistaken for Cliamp having quit —
			// the deadline retracts it if the writes have really stopped.
			return true
		case !live(current):
			// Left behind by a crash: too old to be evidence that Cliamp is
			// running, so it must not resurrect the last track.
			return retract()
		default:
			armDeadline(current)
			showing = true
			return deliver(current.state)
		}
	}

	// Before the loop, so a daemon started mid-track shows the track, which is
	// what the IPC transport gets from the retained snapshot. Watching comes
	// first, so a write cannot slip between the check and the watch.
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
			// The watched directory may hold other things Cliamp keeps there.
			if filepath.Clean(event.Name) != filepath.Clean(path) {
				continue
			}
			settle.Reset(settleDelay)
			settling = settle.C
		case <-watcher.Errors:
			// The watcher reports here when it could not deliver an event, so
			// the document may have changed with nothing to say so. Reading it
			// again costs one file read and closes that gap.
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
