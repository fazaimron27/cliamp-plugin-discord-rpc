// Package status publishes the daemon's Discord connection state where the Lua
// plugin can read it.
package status

// This file is the status document's writer. It exists because the plugin runs
// inside Cliamp while the daemon runs beside it, and nothing carries a fact back
// across that boundary: Cliamp's pub/sub lets a plugin publish, and offers a
// subscriber only to a native client. A file both halves can name is therefore
// the channel, which is the one the file transport already uses in the other
// direction.
//
// The document carries a beat as well as a connection, and the beat is the
// point. What the plugin has to notice is a daemon that stopped, and a state
// written once at startup cannot say that: it would go on reading as a live
// connection for as long as nobody looked. A beat that stops moving is the only
// thing a process that is gone leaves behind.

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Beat is how often the daemon rewrites the status document, and therefore how
// long the plugin's staleness threshold has to be a multiple of. The two are
// held together by the contract test beside the plugin, because a threshold the
// daemon could outrun would report a healthy connection as a stopped daemon.
const Beat = 5 * time.Second

// SchemaVersion is the document's shape, which the plugin refuses to read unless
// it recognizes it. A change here is a change to what the plugin parses, so the
// two move together or the plugin ignores the file. It is exported because the
// contract test beside the plugin reads it to hold the two halves to each other,
// the way the playback document's schema already is.
const SchemaVersion = 1

// State is what the daemon last knew about its Discord connection.
type State int

// The three states are ordered so the zero value is the honest one for a daemon
// that has not yet tried to reach Discord.
const (
	// Unknown is a daemon that has made no attempt, so it has nothing to
	// report. It is not a kind of disconnection: reporting it as one would
	// announce Discord down every time the daemon started with nothing
	// playing, since the daemon connects only when it has something to say.
	Unknown State = iota
	// Connected is a Discord that took the last request made of it.
	Connected
	// Disconnected is a Discord that was unreachable, or that dropped the
	// connection the daemon then closed.
	Disconnected
)

// Reporter writes the daemon's connection state to the status document, and
// keeps that document's beat moving, until its context is cancelled.
type Reporter struct {
	path string
	beat time.Duration
	now  func() time.Time
	// wake carries a state change to the writer, so a change is written when it
	// happens rather than when the next beat falls due. It is buffered and
	// written to without blocking, because the daemon's event loop is the sender
	// and a loop that waited on a file would be the cost this design avoids; the
	// writer reads the current state rather than the message, so a signal that
	// arrives while another is still queued loses nothing.
	wake chan struct{}

	mu    sync.Mutex
	state State
	// reported is the write failure already logged. Failures repeat at the
	// beat, so logging each one would bury the journal it exists to explain,
	// and logging none would leave a status document that never appears
	// unexplained. It is cleared by a write that lands, so a fault that
	// returns after a recovery is reported again.
	reported string
}

// New returns a reporter that writes path every beat. The clock is a parameter
// so a test can move a beat interval without waiting one out.
func New(path string, beat time.Duration, now func() time.Time) *Reporter {
	return &Reporter{path: path, beat: beat, now: now, wake: make(chan struct{}, 1)}
}

// Set records what the daemon now knows and has it written. The daemon's event
// loop never waits on a file: the write happens on the writer's goroutine, which
// this nudges. Only a change is worth writing, since the daemon reports the same
// connection on every publish and a state the plugin has already read does not
// become news by being written again; the beat rewrites the document regardless,
// which is what keeps a daemon that is merely quiet from reading as a dead one.
func (r *Reporter) Set(state State) {
	r.mu.Lock()
	changed := state != r.state
	r.state = state
	r.mu.Unlock()
	if !changed {
		return
	}
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Report writes the document immediately and then on every beat, returning when
// ctx is cancelled. The first write is what stops a document left by a previous
// daemon from standing in for this one while its first beat falls due.
func (r *Reporter) Report(ctx context.Context) {
	r.write()
	ticker := time.NewTicker(r.beat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.write()
		case <-r.wake:
			r.write()
		}
	}
}

// write renders the current state and puts it on disk.
func (r *Reporter) write() {
	r.mu.Lock()
	state := r.state
	r.mu.Unlock()
	payload, err := json.Marshal(document{
		Version:   SchemaVersion,
		Beat:      r.now().Unix(),
		Connected: connection(state),
	})
	if err == nil {
		err = write(r.path, payload)
	}
	r.report(err)
}

// report logs a write failure once, and says nothing at all while writes are
// landing.
func (r *Reporter) report(err error) {
	if err == nil {
		r.reported = ""
		return
	}
	detail := err.Error()
	if detail == r.reported {
		return
	}
	r.reported = detail
	log.Printf("write Discord status: %v", err)
}

// document is the file's shape. Connected is a pointer because the plugin has to
// tell "not connected" from "nothing has been tried yet", and an absent key is
// how those two stay apart on the wire.
type document struct {
	Version   int   `json:"v"`
	Beat      int64 `json:"beat"`
	Connected *bool `json:"connected,omitempty"`
}

// connection renders a state as the document's optional boolean, or nil for a
// state the daemon cannot yet speak to.
func connection(state State) *bool {
	switch state {
	case Connected:
		value := true
		return &value
	case Disconnected:
		value := false
		return &value
	default:
		return nil
	}
}

// write replaces the document in one step, so a plugin polling on a clock of
// its own never reads a half-written file. The temporary file is made beside the
// destination rather than in the system temporary directory, because a rename
// is atomic only within one filesystem.
func write(path string, payload []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, "rpc-status-*.tmp")
	if err != nil {
		return err
	}
	name := temp.Name()
	if _, err := temp.Write(payload); err != nil {
		temp.Close()
		os.Remove(name)
		return err
	}
	if err := temp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
