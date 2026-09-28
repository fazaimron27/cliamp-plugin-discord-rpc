package statewatch_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/statewatch"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/version"
)

// The plugin runs under Cliamp's own Lua, which is not to hand here, so these
// run it under any interpreter that is. They are skipped where none is, which
// is why the text agreements in transport_plugin_test.go exist as well: those
// hold everywhere, and these hold wherever the plugin can really be executed.

// luaRuntimes are tried in order, the versioned names first so a system that
// has a real interpreter does not silently use whatever "lua" points at.
var luaRuntimes = []string{"lua5.4", "lua5.3", "lua", "luajit"}

func luaRuntime(t *testing.T) string {
	t.Helper()
	for _, name := range luaRuntimes {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	t.Skip("no Lua interpreter on PATH, so the plugin itself cannot be run here")
	return ""
}

// pluginRecord is one thing the plugin did under the driver.
type pluginRecord struct {
	Kind string
	// Retain is the p:publish option, which only a publish carries.
	Retain bool
	Body   json.RawMessage
}

// runPlugin loads the real discord-rpc.lua under the stub Cliamp in testdata
// and returns what it published and wrote, in order. transport is what
// config.toml would hold under [plugins.discord-rpc], with "nil" for a plugin
// that was never configured.
func runPlugin(t *testing.T, transport string) []pluginRecord {
	t.Helper()
	driver, err := filepath.Abs(filepath.Join("testdata", "plugin_driver.lua"))
	if err != nil {
		t.Fatal(err)
	}
	plugin, err := filepath.Abs(filepath.Join("..", "..", "..", "discord-rpc.lua"))
	if err != nil {
		t.Fatal(err)
	}

	command := exec.Command(luaRuntime(t), driver, plugin, transport)
	var stderr strings.Builder
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("the plugin failed to run: %v\n%s", err, stderr.String())
	}

	var records []pluginRecord
	for _, line := range strings.Split(strings.TrimRight(string(output), "\n"), "\n") {
		fields := strings.SplitN(line, "\t", 4)
		record := pluginRecord{Kind: fields[0]}
		switch record.Kind {
		case "error":
			t.Fatalf("the plugin reported an error: %s", strings.Join(fields[1:], " "))
		case "write":
			record.Body = json.RawMessage(fields[1])
		case "publish":
			record.Retain = fields[1] == "true"
			record.Body = json.RawMessage(fields[2])
		default:
			t.Fatalf("the driver produced an unknown line: %q", line)
		}
		records = append(records, record)
	}
	if len(records) == 0 {
		t.Fatal("the plugin produced nothing at all")
	}
	return records
}

// recordsOfKind selects what the plugin did, in order.
func recordsOfKind(records []pluginRecord, kind string) []pluginRecord {
	var selected []pluginRecord
	for _, record := range records {
		if record.Kind == kind {
			selected = append(selected, record)
		}
	}
	return selected
}

// inspectWritten reads a document the plugin wrote with the code the daemon
// runs, so what the test asserts is the document the daemon would see rather
// than the plugin's idea of it.
func inspectWritten(t *testing.T, body json.RawMessage) statewatch.Detail {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rpc-state.json")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	detail := statewatch.Inspect(path, time.Minute)
	if detail.Problem != nil {
		t.Fatalf("the daemon refuses the document the plugin wrote: %v\n%s", detail.Problem, body)
	}
	return detail
}

// The IPC transport is what shipped before the state file, and the daemon's
// startup display depends on these snapshots being retained: a daemon started
// mid-track has to be shown the track it missed.
func TestPluginPublishesOverIPCWhenNothingIsConfigured(t *testing.T) {
	records := runPlugin(t, "nil")

	if writes := recordsOfKind(records, "write"); len(writes) != 0 {
		t.Fatalf("an unconfigured plugin wrote %d documents; IPC is the default", len(writes))
	}
	// app.start, track.change, app.quit. The heartbeat is the file transport's
	// alone: over IPC the daemon's own subscription is the liveness signal.
	publishes := recordsOfKind(records, "publish")
	if len(publishes) != 3 {
		t.Fatalf("published %d snapshots, want one per event", len(publishes))
	}
	for _, publish := range publishes {
		if !publish.Retain {
			t.Errorf("a snapshot was published without retention: %s", publish.Body)
		}
	}

	var started struct {
		Status        string `json:"status"`
		Title         string `json:"title"`
		PluginVersion string `json:"plugin_version"`
	}
	if err := json.Unmarshal(publishes[0].Body, &started); err != nil {
		t.Fatal(err)
	}
	if started.Status != "playing" || started.Title != "Track" {
		t.Errorf("the first snapshot = %+v", started)
	}
	if started.PluginVersion != version.Number {
		t.Errorf("plugin version = %q, want the release this repo pins, %q", started.PluginVersion, version.Number)
	}

	var quit struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(publishes[2].Body, &quit); err != nil {
		t.Fatal(err)
	}
	if quit.Status != "stopped" {
		t.Errorf("the snapshot published on quit = %+v", quit)
	}
}

// What the daemon reads in file mode, produced the way the plugin produces it:
// app.start, a track change carrying one field, one heartbeat, and a quit.
func TestPluginWritesTheDocumentTheDaemonReads(t *testing.T) {
	writes := recordsOfKind(runPlugin(t, "file"), "write")
	if len(writes) != 4 {
		t.Fatalf("wrote %d documents, want one per event and one for the heartbeat", len(writes))
	}

	started := inspectWritten(t, writes[0].Body)
	if !started.State.IsPlaying() || started.State.Title != "Track" {
		t.Errorf("the document written at startup = %+v", started.State)
	}
	if started.State.PluginVersion != version.Number {
		t.Errorf("plugin version = %q, want the release this repo pins, %q", started.State.PluginVersion, version.Number)
	}

	// The event carries the new title and nothing else, so the fields it leaves
	// out have to come from the player. A document built only from the event
	// would reach Discord as a track with no artist and no position.
	changed := inspectWritten(t, writes[1].Body)
	if changed.State.Title != "Second Track" {
		t.Errorf("the document written on the track change = %+v", changed.State)
	}
	if changed.State.Artist != "Artist" || changed.State.Position != 61 {
		t.Errorf("the track change dropped what the event left out: %+v", changed.State)
	}

	// The heartbeat's whole point is that it is not a change. The daemon
	// interpolates the playhead from updated_at, so a beat that moved it would
	// re-anchor the progress bar on a position that had not moved.
	beat := inspectWritten(t, writes[2].Body)
	if beat.State.ObservedAt != changed.State.ObservedAt {
		t.Errorf("a heartbeat moved the change time from %d to %d", changed.State.ObservedAt, beat.State.ObservedAt)
	}
	if beat.Age >= changed.Age {
		t.Errorf("the heartbeat did not advance: %v then %v", changed.Age, beat.Age)
	}
	// And it still has to carry the whole track: the daemon reads the last
	// document rather than remembering the earlier ones.
	if beat.State.Title != changed.State.Title || beat.State.Position != changed.State.Position {
		t.Errorf("the heartbeat wrote a partial document: %+v", beat.State)
	}

	if stopped := inspectWritten(t, writes[3].Body); stopped.State.IsPlaying() {
		t.Errorf("the document written on quit still reads as playing: %+v", stopped.State)
	}
}
