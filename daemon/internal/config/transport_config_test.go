package config_test

// This file covers how the playback transport is chosen and recorded, where the
// state path and its liveness window come from, and whether this daemon and the
// Lua plugin end up reading the same source. The two helpers below isolate each
// load from the ambient environment so a case can only be influenced by its own
// input.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/config"
)

// transportHome isolates a config load from the ambient environment so a test
// can only be influenced by its own input. It returns the home directory the
// defaults are composed from.
func transportHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLIAMP_DISCORD_APP_ID", "")
	t.Setenv("CLIAMP_DISCORD_LASTFM_API_KEY", "")
	t.Setenv("CLIAMP_DISCORD_TRANSPORT", "")
	return home
}

// transportConfig writes a Cliamp config file holding only the plugin section
// under test, and returns its path.
func transportConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[plugins.discord-rpc]\n"+body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// With nothing configured, the transport is IPC. The default must not change
// what an existing install does: IPC is what every current user is already
// running.
func TestConfigTransportDefaultsToIPC(t *testing.T) {
	transportHome(t)

	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Transport != config.TransportIPC {
		t.Fatalf("transport = %q, want %q", cfg.Transport, config.TransportIPC)
	}
}

// A transport key in the shared plugin section is read by the daemon. The
// plugin reads this same key through p:config(), which is what keeps the two
// halves from being configured separately.
func TestConfigTransportReadsTheSharedPluginSection(t *testing.T) {
	transportHome(t)
	path := transportConfig(t, "transport = \"file\"\n")

	cfg, err := config.Load([]string{"--config", path})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Transport != config.TransportFile {
		t.Fatalf("transport = %q, want %q", cfg.Transport, config.TransportFile)
	}
}

// A transport set in config.toml is overridden by the environment, which is in
// turn overridden by the command line.
func TestConfigTransportPrecedence(t *testing.T) {
	transportHome(t)
	path := transportConfig(t, "transport = \"file\"\n")

	t.Run("environment overrides the file", func(t *testing.T) {
		t.Setenv("CLIAMP_DISCORD_TRANSPORT", config.TransportIPC)
		cfg, err := config.Load([]string{"--config", path})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Transport != config.TransportIPC {
			t.Fatalf("transport = %q, want the environment value %q", cfg.Transport, config.TransportIPC)
		}
	})

	t.Run("command line overrides the environment", func(t *testing.T) {
		t.Setenv("CLIAMP_DISCORD_TRANSPORT", config.TransportIPC)
		cfg, err := config.Load([]string{"--config", path, "--transport", config.TransportFile})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Transport != config.TransportFile {
			t.Fatalf("transport = %q, want the command-line value %q", cfg.Transport, config.TransportFile)
		}
	})
}

// A transport naming neither of the two known values is rejected, and the
// error names it. The likeliest cause is a misspelling, and echoing the value
// also exposes one that kept its quote characters from an inline comment.
func TestConfigRejectsAnUnknownTransport(t *testing.T) {
	transportHome(t)

	_, err := config.Load([]string{"--transport", "carrier-pigeon"})
	if err == nil {
		t.Fatal("an unknown transport was accepted")
	}
	for _, want := range []string{"carrier-pigeon", config.TransportIPC, config.TransportFile} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %q: %v", want, err)
		}
	}
}

// With nothing configured, the state path is the legacy location. v1.4.0 wrote
// there and the plugin still composes the same path from HOME, so the default
// keeps a legacy install's existing file in play.
func TestConfigStatePathDefaultsToTheLegacyLocation(t *testing.T) {
	home := transportHome(t)

	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".local", "share", "cliamp", "rpc-state.json")
	if cfg.StatePath != want {
		t.Fatalf("state path = %q, want %q", cfg.StatePath, want)
	}
}

// state_path comes from config.toml when the file names it and otherwise from
// the home default.
//
// There is no flag and no environment variable for it, unlike the transport: an
// override this side could only be made where the plugin cannot see it, and the
// daemon would watch a path nothing writes.
func TestConfigStatePathComesFromTheFileOrTheDefault(t *testing.T) {
	home := transportHome(t)

	t.Run("file overrides the default", func(t *testing.T) {
		path := transportConfig(t, "state_path = \"/tmp/from-toml.json\"\n")
		cfg, err := config.Load([]string{"--config", path})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.StatePath != "/tmp/from-toml.json" {
			t.Fatalf("state path = %q", cfg.StatePath)
		}
	})

	t.Run("home default", func(t *testing.T) {
		cfg, err := config.Load(nil)
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(home, ".local", "share", "cliamp", "rpc-state.json")
		if cfg.StatePath != want {
			t.Fatalf("state path = %q, want %q", cfg.StatePath, want)
		}
	})
}

// The plugin's side of the joint setting is not only what the file says: with
// no key in the file the plugin uses its own default, and comparing against the
// empty string would make restating that default look like a disagreement.
func TestConfigComparesAgainstThePluginsEffectiveValue(t *testing.T) {
	transportHome(t)

	t.Run("the file names neither, and the daemon names the plugin's default", func(t *testing.T) {
		cfg, err := config.Load([]string{"--transport", config.TransportIPC})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.PluginTransport() != config.TransportIPC {
			t.Fatalf("plugin transport = %q, want the plugin's own default", cfg.PluginTransport())
		}
		if cfg.TransportDisagreesWithPlugin() {
			t.Fatalf("an explicit %q was reported as disagreeing with a plugin that defaults to it", config.TransportIPC)
		}
	})

	t.Run("the file names neither, and the daemon asks for the file", func(t *testing.T) {
		cfg, err := config.Load([]string{"--transport", config.TransportFile})
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.TransportDisagreesWithPlugin() {
			t.Fatal("a plugin that defaults to ipc was reported as agreeing with a daemon reading the file")
		}
	})

	t.Run("the file names one", func(t *testing.T) {
		path := transportConfig(t, "transport = \"file\"\n")
		cfg, err := config.Load([]string{"--config", path})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.PluginTransport() != config.TransportFile {
			t.Fatalf("plugin transport = %q, want the file's value", cfg.PluginTransport())
		}
	})
}

// TransportWarning explains a transport the plugin cannot see and stays silent
// when the halves agree.
//
// Naming all three is the point: what this daemon is reading, what the plugin
// will read, and where each came from. A warning that names one side leaves the
// user guessing which to change.
func TestConfigTransportWarningNamesBothHalves(t *testing.T) {
	transportHome(t)
	path := transportConfig(t, "transport = \"file\"\n")

	t.Run("an override the plugin cannot see is explained", func(t *testing.T) {
		cfg, err := config.Load([]string{"--config", path, "--transport", config.TransportIPC})
		if err != nil {
			t.Fatal(err)
		}
		warning := cfg.TransportWarning()
		if warning == "" {
			t.Fatal("no warning for a transport the plugin cannot see")
		}
		for _, want := range []string{cfg.Transport, cfg.TransportSource, cfg.PluginTransport()} {
			if !strings.Contains(warning, want) {
				t.Errorf("warning does not name %q: %s", want, warning)
			}
		}
	})

	t.Run("halves that agree say nothing", func(t *testing.T) {
		cfg, err := config.Load([]string{"--config", path})
		if err != nil {
			t.Fatal(err)
		}
		if warning := cfg.TransportWarning(); warning != "" {
			t.Fatalf("warning = %q, want none when both halves read the file", warning)
		}
		cfg, err = config.Load([]string{"--config", path, "--transport", config.TransportFile})
		if err != nil {
			t.Fatal(err)
		}
		if warning := cfg.TransportWarning(); warning != "" {
			t.Fatalf("warning = %q, want none when the override restates the file", warning)
		}
	})
}

// The Lua plugin has no command line and no environment of its own: it can only
// read config.toml through p:config(). So the halves agree unless this daemon
// was overridden away from what the file says, which is what the provenance is
// recorded for.
//
// TransportSource names the winning source. Restating the file's own value from
// the environment or the command line changes nothing for the plugin, so it is
// recorded without being a disagreement.
func TestConfigRecordsWhereTheTransportCameFrom(t *testing.T) {
	transportHome(t)
	path := transportConfig(t, "transport = \"file\"\n")

	t.Run("default", func(t *testing.T) {
		cfg, err := config.Load(nil)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.TransportSource != config.SourceDefault {
			t.Fatalf("source = %q, want %q", cfg.TransportSource, config.SourceDefault)
		}
		if cfg.TransportDisagreesWithPlugin() {
			t.Fatal("the default was reported as disagreeing with the plugin")
		}
	})

	t.Run("file", func(t *testing.T) {
		cfg, err := config.Load([]string{"--config", path})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.TransportSource != config.SourceFile || cfg.TransportFromFile != config.TransportFile {
			t.Fatalf("source = %q, file value = %q", cfg.TransportSource, cfg.TransportFromFile)
		}
		if cfg.TransportDisagreesWithPlugin() {
			t.Fatal("a value taken from config.toml was reported as disagreeing with the plugin")
		}
	})

	t.Run("environment away from the file", func(t *testing.T) {
		t.Setenv("CLIAMP_DISCORD_TRANSPORT", config.TransportIPC)
		cfg, err := config.Load([]string{"--config", path})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.TransportSource != config.SourceEnvironment || !cfg.TransportDisagreesWithPlugin() {
			t.Fatalf("source = %q, disagrees = %v", cfg.TransportSource, cfg.TransportDisagreesWithPlugin())
		}
	})

	t.Run("environment agreeing with the file", func(t *testing.T) {
		t.Setenv("CLIAMP_DISCORD_TRANSPORT", config.TransportFile)
		cfg, err := config.Load([]string{"--config", path})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.TransportDisagreesWithPlugin() {
			t.Fatalf("source = %q, files = %q, chosen = %q", cfg.TransportSource, cfg.TransportFromFile, cfg.Transport)
		}
	})

	t.Run("command line away from the file", func(t *testing.T) {
		cfg, err := config.Load([]string{"--config", path, "--transport", config.TransportIPC})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.TransportSource != config.SourceFlag || !cfg.TransportDisagreesWithPlugin() {
			t.Fatalf("source = %q, disagrees = %v", cfg.TransportSource, cfg.TransportDisagreesWithPlugin())
		}
	})
}

// With nothing configured, the max age is the legacy window, and the flag
// replaces it.
//
// The plugin heartbeats every 15s, so v1.4.0's 45s window tolerates two missed
// beats before activity is cleared. A zero window clears every document on
// arrival, so the file transport could never report anything.
func TestConfigMaxAgeDefaultsToTheLegacyWindow(t *testing.T) {
	transportHome(t)

	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StateMaxAge != 45*time.Second {
		t.Fatalf("max age = %v, want 45s", cfg.StateMaxAge)
	}

	cfg, err = config.Load([]string{"--max-age", "90s"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StateMaxAge != 90*time.Second {
		t.Fatalf("max age = %v, want the command-line value", cfg.StateMaxAge)
	}

	if _, err := config.Load([]string{"--max-age", "0"}); err == nil {
		t.Fatal("a zero max age was accepted")
	}
}
