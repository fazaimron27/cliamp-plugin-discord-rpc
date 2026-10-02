package daemon

// This file exercises the --check diagnostic: the healthy report, the redacted
// application ID, a failed transport that must not mask the one that worked,
// the Last.fm warnings, the version-skew wording it shares with the run loop,
// and the config-file probe on a readable path, a directory, an unreadable
// file, and a missing one.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/config"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/version"
)

// fakeValidator stands in for the Last.fm resolver. Validation is one request
// with a pass/fail answer, so an error field is the whole fake.
type fakeValidator struct{ err error }

func (f fakeValidator) Validate(context.Context) error { return f.err }

// serveCheckCliamp stands up the v2 handshake harness on a fresh socket and
// returns the socket path. The harness replays one snapshot carrying
// pluginVersion, which is what the version probe reads.
func serveCheckCliamp(t *testing.T, pluginVersion string) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "cliamp.sock")
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	serveCliampEvent(t, socket, pluginVersion, release)
	return socket
}

// A healthy environment — a subscribed Cliamp, a reachable Discord, and a
// configured Last.fm key — exits 0, reports every probe, and names the daemon
// version without reporting anything failed.
func TestCheckPassesInAHealthyEnvironment(t *testing.T) {
	socket := serveCheckCliamp(t, version.Number)
	cfg := config.Config{
		ApplicationID: config.DefaultApplicationID,
		CliampSocket:  socket,
		LastFMAPIKey:  "configured-key",
	}

	var out bytes.Buffer
	code := check(context.Background(), cfg, newFakeDiscord(), fakeValidator{}, &out)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\n%s", code, out.String())
	}
	report := out.String()
	for _, probe := range []string{"cliamp", "plugin", "discord", "last.fm"} {
		if !strings.Contains(report, probe) {
			t.Errorf("report omits the %s probe:\n%s", probe, report)
		}
	}
	if strings.Contains(report, "fail") {
		t.Errorf("healthy environment reported a failure:\n%s", report)
	}
	if !strings.Contains(report, version.Number) {
		t.Errorf("report omits the daemon version:\n%s", report)
	}
}

// The application ID is what the run loop authenticates with. The report has to
// identify it well enough to compare against the Discord Developer Portal
// without printing the whole value, matching --help, which hides it too.
func TestCheckRedactsTheApplicationID(t *testing.T) {
	socket := serveCheckCliamp(t, version.Number)
	cfg := config.Config{
		ApplicationID: config.DefaultApplicationID,
		CliampSocket:  socket,
	}

	var out bytes.Buffer
	check(context.Background(), cfg, newFakeDiscord(), fakeValidator{}, &out)

	if strings.Contains(out.String(), config.DefaultApplicationID) {
		t.Fatalf("report exposes the full application ID:\n%s", out.String())
	}
}

// Discord being unreachable fails the check with the report naming the Discord
// failure, while the Cliamp probe that did succeed stays in the report: one
// failed transport must not mask the half that worked.
func TestCheckFailsWhenDiscordIsUnavailable(t *testing.T) {
	socket := serveCheckCliamp(t, version.Number)
	cfg := config.Config{ApplicationID: config.DefaultApplicationID, CliampSocket: socket}

	var out bytes.Buffer
	code := check(context.Background(), cfg, newFakeDiscord().refuseConnect(errDiscordUnreachable), fakeValidator{}, &out)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1\n%s", code, out.String())
	}
	report := out.String()
	if !strings.Contains(report, "discord") || !strings.Contains(report, "fail") {
		t.Fatalf("report does not name the Discord failure:\n%s", report)
	}
	if !strings.Contains(report, "cliamp") || !strings.Contains(report, "ok") {
		t.Fatalf("report lost the successful Cliamp probe:\n%s", report)
	}
}

// A socket that is not there is the IPC transport's commonest failure, so the
// report has to name Cliamp rather than leaving the user with an exit code.
func TestCheckFailsWhenCliampIsUnreachable(t *testing.T) {
	cfg := config.Config{
		ApplicationID: config.DefaultApplicationID,
		CliampSocket:  filepath.Join(t.TempDir(), "absent.sock"),
	}

	var out bytes.Buffer
	code := check(context.Background(), cfg, newFakeDiscord(), fakeValidator{}, &out)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "cliamp") || !strings.Contains(out.String(), "fail") {
		t.Fatalf("report does not name the Cliamp failure:\n%s", out.String())
	}
}

// Artwork is an optional enhancement, so a rejected key leaves the daemon fully
// functional. Reporting it as a hard failure would call a working setup broken.
func TestCheckWarnsButPassesWhenLastFMKeyIsRejected(t *testing.T) {
	socket := serveCheckCliamp(t, version.Number)
	cfg := config.Config{
		ApplicationID: config.DefaultApplicationID,
		CliampSocket:  socket,
		LastFMAPIKey:  "rejected-key",
	}

	var out bytes.Buffer
	code := check(context.Background(), cfg, newFakeDiscord(), fakeValidator{err: errors.New("Last.fm rejected the API key: Invalid API key (code 10)")}, &out)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\n%s", code, out.String())
	}
	report := out.String()
	if !strings.Contains(report, "warn") || !strings.Contains(report, "API key") {
		t.Fatalf("report does not warn about the rejected key:\n%s", report)
	}
}

// With no Last.fm key configured the validator is never called, and the report
// warns that artwork is disabled rather than failing: artwork is optional, so
// its absence is not a broken setup.
func TestCheckWarnsWhenNoLastFMKeyIsConfigured(t *testing.T) {
	socket := serveCheckCliamp(t, version.Number)
	cfg := config.Config{ApplicationID: config.DefaultApplicationID, CliampSocket: socket}

	var out bytes.Buffer
	code := check(context.Background(), cfg, newFakeDiscord(), fakeValidator{err: errors.New("must not be called")}, &out)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "warn") {
		t.Fatalf("report does not warn about the disabled artwork:\n%s", out.String())
	}
}

// A skewed plugin version names the half that is behind — the plugin or the
// daemon — while still passing, exactly as the running daemon warns rather than
// fails. The versions are derived from the daemon's own line, so these cases
// hold at any release rather than encoding today's version.
func TestCheckNamesTheStaleHalfOnVersionSkew(t *testing.T) {
	tests := []struct {
		name           string
		pluginVersion  string
		expectedPhrase string
	}{
		{"plugin behind", olderLine(t), "the plugin is the half that is behind"},
		{"daemon behind", newerLine(t), "the daemon is the half that is behind"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			socket := serveCheckCliamp(t, test.pluginVersion)
			cfg := config.Config{ApplicationID: config.DefaultApplicationID, CliampSocket: socket}

			var out bytes.Buffer
			code := check(context.Background(), cfg, newFakeDiscord(), fakeValidator{}, &out)

			if code != 0 {
				t.Fatalf("exit code = %d, want 0\n%s", code, out.String())
			}
			if !strings.Contains(out.String(), test.expectedPhrase) {
				t.Fatalf("report does not say %q:\n%s", test.expectedPhrase, out.String())
			}
		})
	}
}

// The diagnostic and the running daemon answer the same question about the same
// snapshot, so they must answer it in the same words. Asserting that both carry
// one sentence is what stops either from growing its own phrasing again — both
// did once, and each rewording had to be discovered by reading the other.
func TestVersionMessagingSpeaksWithOneVoice(t *testing.T) {
	tests := []struct {
		name     string
		relation version.Relation
		plugin   string
	}{
		{"plugin behind", version.PluginBehind, olderLine(t)},
		{"daemon behind", version.DaemonBehind, newerLine(t)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			explained := version.Explain(test.relation, test.plugin, version.Number)

			var watch versionWatch
			if warning := watch.observe(test.plugin); !strings.Contains(warning, explained) {
				t.Errorf("the daemon's warning does not say %q: %q", explained, warning)
			}

			socket := serveCheckCliamp(t, test.plugin)
			cfg := config.Config{ApplicationID: config.DefaultApplicationID, CliampSocket: socket}
			var out bytes.Buffer
			check(context.Background(), cfg, newFakeDiscord(), fakeValidator{}, &out)
			if !strings.Contains(out.String(), explained) {
				t.Errorf("the check report does not say %q:\n%s", explained, out.String())
			}
		})
	}
}

// A plugin on the daemon's own release line — here a pre-release of it — is
// reported as the same line rather than warned about, so a matching install
// does not nag.
func TestCheckStaysQuietOnAMatchingPluginLine(t *testing.T) {
	pluginLine := version.Number + "-dev.1"
	socket := serveCheckCliamp(t, pluginLine)
	cfg := config.Config{ApplicationID: config.DefaultApplicationID, CliampSocket: socket}

	var out bytes.Buffer
	code := check(context.Background(), cfg, newFakeDiscord(), fakeValidator{}, &out)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\n%s", code, out.String())
	}
	if want := version.Explain(version.Same, pluginLine, version.Number); !strings.Contains(out.String(), want) {
		t.Fatalf("a matching release line was not reported as %q:\n%s", want, out.String())
	}
}

// configLine returns the report's config probe line. Assertions go through it so
// that a bare "ok" from another probe cannot satisfy a test about this one.
func configLine(t *testing.T, report string) string {
	t.Helper()
	for _, line := range strings.Split(report, "\n") {
		if strings.HasPrefix(line, "config") {
			return line
		}
	}
	t.Fatalf("report has no config probe line:\n%s", report)
	return ""
}

// The config probe answered "does this path exist", because os.Stat is what it
// called. A directory answers that question successfully while being no config
// file at all, so `--check` reported `config ok` for a path the daemon cannot
// use, and the branch whose message says "is not readable" could only ever fire
// when the path was missing outright.
//
// A config the daemon cannot read is still not a hard failure — the built-in
// defaults are a working configuration — so the exit code stays 0 while the
// line warns. Running as root defeats the mode bits, so the unreadable case's
// premise is checked rather than assumed.
func TestCheckReportsWhetherTheConfigFileIsReadable(t *testing.T) {
	dir := t.TempDir()
	readable := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(readable, []byte("[plugins.discord-rpc]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(dir, "a-directory")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	unreadable := filepath.Join(dir, "unreadable.toml")
	if err := os.WriteFile(unreadable, []byte("[plugins.discord-rpc]\n"), 0o000); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name            string
		path            string
		want            string
		needsUnreadable bool
	}{
		{name: "readable", path: readable, want: "ok"},
		{name: "directory", path: directory, want: "warn"},
		{name: "unreadable", path: unreadable, want: "warn", needsUnreadable: true},
		{name: "missing", path: filepath.Join(dir, "absent.toml"), want: "warn"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.needsUnreadable {
				if _, err := os.ReadFile(testCase.path); err == nil {
					t.Skip("this process can read the file regardless of its mode, so there is nothing to probe")
				}
			}
			socket := serveCheckCliamp(t, version.Number)
			cfg := config.Config{
				ApplicationID: config.DefaultApplicationID,
				CliampSocket:  socket,
				CliampConfig:  testCase.path,
			}

			var out bytes.Buffer
			if code := check(context.Background(), cfg, newFakeDiscord(), fakeValidator{}, &out); code != 0 {
				t.Fatalf("exit code = %d, want 0\n%s", code, out.String())
			}

			line := configLine(t, out.String())
			if !strings.Contains(line, testCase.want) {
				t.Errorf("config probe reported %q, want %s for %s", line, testCase.want, testCase.path)
			}
		})
	}
}
