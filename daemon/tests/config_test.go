package tests

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/config"
)

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

	cfg, err := config.Load([]string{"-config", path})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ApplicationID != "app-id" || cfg.LastFMAPIKey != "discord-key" {
		t.Fatalf("credentials = %q, %q", cfg.ApplicationID, cfg.LastFMAPIKey)
	}
}

func TestConfigEnvironmentOverridesFile(t *testing.T) {
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

func TestConfigRequiresApplicationID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLIAMP_DISCORD_APP_ID", "")
	if _, err := config.Load(nil); err == nil {
		t.Fatal("expected missing application ID error")
	}
}
