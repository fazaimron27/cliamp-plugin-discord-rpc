package daemon

// This file exercises the --check diagnostic on the file transport: a live
// document, a missing one, one this daemon cannot read, one past its heartbeat
// window, the plugin relation read from the document, a transport override the
// plugin cannot see, and the probe list the report promises.

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/config"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/version"
)

// stateFileConfig is a diagnostic configured for the file transport, with no
// socket: a report that needs one is a report that ignores the transport. The
// Last.fm key is set so the report's only remaining warning is the one a test is
// looking for, an absent key warning by design.
func stateFileConfig(path string) config.Config {
	return config.Config{
		ApplicationID:   config.DefaultApplicationID,
		Transport:       config.TransportFile,
		TransportSource: config.SourceFile,
		StatePath:       path,
		StateMaxAge:     time.Minute,
		LastFMAPIKey:    "configured-key",
	}
}

// runCheckReport runs the diagnostic and returns its exit code with the report.
func runCheckReport(t *testing.T, cfg config.Config) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := check(context.Background(), cfg, newFakeDiscord(), fakeValidator{}, &out)
	return code, out.String()
}

// A live state document passes the check. The plugin relation comes from the
// document itself — there is no subscription to read a retained snapshot from —
// and it is asserted as the sentence version.Explain words, which pins which
// relation the document produced rather than only that some plugin line
// appeared.
func TestCheckPassesForALiveStateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rpc-state.json")
	writeStateDocument(t, path, map[string]any{"title": "Playing", "plugin_version": version.Number})

	code, report := runCheckReport(t, stateFileConfig(path))

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\n%s", code, report)
	}
	if strings.Contains(report, "fail") {
		t.Errorf("a live document was reported as a failure:\n%s", report)
	}
	if want := version.Explain(version.Same, version.Number, version.Number); !strings.Contains(report, want) {
		t.Errorf("report does not give the document's plugin relation as %q:\n%s", want, report)
	}
	if !strings.Contains(report, path) {
		t.Errorf("report does not name the document it read:\n%s", report)
	}
}

// With no document at all the check exits 1 and names the path it looked for,
// which is the file transport's counterpart to a socket that is not there.
func TestCheckFailsWhenTheStateFileIsMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rpc-state.json")

	code, report := runCheckReport(t, stateFileConfig(path))

	if code != 1 {
		t.Fatalf("exit code = %d, want 1\n%s", code, report)
	}
	if !strings.Contains(report, "cliamp") || !strings.Contains(report, "fail") {
		t.Fatalf("report does not name the Cliamp failure:\n%s", report)
	}
	if !strings.Contains(report, path) {
		t.Fatalf("report does not name the document it looked for:\n%s", report)
	}
}

// The document this daemon cannot read is the failure the run loop cannot
// explain: it stays silent, and only reading the document says why.
func TestCheckExplainsADocumentItCannotRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rpc-state.json")
	writeStateDocument(t, path, map[string]any{"v": 2})

	code, report := runCheckReport(t, stateFileConfig(path))

	if code != 1 {
		t.Fatalf("exit code = %d, want 1\n%s", code, report)
	}
	if !strings.Contains(report, "schema") {
		t.Fatalf("report does not say why the document was unusable:\n%s", report)
	}
}

// A document whose heartbeat is older than the configured window fails the
// check and names the window it missed. A document a crash left behind is not a
// working transport, and saying so is the point: the run loop would be showing
// nothing.
func TestCheckFailsForADocumentPastTheWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rpc-state.json")
	old := time.Now().Add(-time.Hour).Unix()
	writeStateDocument(t, path, map[string]any{"heartbeat": old, "updated_at": old})

	code, report := runCheckReport(t, stateFileConfig(path))

	if code != 1 {
		t.Fatalf("exit code = %d, want 1\n%s", code, report)
	}
	if !strings.Contains(report, "1m") {
		t.Fatalf("report does not name the window the document missed:\n%s", report)
	}
}

// A document reporting an older plugin line warns rather than fails: the daemon
// can still run, which is the same rule the run loop follows.
func TestCheckReportsThePluginRelationFromTheDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rpc-state.json")
	writeStateDocument(t, path, map[string]any{"plugin_version": olderLine(t)})

	code, report := runCheckReport(t, stateFileConfig(path))

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\n%s", code, report)
	}
	if !strings.Contains(report, "plugin is the half that is behind") {
		t.Fatalf("report does not say which half is behind:\n%s", report)
	}
}

// An override the plugin cannot see warns rather than fails, naming both
// transports: the daemon is doing what it was told, but a mismatch looks
// exactly like a broken Cliamp from the outside, the daemon reading one source
// while the plugin writes the other.
func TestCheckNamesATransportOverrideThePluginCannotSee(t *testing.T) {
	socket := serveCheckCliamp(t, version.Number)
	cfg := config.Config{
		ApplicationID:     config.DefaultApplicationID,
		CliampSocket:      socket,
		Transport:         config.TransportIPC,
		TransportSource:   config.SourceFlag,
		TransportFromFile: config.TransportFile,
	}

	code, report := runCheckReport(t, cfg)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\n%s", code, report)
	}
	if !strings.Contains(report, "warn") {
		t.Fatalf("report does not warn about the override:\n%s", report)
	}
	for _, half := range []string{config.TransportIPC, config.TransportFile} {
		if !strings.Contains(report, half) {
			t.Errorf("report does not name %q:\n%s", half, report)
		}
	}
}

// When the two halves agree the report names the transport in use and marks the
// line ok, not merely present: a warning here is the whole thing this report
// exists to distinguish.
func TestCheckReportsTheTransportWhenTheHalvesAgree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rpc-state.json")
	writeStateDocument(t, path, map[string]any{})

	code, report := runCheckReport(t, stateFileConfig(path))

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\n%s", code, report)
	}
	if !strings.Contains(report, "transport") || !strings.Contains(report, config.TransportFile) {
		t.Fatalf("report does not name the transport in use:\n%s", report)
	}
	if !strings.Contains(report, "transport ok") {
		t.Fatalf("agreeing halves did not report an ok transport:\n%s", report)
	}
}

// The probe list is the contract of the report: a run that names fewer probes
// than this is a run that silently stopped testing something.
func TestCheckReportsEveryProbeForTheFileTransport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rpc-state.json")
	writeStateDocument(t, path, map[string]any{})

	_, report := runCheckReport(t, stateFileConfig(path))

	for _, probe := range []string{"cliamp", "plugin", "discord", "last.fm", "transport", "config"} {
		if !strings.Contains(report, probe) {
			t.Errorf("report omits the %s probe:\n%s", probe, report)
		}
	}
}
