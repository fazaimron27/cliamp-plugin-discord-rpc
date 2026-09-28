package config_test

import (
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/config"
)

func TestConfigUsesBuiltInApplicationID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLIAMP_DISCORD_APP_ID", "")
	t.Setenv("CLIAMP_DISCORD_LASTFM_API_KEY", "")

	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ApplicationID != config.DefaultApplicationID {
		t.Fatalf("application ID = %q, want built-in default", cfg.ApplicationID)
	}
	if cfg.LastFMAPIKey != "" {
		t.Fatalf("Last.fm API key = %q, want disabled by default", cfg.LastFMAPIKey)
	}
}

func TestConfigUsesDedicatedPluginSection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLIAMP_DISCORD_APP_ID", "")
	t.Setenv("CLIAMP_DISCORD_LASTFM_API_KEY", "")
	path := filepath.Join(home, "config.toml")
	data := "[plugins.cliamp-lastfm]\napi_key = \"scrobbling-key\"\n\n[plugins.discord-rpc]\napp_id = \"app-id\"\nlastfm_api_key = \"discord-key\"\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load([]string{"--config", path})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ApplicationID != "app-id" || cfg.LastFMAPIKey != "discord-key" {
		t.Fatalf("credentials = %q, %q", cfg.ApplicationID, cfg.LastFMAPIKey)
	}
}

// A quoted value followed by an inline comment used to keep its quote
// characters, because the quoted branch required a quote as the final character
// of the whole value and anything trailing it fell through to the unquoted path.
// An application ID carrying literal quotes reaches the Discord handshake and is
// rejected there, so the user sees a connection error rather than a
// configuration error; for the Last.fm key the same defect is silent, and
// artwork simply never appears.
func TestConfigParsesQuotedValueWithInlineComment(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{"bare quoted", `app_id = "123456789"`, "123456789"},
		{"quoted, spaced comment", `app_id = "123456789" # my app`, "123456789"},
		{"quoted, unspaced comment", `app_id = "123456789"# my app`, "123456789"},
		{"unquoted with comment", `app_id = 123456789 # my app`, "123456789"},
		{"single quoted", `app_id = '123456789'`, "123456789"},
		{"hash inside basic quotes", `app_id = "12#34"`, "12#34"},
		{"hash inside literal quotes", `app_id = '12#34'`, "12#34"},
		{"quoted, empty comment", `app_id = "123456789" #`, "123456789"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("CLIAMP_DISCORD_APP_ID", "")
			t.Setenv("CLIAMP_DISCORD_LASTFM_API_KEY", "")
			path := filepath.Join(home, "config.toml")
			data := "[plugins.discord-rpc]\n" + testCase.line + "\n"
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}

			cfg, err := config.Load([]string{"--config", path})
			if err != nil {
				t.Fatal(err)
			}
			if cfg.ApplicationID != testCase.want {
				t.Errorf("%s parsed as %q, want %q", testCase.line, cfg.ApplicationID, testCase.want)
			}
		})
	}
}

func TestConfigEnvironmentOverridesFileAndBuiltInDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLIAMP_DISCORD_APP_ID", "env-app")
	t.Setenv("CLIAMP_DISCORD_LASTFM_API_KEY", "env-key")
	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ApplicationID != "env-app" || cfg.LastFMAPIKey != "env-key" {
		t.Fatalf("credentials = %q, %q", cfg.ApplicationID, cfg.LastFMAPIKey)
	}
}

func TestConfigCommandLineOverridesEnvironment(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLIAMP_DISCORD_APP_ID", "env-app")

	cfg, err := config.Load([]string{"--app-id", "flag-app"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ApplicationID != "flag-app" {
		t.Fatalf("application ID = %q, want command-line override", cfg.ApplicationID)
	}
}

func TestConfigVersionFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg, err := config.Load([]string{"--version"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ShowVersion {
		t.Fatal("--version did not request the version")
	}
	off, err := config.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if off.ShowVersion {
		t.Fatal("version requested without the flag")
	}
}

func TestConfigCheckFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg, err := config.Load([]string{"--check"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ShowCheck {
		t.Fatal("--check did not request the diagnostic")
	}
	// --check and --version are separate modes: asking for one must not trigger
	// the other, or `--version` would start probing the environment.
	if cfg.ShowVersion {
		t.Fatal("--check also requested the version")
	}
	off, err := config.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if off.ShowCheck || off.ShowVersion {
		t.Fatal("a mode was requested without its flag")
	}
}

func TestConfigHelpUsesDoubleDashOptions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLIAMP_DISCORD_APP_ID", "secret-app-id")

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	originalStderr := os.Stderr
	os.Stderr = writer
	t.Cleanup(func() { os.Stderr = originalStderr })

	_, loadErr := config.Load([]string{"--help"})
	if closeErr := writer.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	output, readErr := io.ReadAll(reader)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !errors.Is(loadErr, flag.ErrHelp) {
		t.Fatalf("error = %v", loadErr)
	}

	help := string(output)
	for _, option := range []string{"--app-id", "--check", "--config", "--large-image", "--large-text", "--max-age", "--socket", "--transport", "--version"} {
		if !strings.Contains(help, option) {
			t.Errorf("help does not contain %q:\n%s", option, help)
		}
	}
	if strings.Contains(help, "\n  -app-id") {
		t.Errorf("help contains single-dash option:\n%s", help)
	}
	if strings.Contains(help, "secret-app-id") {
		t.Errorf("help exposes application ID:\n%s", help)
	}
	// A boolean flag's zero value is not a useful default to print.
	if strings.Contains(help, `(default "false")`) || strings.Contains(help, `(default "true")`) {
		t.Errorf("help shows a boolean default:\n%s", help)
	}
}
