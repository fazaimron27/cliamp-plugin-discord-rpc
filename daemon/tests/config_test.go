package tests

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
	for _, option := range []string{"--app-id", "--config", "--large-image", "--large-text", "--socket"} {
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
}
