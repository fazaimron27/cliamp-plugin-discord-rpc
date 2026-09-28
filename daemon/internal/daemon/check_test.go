package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/config"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/presence"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/version"
)

// fakeValidator stands in for the Last.fm resolver. Validation is one request
// with a pass/fail answer, so an error field is the whole fake.
type fakeValidator struct{ err error }

func (f fakeValidator) Validate(context.Context) error { return f.err }

// unreachableDiscord fails to connect, standing in for a desktop session with
// no Discord client running.
type unreachableDiscord struct{}

func (unreachableDiscord) Connected() bool { return false }
func (unreachableDiscord) Connect(context.Context) error {
	return errors.New("Discord IPC unavailable: no Discord IPC socket candidates")
}
func (unreachableDiscord) SetActivity(*presence.Activity) error { return nil }
func (unreachableDiscord) ClearActivity() error                 { return nil }
func (unreachableDiscord) Close() error                         { return nil }

// newerLine and olderLine derive an adjacent release line from the daemon's own
// instead of hardcoding one, so a version bump cannot leave a fixture asserting
// a pairing that is no longer adjacent. The version-watch fixtures in
// daemon_test.go rely on the same derivation.
func newerLine(t *testing.T) string {
	t.Helper()
	return adjacentLine(t, 1)
}

func olderLine(t *testing.T) string {
	t.Helper()
	return adjacentLine(t, -1)
}

func adjacentLine(t *testing.T, delta int) string {
	t.Helper()
	parts := strings.SplitN(version.Number, ".", 3)
	if len(parts) < 2 {
		t.Fatalf("version.Number %q is not major.minor", version.Number)
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		t.Fatalf("version.Number %q has a non-numeric minor: %v", version.Number, err)
	}
	return fmt.Sprintf("%s.%d.0", parts[0], minor+delta)
}

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

func TestCheckPassesInAHealthyEnvironment(t *testing.T) {
	socket := serveCheckCliamp(t, version.Number)
	cfg := config.Config{
		ApplicationID: config.DefaultApplicationID,
		CliampSocket:  socket,
		LastFMAPIKey:  "configured-key",
	}

	var out bytes.Buffer
	code := check(context.Background(), cfg, &fakeDiscord{}, fakeValidator{}, &out)

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
	check(context.Background(), cfg, &fakeDiscord{}, fakeValidator{}, &out)

	if strings.Contains(out.String(), config.DefaultApplicationID) {
		t.Fatalf("report exposes the full application ID:\n%s", out.String())
	}
}

func TestCheckFailsWhenDiscordIsUnavailable(t *testing.T) {
	socket := serveCheckCliamp(t, version.Number)
	cfg := config.Config{ApplicationID: config.DefaultApplicationID, CliampSocket: socket}

	var out bytes.Buffer
	code := check(context.Background(), cfg, unreachableDiscord{}, fakeValidator{}, &out)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1\n%s", code, out.String())
	}
	report := out.String()
	if !strings.Contains(report, "discord") || !strings.Contains(report, "fail") {
		t.Fatalf("report does not name the Discord failure:\n%s", report)
	}
	// A transport failure must not mask the half that worked.
	if !strings.Contains(report, "cliamp") || !strings.Contains(report, "ok") {
		t.Fatalf("report lost the successful Cliamp probe:\n%s", report)
	}
}

func TestCheckFailsWhenCliampIsUnreachable(t *testing.T) {
	cfg := config.Config{
		ApplicationID: config.DefaultApplicationID,
		CliampSocket:  filepath.Join(t.TempDir(), "absent.sock"),
	}

	var out bytes.Buffer
	code := check(context.Background(), cfg, &fakeDiscord{}, fakeValidator{}, &out)

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
	code := check(context.Background(), cfg, &fakeDiscord{}, fakeValidator{err: errors.New("Last.fm rejected the API key: Invalid API key (code 10)")}, &out)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\n%s", code, out.String())
	}
	report := out.String()
	if !strings.Contains(report, "warn") || !strings.Contains(report, "API key") {
		t.Fatalf("report does not warn about the rejected key:\n%s", report)
	}
}

func TestCheckWarnsWhenNoLastFMKeyIsConfigured(t *testing.T) {
	socket := serveCheckCliamp(t, version.Number)
	cfg := config.Config{ApplicationID: config.DefaultApplicationID, CliampSocket: socket}

	var out bytes.Buffer
	code := check(context.Background(), cfg, &fakeDiscord{}, fakeValidator{err: errors.New("must not be called")}, &out)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "warn") {
		t.Fatalf("report does not warn about the disabled artwork:\n%s", out.String())
	}
}

func TestCheckNamesTheStaleHalfOnVersionSkew(t *testing.T) {
	tests := []struct {
		name           string
		pluginVersion  string
		expectedPhrase string
	}{
		// The relation is computed from the daemon's own line, so these hold at
		// any release rather than encoding today's version.
		{"plugin behind", olderLine(t), "the plugin is the half that is behind"},
		{"daemon behind", newerLine(t), "the daemon is the half that is behind"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			socket := serveCheckCliamp(t, test.pluginVersion)
			cfg := config.Config{ApplicationID: config.DefaultApplicationID, CliampSocket: socket}

			var out bytes.Buffer
			code := check(context.Background(), cfg, &fakeDiscord{}, fakeValidator{}, &out)

			// Version skew warns, exactly as the running daemon does.
			if code != 0 {
				t.Fatalf("exit code = %d, want 0\n%s", code, out.String())
			}
			if !strings.Contains(out.String(), test.expectedPhrase) {
				t.Fatalf("report does not say %q:\n%s", test.expectedPhrase, out.String())
			}
		})
	}
}

func TestCheckStaysQuietOnAMatchingPluginLine(t *testing.T) {
	socket := serveCheckCliamp(t, version.Number+"-dev.1")
	cfg := config.Config{ApplicationID: config.DefaultApplicationID, CliampSocket: socket}

	var out bytes.Buffer
	code := check(context.Background(), cfg, &fakeDiscord{}, fakeValidator{}, &out)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "matches this daemon") {
		t.Fatalf("a matching release line was not reported as a match:\n%s", out.String())
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
		name string
		path string
		want string
		// Running as root defeats the mode bits, so the premise of the
		// unreadable case is checked rather than assumed.
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
			// A config the daemon cannot read is still not a hard failure: the
			// built-in defaults are a working configuration, so the exit code
			// must stay 0 while the line warns.
			if code := check(context.Background(), cfg, &fakeDiscord{}, fakeValidator{}, &out); code != 0 {
				t.Fatalf("exit code = %d, want 0\n%s", code, out.String())
			}

			line := configLine(t, out.String())
			if !strings.Contains(line, testCase.want) {
				t.Errorf("config probe reported %q, want %s for %s", line, testCase.want, testCase.path)
			}
		})
	}
}
