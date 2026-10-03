package config_test

// This file exercises the configuration surface from outside the package: the
// credentials Load resolves from the built-in default, the dedicated plugin
// section, the environment, and the command line; how a TOML value carrying an
// inline comment is read; and the version, check, and help flags. It is the
// external config_test package, so it sees a caller's view of the API.

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

// With no environment and no config file, Load falls back to the built-in
// application ID and leaves Last.fm scrobbling off.
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

// Credentials come from the dedicated [plugins.discord-rpc] section, and an
// unrelated plugin's section holding a like-named key is not mistaken for it.
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

// With both credentials in the environment and no config file, Load uses the
// environment values rather than the built-in default.
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

// A flag on the command line beats an environment variable set for the same
// setting.
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

// --version requests the version mode, and a load without the flag does not.
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

// --check requests the diagnostic mode, and a load without a flag requests
// neither mode.
//
// --check and --version are separate modes: asking for one must not trigger the
// other, or --version would start probing the environment.
func TestConfigCheckFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg, err := config.Load([]string{"--check"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ShowCheck {
		t.Fatal("--check did not request the diagnostic")
	}
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

// --help writes the option list to stderr, and that list names every option
// with two dashes, hides the application ID, and omits a boolean flag's zero
// value, which is not a useful default to print.
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
	for _, option := range []string{"--app-id", "--check", "--config", "--large-image", "--large-text", "--max-age", "--socket", "--transport", "--update", "--version"} {
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
	if strings.Contains(help, `(default "false")`) || strings.Contains(help, `(default "true")`) {
		t.Errorf("help shows a boolean default:\n%s", help)
	}
}

// A tag is only meaningful with --update, so it is refused rather than ignored: a
// positional argument used to be discarded silently, and a user who typed a
// version would have had nothing installed and no complaint.
func TestLoadRejectsATagWithNoMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, args := range [][]string{{"v1.12.0"}, {"--check", "v1.12.0"}, {"--version", "v1.12.0"}} {
		if _, err := config.Load(args); err == nil {
			t.Fatalf("Load(%v) accepted a tag with no update mode", args)
		}
	}
}

// Installing and answering are different jobs for one process, so a command that
// asks for both is contradictory rather than a precedence question.
func TestLoadRejectsContradictoryModes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, args := range [][]string{
		{"--update", "--check"},
		{"--update", "--version"},
	} {
		if _, err := config.Load(args); err == nil {
			t.Fatalf("Load(%v) accepted contradictory modes", args)
		}
	}
}

// The mode and its tag reach Config, and the tag keeps the spelling it was given:
// normalizing it, and refusing one this command cannot serve, are the update
// path's business rather than the parser's.
func TestLoadAcceptsTheUpdateMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLIAMP_DISCORD_APP_ID", "")
	t.Setenv("CLIAMP_DISCORD_LASTFM_API_KEY", "")
	tests := []struct {
		name string
		args []string
		tag  string
	}{
		{"update with no tag", []string{"--update"}, ""},
		{"update with a tag", []string{"--update", "v1.12.0"}, "v1.12.0"},
		{"update with a bare version", []string{"--update", "1.12.0"}, "1.12.0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg, err := config.Load(test.args)
			if err != nil {
				t.Fatalf("Load(%v): %v", test.args, err)
			}
			if !cfg.ShowUpdate {
				t.Error("ShowUpdate = false, want true")
			}
			if cfg.ReleaseTag != test.tag {
				t.Errorf("ReleaseTag = %q, want %q", cfg.ReleaseTag, test.tag)
			}
		})
	}
}

// A second positional is a mistake rather than a list, and flag parsing stops at
// the first positional, so a flag after the tag is a second argument here. The
// usage text puts the tag last for that reason.
func TestLoadRejectsASecondPositional(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := config.Load([]string{"--update", "v1.12.0", "--check"}); err == nil {
		t.Fatal("Load accepted two positional arguments")
	}
}

// The usage line names the tag, because it is a positional and nothing else says
// the command takes one.
//
// This reads the line through --help rather than by calling the renderer, because
// this file is the external test package and the renderer is unexported: what a
// user sees on stderr is the assertion, and it is the stronger one anyway.
func TestUsageNamesTheTag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

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
	if !strings.Contains(string(output), "[options] [tag]") {
		t.Fatalf("usage = %q, want it to name the tag", output)
	}
}
