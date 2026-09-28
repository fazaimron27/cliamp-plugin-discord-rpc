package playback_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/playback"
)

// The plugin is half this project's shipped surface and none of it is Go, so
// nothing has ever checked that what it publishes is what the daemon accepts.
// These tests run the real Lua file against a stubbed Cliamp API and hold the
// result to the daemon's own contract.

// publishedSnapshot is one call the plugin made to p:publish.
type publishedSnapshot struct {
	Topic   string          `json:"topic"`
	Retain  bool            `json:"retain"`
	Payload json.RawMessage `json:"payload"`
}

type pluginRun struct {
	Published []publishedSnapshot `json:"published"`
	Logs      []string            `json:"logs"`
}

// runScenario executes discord-rpc.lua under the stub harness and returns what
// it published. luajit is treated as optional locally and required in CI, where
// CI sets CLIAMP_REQUIRE_LUA so the suite cannot pass by skipping.
func runScenario(t *testing.T, scenario string) pluginRun {
	t.Helper()

	interpreter, err := exec.LookPath("luajit")
	if err != nil {
		if os.Getenv("CLIAMP_REQUIRE_LUA") != "" {
			t.Fatalf("luajit is required but was not found in PATH: %v", err)
		}
		t.Skip("luajit not found; skipping the Lua plugin contract tests")
	}

	var stderr bytes.Buffer
	command := exec.Command(
		interpreter,
		filepath.Join("testdata", "lua", "run.lua"),
		filepath.Join("..", "..", "..", "discord-rpc.lua"),
		scenario,
	)
	command.Stderr = &stderr
	stdout, err := command.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			t.Fatalf("scenario %s exited %d: %s", scenario, exit.ExitCode(), stderr.String())
		}
		t.Fatalf("running scenario %s: %v", scenario, err)
	}

	var run pluginRun
	if err := json.Unmarshal(stdout, &run); err != nil {
		t.Fatalf("decoding scenario %s output %q: %v", scenario, stdout, err)
	}
	return run
}

// stateKeys reports the JSON keys playback.State accepts, which is exactly the
// set the plugin has to publish. Fields tagged `json:"-"` are derived by the
// daemon and are deliberately not part of the wire contract.
func stateKeys(t *testing.T) []string {
	t.Helper()
	stateType := reflect.TypeOf(playback.State{})
	keys := make([]string, 0, stateType.NumField())
	for i := 0; i < stateType.NumField(); i++ {
		tag := stateType.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		keys = append(keys, strings.Split(tag, ",")[0])
	}
	sort.Strings(keys)
	return keys
}

// scenarios that must produce exactly one snapshot. Without this control the
// contract test below would pass vacuously on a plugin that publishes nothing.
func TestPluginPublishesKnownScenarios(t *testing.T) {
	for _, scenario := range []string{"playing", "quit"} {
		t.Run(scenario, func(t *testing.T) {
			run := runScenario(t, scenario)
			if len(run.Published) != 1 {
				t.Fatalf("published %d snapshots, want exactly 1", len(run.Published))
			}
			snapshot := run.Published[0]
			if snapshot.Topic != "playback" {
				t.Errorf("published to topic %q, want %q", snapshot.Topic, "playback")
			}
			if !snapshot.Retain {
				t.Error("published without retain, so the daemon would only see live snapshots")
			}
		})
	}
}

// TestPluginPayloadSatisfiesDaemonContract is the guard the audit asked for: a
// field renamed on either side of the Lua/Go boundary fails here, and a payload
// the daemon would reject never leaves the plugin.
func TestPluginPayloadSatisfiesDaemonContract(t *testing.T) {
	wantKeys := stateKeys(t)
	for _, scenario := range []string{"playing", "state-nil", "all-nil", "quit"} {
		t.Run(scenario, func(t *testing.T) {
			run := runScenario(t, scenario)
			for index, snapshot := range run.Published {
				var state playback.State
				if err := json.Unmarshal(snapshot.Payload, &state); err != nil {
					t.Fatalf("snapshot %d: %v", index, err)
				}
				if err := state.Validate(); err != nil {
					t.Errorf("snapshot %d would be discarded by the daemon: %v\npayload: %s",
						index, err, snapshot.Payload)
				}

				var object map[string]json.RawMessage
				if err := json.Unmarshal(snapshot.Payload, &object); err != nil {
					t.Fatalf("snapshot %d: %v", index, err)
				}
				gotKeys := make([]string, 0, len(object))
				for key := range object {
					gotKeys = append(gotKeys, key)
				}
				sort.Strings(gotKeys)
				if !reflect.DeepEqual(gotKeys, wantKeys) {
					t.Errorf("snapshot %d publishes keys %v, daemon accepts %v",
						index, gotKeys, wantKeys)
				}
			}
		})
	}
}

// TestPluginWithholdsSnapshotWithoutStatus is the regression test for the
// status-less snapshot. A payload with no status is rejected by the daemon, and
// because the publish is retained it replaces the last good snapshot while
// doing so. The plugin must not send one at all.
func TestPluginWithholdsSnapshotWithoutStatus(t *testing.T) {
	for _, scenario := range []string{"state-nil", "all-nil"} {
		t.Run(scenario, func(t *testing.T) {
			run := runScenario(t, scenario)
			if len(run.Published) != 0 {
				t.Errorf("published %d snapshot(s) while the player state was unknown, want none",
					len(run.Published))
			}
			if len(run.Logs) == 0 {
				t.Error("withheld a snapshot without logging anything, so the gap stays invisible")
			}
		})
	}
}
