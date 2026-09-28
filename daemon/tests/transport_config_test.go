package tests

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

func TestConfigTransportDefaultsToIPC(t *testing.T) {
	transportHome(t)

	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	// The default must not change what an existing install does: IPC is what
	// every current user is already running.
	if cfg.Transport != config.TransportIPC {
		t.Fatalf("transport = %q, want %q", cfg.Transport, config.TransportIPC)
	}
}

func TestConfigTransportReadsTheSharedPluginSection(t *testing.T) {
	transportHome(t)
	path := transportConfig(t, "transport = \"file\"\n")

	cfg, err := config.Load([]string{"--config", path})
	if err != nil {
		t.Fatal(err)
	}
	// The plugin reads this same key through p:config(), which is what keeps the
	// two halves from being configured separately.
	if cfg.Transport != config.TransportFile {
		t.Fatalf("transport = %q, want %q", cfg.Transport, config.TransportFile)
	}
}

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

func TestConfigRejectsAnUnknownTransport(t *testing.T) {
	transportHome(t)

	_, err := config.Load([]string{"--transport", "carrier-pigeon"})
	if err == nil {
		t.Fatal("an unknown transport was accepted")
	}
	// The message must name the offending value, because the likeliest cause is
	// a misspelled one. Echoing it also exposes a value that kept its quote
	// characters from an inline comment in config.toml.
	for _, want := range []string{"carrier-pigeon", config.TransportIPC, config.TransportFile} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %q: %v", want, err)
		}
	}
}

func TestConfigStatePathDefaultsToTheLegacyLocation(t *testing.T) {
	home := transportHome(t)

	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	// v1.4.0 wrote here and the plugin still composes the same path from HOME,
	// so the default keeps a legacy install's existing file in play.
	want := filepath.Join(home, ".local", "share", "cliamp", "rpc-state.json")
	if cfg.StatePath != want {
		t.Fatalf("state path = %q, want %q", cfg.StatePath, want)
	}
}

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

	// No flag and no environment variable, unlike the transport: an override
	// this side could only be made where the plugin cannot see it, and the
	// daemon would watch a path nothing writes.
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

// The Lua plugin has no command line and no environment of its own: it can only
// read config.toml through p:config(). So the halves agree unless this daemon
// was overridden away from what the file says, which is what the provenance is
// recorded for.
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
		// Restating the file's own value changes nothing for the plugin, so it
		// must not warn.
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

func TestConfigMaxAgeDefaultsToTheLegacyWindow(t *testing.T) {
	transportHome(t)

	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	// The plugin heartbeats every 15s, so v1.4.0's 45s window tolerates two
	// missed beats before activity is cleared.
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

	// A zero window clears every document on arrival, so the file transport
	// could never report anything.
	if _, err := config.Load([]string{"--max-age", "0"}); err == nil {
		t.Fatal("a zero max age was accepted")
	}
}
