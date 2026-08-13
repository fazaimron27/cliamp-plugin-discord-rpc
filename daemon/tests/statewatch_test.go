package tests

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/statewatch"
)

func TestStateWatcherReportsTargetChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rpc-state.json")
	watcher, err := statewatch.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()

	if err := os.WriteFile(path, []byte(`{"seq":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	waitForStateEvent(t, watcher)

	if err := os.WriteFile(path, []byte(`{"seq":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	waitForStateEvent(t, watcher)

	temporary := filepath.Join(dir, "state.tmp")
	if err := os.WriteFile(temporary, []byte(`{"seq":3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporary, path); err != nil {
		t.Fatal(err)
	}
	waitForStateEvent(t, watcher)
}

func TestStateWatcherIgnoresOtherFiles(t *testing.T) {
	dir := t.TempDir()
	watcher, err := statewatch.New(filepath.Join(dir, "rpc-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()

	if err := os.WriteFile(filepath.Join(dir, "other.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-watcher.Events():
		t.Fatal("watcher reported an unrelated file")
	case err := <-watcher.Errors():
		t.Fatalf("watcher error: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
}

func waitForStateEvent(t *testing.T, watcher *statewatch.Watcher) {
	t.Helper()
	select {
	case <-watcher.Events():
	case err := <-watcher.Errors():
		t.Fatalf("watcher error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for state event")
	}
}
