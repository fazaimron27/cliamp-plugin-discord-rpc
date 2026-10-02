package statewatch_test

// This file tests the Lua plugin by running it under a real interpreter, which
// is why it skips where none is on PATH: Cliamp's own Lua is not to hand, so
// the plugin runs under any interpreter that is. The text agreements in
// transport_plugin_test.go exist for precisely that reason: they hold
// everywhere, and these hold wherever the plugin can really be executed.
//
// The plugin is driven by testdata/plugin_driver.lua, which stands in for
// Cliamp and records what the plugin published and wrote, in order.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/statewatch"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/version"
)

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
	// Text and Duration are the cliamp.message arguments, which only a
	// message carries.
	Text     string
	Duration int
}

// runPlugin loads the real discord-rpc.lua under the stub Cliamp in testdata
// and returns what it published, wrote and said, in order. transport is what
// config.toml would hold under [plugins.discord-rpc], with "nil" for a plugin
// that was never configured.
func runPlugin(t *testing.T, transport string) []pluginRecord {
	t.Helper()
	return runPluginScenario(t, transport, "")
}

// runPluginScenario runs the same driver over one of its extra scenarios, which
// the default fixture does not carry. An empty scenario runs the default
// fixture, which is what every other test reads.
func runPluginScenario(t *testing.T, transport, scenario string) []pluginRecord {
	t.Helper()
	driver, err := filepath.Abs(filepath.Join("testdata", "plugin_driver.lua"))
	if err != nil {
		t.Fatal(err)
	}
	plugin, err := filepath.Abs(filepath.Join("..", "..", "..", "discord-rpc.lua"))
	if err != nil {
		t.Fatal(err)
	}

	arguments := []string{driver, plugin, transport}
	if scenario != "" {
		arguments = append(arguments, scenario)
	}

	command := exec.Command(luaRuntime(t), arguments...)
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
		case "message":
			seconds, err := strconv.Atoi(fields[2])
			if err != nil {
				t.Fatalf("the driver produced a non-numeric duration: %q", line)
			}
			record.Text, record.Duration = fields[1], seconds
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

// An unconfigured plugin publishes over IPC, the transport that shipped before
// the state file, writing no documents. It publishes one retained snapshot per
// event — app.start, track.change, app.quit — and retention is what lets the
// daemon's startup display show the track it missed. The heartbeat is the file
// transport's alone: over IPC the daemon's own subscription is the liveness
// signal.
func TestPluginPublishesOverIPCWhenNothingIsConfigured(t *testing.T) {
	records := runPlugin(t, "nil")

	if writes := recordsOfKind(records, "write"); len(writes) != 0 {
		t.Fatalf("an unconfigured plugin wrote %d documents; IPC is the default", len(writes))
	}
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

// A configured plugin writes the document the daemon reads, produced the way
// the plugin produces it: app.start, a track change carrying one field, one
// heartbeat, and a quit, so one document per event plus one for the heartbeat.
//
// The track change carries only the new title, so the fields it leaves out have
// to come from the player; a document built only from the event would reach
// Discord as a track with no artist and no position. The heartbeat must not
// move updated_at, because the daemon interpolates the playhead from it and a
// beat that moved the time would re-anchor the progress bar on a position that
// had not moved — yet the beat still carries the whole track, because the
// daemon reads the last document rather than remembering the earlier ones.
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

	changed := inspectWritten(t, writes[1].Body)
	if changed.State.Title != "Second Track" {
		t.Errorf("the document written on the track change = %+v", changed.State)
	}
	if changed.State.Artist != "Artist" || changed.State.Position != 61 {
		t.Errorf("the track change dropped what the event left out: %+v", changed.State)
	}

	beat := inspectWritten(t, writes[2].Body)
	if beat.State.ObservedAt != changed.State.ObservedAt {
		t.Errorf("a heartbeat moved the change time from %d to %d", changed.State.ObservedAt, beat.State.ObservedAt)
	}
	if beat.Age >= changed.Age {
		t.Errorf("the heartbeat did not advance: %v then %v", changed.Age, beat.Age)
	}
	if beat.State.Title != changed.State.Title || beat.State.Position != changed.State.Position {
		t.Errorf("the heartbeat wrote a partial document: %+v", beat.State)
	}

	if stopped := inspectWritten(t, writes[3].Body); stopped.State.IsPlaying() {
		t.Errorf("the document written on quit still reads as playing: %+v", stopped.State)
	}
}

// The plugin names the track it just handed over, so which track is on the
// Discord card can be read in Cliamp rather than only in Discord. What it names
// is what it sent, not what Discord is showing: nothing here reads the daemon
// back, so a card Discord refused, or one it skipped as unchanged, still reads
// as sent.
//
// Two announcements and not four comes from the dedupe latch, not from the call
// sites: the fixture's heartbeat repeats the track it beat for and its quit
// repeats the track it cleared, so both are the same line twice in a row and
// both are swallowed. The status rule is held apart in
// TestPluginAnnouncesNothingForAStoppedCard, where no earlier line exists to
// repeat.
func TestPluginAnnouncesTheTrackItPublished(t *testing.T) {
	for _, transport := range []string{"nil", "file"} {
		t.Run(transport, func(t *testing.T) {
			messages := recordsOfKind(runPlugin(t, transport), "message")
			if len(messages) != 2 {
				t.Fatalf("announced %d times, want one per published track:\n%v", len(messages), messages)
			}
			want := []string{
				"Broadcasting to Discord: Artist - Track",
				"Broadcasting to Discord: Artist - Second Track",
			}
			for i, message := range messages {
				if message.Text != want[i] {
					t.Errorf("announcement %d = %q, want %q", i, message.Text, want[i])
				}
				if message.Duration != 10 {
					t.Errorf("announcement %d lasts %ds, want the 10s the Last.fm plugin uses", i, message.Duration)
				}
			}
		})
	}
}

// A local file and a stream both report no artist, and a name built by
// concatenation would read "Artist - " with the first half missing — a track
// named by its separator. The title alone is what there is to say.
//
// The scenario adds its track change to the fixture rather than replacing the
// fixture's own, so three tracks are published here and the artistless one is
// the last.
func TestPluginAnnouncesAnArtistlessTrackByTitleAlone(t *testing.T) {
	messages := recordsOfKind(runPluginScenario(t, "nil", "no-artist"), "message")
	if len(messages) != 3 {
		t.Fatalf("announced %d times, want one per published track:\n%v", len(messages), messages)
	}
	if want := "Broadcasting to Discord: Second Track"; messages[2].Text != want {
		t.Errorf("the artistless announcement = %q, want %q", messages[2].Text, want)
	}
}

// Cliamp fires a seek for every step of a dragged progress bar, and each one
// re-sends a track that has not changed, so announcing per event would be one
// flash per step. The same text is announced once.
//
// The latch that does it cannot wedge the way the status document's did: the
// text is rebuilt from the current track every time, so the next track always
// clears it, where a record of having spoken outlived the thing it described.
func TestPluginAnnouncesOnceWhileScrubbing(t *testing.T) {
	messages := recordsOfKind(runPluginScenario(t, "nil", "seek"), "message")
	if len(messages) != 2 {
		t.Fatalf("two seeks inside one track produced %d announcements, want only the track's own:\n%v", len(messages), messages)
	}
	if want := "Broadcasting to Discord: Artist - Second Track"; messages[1].Text != want {
		t.Errorf("the announcement after the seeks = %q, want %q", messages[1].Text, want)
	}
}

// A Cliamp that starts with nothing playing publishes a stopped snapshot, whose
// card is being cleared rather than showing a track. The latch cannot hide a
// mistake here: nothing has been announced yet, so a line spoken for this
// snapshot would be the first line of the run.
//
// This is the only place the status rule is observable. Everywhere else a
// stopped snapshot repeats the line the latch already holds — the quit clears
// the same track it was showing — and is swallowed whether or not the rule is
// there.
func TestPluginAnnouncesNothingForAStoppedCard(t *testing.T) {
	messages := recordsOfKind(runPluginScenario(t, "nil", "stopped-start"), "message")
	if len(messages) != 1 {
		t.Fatalf("announced %d times, want only the track that followed the cleared card:\n%v", len(messages), messages)
	}
	if want := "Broadcasting to Discord: Artist - Second Track"; messages[0].Text != want {
		t.Errorf("the announcement = %q, want %q", messages[0].Text, want)
	}
}
