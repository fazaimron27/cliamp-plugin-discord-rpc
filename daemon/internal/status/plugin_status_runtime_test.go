package status_test

// This file tests the Lua plugin's half of the connection indicator by running
// it under a real interpreter, which is why it skips where none is on PATH:
// Cliamp's own Lua is not to hand, so the plugin runs under any interpreter
// that is. The text agreements in plugin_status_test.go exist for precisely
// that reason: they hold everywhere, and these hold wherever the plugin can
// really be executed.
//
// The plugin is driven by testdata/status_driver.lua, which stands in for
// Cliamp, puts the status documents the daemon writes in place, and records
// what the plugin said about them.

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

// statusRecord is one thing the plugin did under the driver.
type statusRecord struct {
	Kind   string
	Fields []string
}

// runStatusDriver loads the real discord-rpc.lua under the stub Cliamp in
// testdata and returns what the plugin did, in order. transport is what
// config.toml would hold under [plugins.discord-rpc], with "nil" for a plugin
// that was never configured; api is "full" or "bare", a Cliamp with or without
// the status API.
func runStatusDriver(t *testing.T, transport, api string) []statusRecord {
	t.Helper()
	driver, err := filepath.Abs(filepath.Join("testdata", "status_driver.lua"))
	if err != nil {
		t.Fatal(err)
	}
	plugin, err := filepath.Abs(filepath.Join("..", "..", "..", "discord-rpc.lua"))
	if err != nil {
		t.Fatal(err)
	}

	command := exec.Command(luaRuntime(t), driver, plugin, transport, api)
	var stderr strings.Builder
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("the plugin failed to run: %v\n%s", err, stderr.String())
	}

	var records []statusRecord
	for _, line := range strings.Split(strings.TrimRight(string(output), "\n"), "\n") {
		fields := strings.Split(line, "\t")
		record := statusRecord{Kind: fields[0], Fields: fields[1:]}
		if record.Kind == "error" {
			t.Fatalf("the plugin reported an error: %s", strings.Join(record.Fields, " "))
		}
		records = append(records, record)
	}
	return records
}

// splitAtRestart divides what the plugin did into the Cliamp it started in and
// the one it was loaded into again, which is the boundary the stored
// connection has to survive.
func splitAtRestart(t *testing.T, records []statusRecord) (before, after []statusRecord) {
	t.Helper()
	for index, record := range records {
		if record.Kind == "restart" {
			return records[:index], records[index+1:]
		}
	}
	t.Fatal("the driver never restarted the plugin")
	return nil, nil
}

// messagesOfKind selects what the plugin said, in order.
func messages(records []statusRecord) []string {
	var said []string
	for _, record := range records {
		if record.Kind == "message" {
			said = append(said, strings.Join(record.Fields, " "))
		}
	}
	return said
}

// The plugin says one thing per transition it actually saw: the connection
// landing, the daemon stopping, and the connection coming back. Everything in
// between — a beat that moved without the connection changing, a document the
// decoder refused, a schema it does not know — is not a fact about Discord and
// has to be silent, because a message per beat would be a status line nobody
// could read.
//
// It runs for both transports: the connection is the daemon's, not the
// playback stream's, and a plugin that only watched it over one of them would
// go quiet on the other.
//
// The second half of the assertion is the restart. A Cliamp restart is not a
// connection change, and the connection the plugin stored is the only thing
// that can tell it so: a plugin announcing from a blank memory would say the
// same thing again on every start.
func TestPluginSaysOnlyWhatChangedAboutTheConnection(t *testing.T) {
	want := []string{"Discord connected", "Discord disconnected", "Discord connected"}
	for _, transport := range []string{"nil", "file"} {
		t.Run(transport, func(t *testing.T) {
			before, after := splitAtRestart(t, runStatusDriver(t, transport, "full"))

			if got := messages(before); !equal(got, want) {
				t.Errorf("the plugin said %q, want %q", got, want)
			}
			if got := messages(after); len(got) != 0 {
				t.Errorf("the plugin announced %q into a new Cliamp with an unchanged connection", got)
			}
		})
	}
}

// The status document is an addition, and a Cliamp that predates it has none of
// cliamp.message, cliamp.store or cliamp.fs.read. The plugin has to go on
// publishing playback there: losing presence entirely is a far worse failure
// than losing a status line.
//
// An errored run is already fatal inside the driver helper, so what is left to
// check here is that playback still left the plugin.
func TestPluginPublishesOnACliampWithoutTheStatusAPI(t *testing.T) {
	for _, transport := range []string{"nil", "file"} {
		t.Run(transport, func(t *testing.T) {
			records := runStatusDriver(t, transport, "bare")
			wanted := "publish"
			if transport == "file" {
				wanted = "write"
			}
			for _, record := range records {
				if record.Kind == wanted {
					return
				}
			}
			t.Fatalf("a Cliamp without the status API got no %s out of the plugin at all", wanted)
		})
	}
}

func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
