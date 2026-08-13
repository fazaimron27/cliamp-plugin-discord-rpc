// Package statewatch reports changes to a state file's parent directory.
package statewatch

import (
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

const debounce = 20 * time.Millisecond

type Watcher struct {
	watcher *fsnotify.Watcher
	events  chan struct{}
	errors  chan error
	done    chan struct{}
}

// New watches the directory containing path. The directory must already exist.
func New(path string) (*Watcher, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := watcher.Add(filepath.Dir(path)); err != nil {
		_ = watcher.Close()
		return nil, err
	}
	w := &Watcher{
		watcher: watcher,
		events:  make(chan struct{}, 1),
		errors:  make(chan error, 1),
		done:    make(chan struct{}),
	}
	go w.run(filepath.Clean(path))
	return w, nil
}

func (w *Watcher) Events() <-chan struct{} { return w.events }
func (w *Watcher) Errors() <-chan error    { return w.errors }

func (w *Watcher) Close() error {
	select {
	case <-w.done:
		return nil
	default:
		close(w.done)
		return w.watcher.Close()
	}
}

func (w *Watcher) run(path string) {
	defer close(w.events)
	defer close(w.errors)
	var timer *time.Timer
	var pending <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	notify := func() {
		if timer == nil {
			timer = time.NewTimer(debounce)
		} else {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(debounce)
		}
		pending = timer.C
	}
	for {
		select {
		case <-w.done:
			return
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			if filepath.Clean(event.Name) == path && event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) != 0 {
				notify()
			}
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			select {
			case w.errors <- err:
			default:
			}
		case <-pending:
			pending = nil
			select {
			case w.events <- struct{}{}:
			default:
			}
		}
	}
}
