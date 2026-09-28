package tests

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/config"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/statewatch"
)

// Nothing here runs the Lua, so these are the agreements a text search can
// hold: the values both halves have to compute the same way, and the field
// names the daemon will refuse a document without.
//
// They matter because every one of these disagreements fails silently. A plugin
// writing a schema the daemon does not read, or a path the daemon does not
// watch, produces no error anywhere: it produces a Discord presence that never
// appears.

// pluginSource reads the plugin file, which is also a release archive member.
func pluginSource(t *testing.T) string {
	t.Helper()
	return repoFile(t, "discord-rpc.lua")
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

// The plugin's fallback is the value the daemon compares against when
// config.toml names no transport, so changing one side alone would have the
// halves reading different sources while both look correctly configured.
func TestPluginFallsBackToTheDaemonsDefaultTransport(t *testing.T) {
	got := pluginConstant(t, `local DEFAULT_TRANSPORT = "([^"]+)"`, "DEFAULT_TRANSPORT")
	if got != config.TransportIPC {
		t.Fatalf("plugin default transport = %q, want %q", got, config.TransportIPC)
	}
}

func TestPluginWritesTheSchemaTheDaemonReads(t *testing.T) {
	got := pluginConstant(t, `local SCHEMA_VERSION = (\d+)`, "SCHEMA_VERSION")
	if want := strconv.Itoa(statewatch.SchemaVersion); got != want {
		t.Fatalf("plugin writes schema %s and the daemon reads %s, so every document would be refused", got, want)
	}
}

func TestPluginHeartbeatsWellInsideTheDaemonsWindow(t *testing.T) {
	seconds, err := strconv.Atoi(pluginConstant(t, `local HEARTBEAT_SECS = (\d+)`, "HEARTBEAT_SECS"))
	if err != nil {
		t.Fatal(err)
	}
	if seconds <= 0 {
		t.Fatalf("heartbeat = %ds", seconds)
	}
	// The window has to tolerate two missed beats rather than one: a beat that
	// lands just past the deadline would otherwise clear the presence of a
	// Cliamp that is running perfectly well.
	if config.DefaultStateMaxAge < 3*time.Duration(seconds)*time.Second {
		t.Fatalf("a %v window against a %ds heartbeat tolerates fewer than two missed beats", config.DefaultStateMaxAge, seconds)
	}
}

// Both halves reach the document by composing HOME, and the plugin is given the
// same config.toml the daemon reads, so what has to agree is the part after
// HOME: that is the whole of the state_path default.
func TestPluginComposesTheSameStatePathAsTheDaemon(t *testing.T) {
	directory := pluginConstant(t, `local STATE_DIR = \(os\.getenv\("HOME"\) or ""\) \.\. "([^"]+)"`, "STATE_DIR")
	name := pluginConstant(t, `local STATE_PATH = STATE_DIR \.\. "([^"]+)"`, "STATE_PATH")

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLIAMP_DISCORD_TRANSPORT", "")
	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimPrefix(cfg.StatePath, home), directory+name; got != want {
		t.Fatalf("the daemon watches %q under HOME and the plugin writes %q", got, want)
	}
}

// The daemon refuses a document missing any of these and says so only in a log
// line, which leaves the user with a presence that never appears. A field
// renamed on one side alone has to fail here instead.
func TestPluginWritesEveryFieldTheDaemonRequires(t *testing.T) {
	source := pluginSource(t)
	for _, field := range []string{
		"status", "title", "artist", "album", "path", "year", "duration",
		"position", "stream", "plugin_version", "updated_at", "heartbeat",
	} {
		// The assignment, not the name: a field that only appears in a comment is
		// a field the plugin does not write.
		if !strings.Contains(source, field+" =") {
			t.Errorf("discord-rpc.lua never assigns %s, which the daemon requires", field)
		}
	}
}
