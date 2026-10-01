package status_test

// This file holds the agreements between the Lua plugin and the status document
// that a text search can check, since nothing here runs the Lua: the values both
// halves have to compute the same way, and the pace at which the plugin reads
// what the daemon writes.
//
// They matter because each disagreement fails silently. A plugin reading a
// schema the daemon does not write, or a path the daemon does not write to,
// raises no error anywhere: it produces a Cliamp that says nothing about
// Discord, which looks exactly like a Discord that is working.

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/config"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/status"
)

// pluginSource reads the plugin file, which is also a release archive member.
// The repository root is three levels above this package: Go runs a test binary
// with its working directory set to the package directory, and the plugin sits
// at the root rather than in any Go package, so the path is spelled out here
// instead of shared with the other tests that read repository files.
func pluginSource(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "discord-rpc.lua"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// pluginConstant extracts a Lua constant by pattern, failing when the pattern
// stops matching rather than returning a zero value.
func pluginConstant(t *testing.T, pattern, what string) string {
	t.Helper()
	match := regexp.MustCompile(pattern).FindStringSubmatch(pluginSource(t))
	if match == nil {
		t.Fatalf("discord-rpc.lua declares no %s; the pattern no longer matches the file", what)
	}
	return match[1]
}

// Both halves reach the document by composing HOME, and what has to agree is
// the part after it. A daemon writing where the plugin does not read leaves the
// indicator silent with nothing to say why, which is the failure this whole
// channel exists to avoid.
func TestPluginComposesTheSameStatusPathAsTheDaemon(t *testing.T) {
	directory := pluginConstant(t, `local STATE_DIR = \(os\.getenv\("HOME"\) or ""\) \.\. "([^"]+)"`, "STATE_DIR")
	name := pluginConstant(t, `local STATUS_PATH = STATE_DIR \.\. "([^"]+)"`, "STATUS_PATH")

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLIAMP_DISCORD_TRANSPORT", "")
	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimPrefix(cfg.StatusPath, home), directory+name; got != want {
		t.Fatalf("the daemon writes %q under HOME and the plugin reads %q", got, want)
	}
}

// The plugin's schema constant equals the version the daemon writes, so the
// plugin cannot read a document shape it does not know and report a connection
// from fields that have moved.
func TestPluginReadsTheSchemaTheDaemonWrites(t *testing.T) {
	got := pluginConstant(t, `local STATUS_SCHEMA_VERSION = (\d+)`, "STATUS_SCHEMA_VERSION")
	if want := strconv.Itoa(status.SchemaVersion); got != want {
		t.Fatalf("the daemon writes status schema %s and the plugin reads %s, so every document would be ignored", want, got)
	}
}

// The plugin's staleness threshold leaves room for two missed beats, the same
// tolerance the playback document's window gives. A threshold the daemon could
// outrun on a slow write would report a daemon that is running perfectly well
// as one that had stopped.
func TestPluginToleratesMissedBeatsBeforeCallingTheDaemonStopped(t *testing.T) {
	seconds := pluginSeconds(t, `local STATUS_STALE_SECS = (\d+)`, "STATUS_STALE_SECS")
	if seconds <= 0 {
		t.Fatalf("staleness threshold = %ds", seconds)
	}
	if status.Beat*3 > time.Duration(seconds)*time.Second {
		t.Fatalf("a %ds threshold against a %v beat tolerates fewer than two missed beats", seconds, status.Beat)
	}
}

// The plugin has to notice a daemon that stopped, and it can only do that as
// often as it looks. A poll interval longer than the threshold would let the
// disconnection go unreported for a whole interval after it was already known.
func TestPluginPollsWithoutWaitingOutItsOwnThreshold(t *testing.T) {
	poll := pluginSeconds(t, `local STATUS_POLL_SECS = (\d+)`, "STATUS_POLL_SECS")
	stale := pluginSeconds(t, `local STATUS_STALE_SECS = (\d+)`, "STATUS_STALE_SECS")
	if poll <= 0 {
		t.Fatalf("poll interval = %ds", poll)
	}
	if poll > stale {
		t.Fatalf("a %ds poll against a %ds threshold leaves a stopped daemon unreported for up to %ds past the point the document already said so", poll, stale, poll)
	}
}

// The poll is the plugin's whole share of the delay. The daemon writes a change
// the moment it happens, so all that stands between the change and the status
// line is how often the plugin looks, and a status line that arrives seconds
// late reads as a broken one rather than a lagging one. The document is a few
// dozen bytes and stays in the page cache, so looking once a second costs
// nothing worth weighing against that.
func TestPluginReadsTheStatusWithinASecond(t *testing.T) {
	seconds := pluginSeconds(t, `local STATUS_POLL_SECS = (\d+)`, "STATUS_POLL_SECS")
	if seconds > 1 {
		t.Fatalf("poll interval = %ds; a change the daemon has already written would sit unread for that long", seconds)
	}
}

// pluginSeconds reads a Lua constant the driver also needs, failing on one that
// is not a whole number of seconds.
func pluginSeconds(t *testing.T, pattern, what string) int {
	t.Helper()
	seconds, err := strconv.Atoi(pluginConstant(t, pattern, what))
	if err != nil {
		t.Fatalf("%s is not a whole number of seconds: %v", what, err)
	}
	return seconds
}
