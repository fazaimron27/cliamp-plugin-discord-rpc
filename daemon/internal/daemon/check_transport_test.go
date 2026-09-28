package daemon

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
// socket: a report that needs one is a report that ignores the transport.
func stateFileConfig(path string) config.Config {
	return config.Config{
		ApplicationID:   config.DefaultApplicationID,
		Transport:       config.TransportFile,
		TransportSource: config.SourceFile,
		StatePath:       path,
		StateMaxAge:     time.Minute,
		// Set so the report's only remaining warning is the one a test is
		// looking for: an absent key warns by design.
		LastFMAPIKey: "configured-key",
	}
}

// runCheckReport runs the diagnostic and returns its exit code with the report.
func runCheckReport(t *testing.T, cfg config.Config) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := check(context.Background(), cfg, &fakeDiscord{}, fakeValidator{}, &out)
	return code, out.String()
}

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
	// The plugin line has to come from the document here: there is no
	// subscription to read a retained snapshot from. It is asserted as the
	// sentence version.Explain words, so this pins which relation the document
	// produced rather than only that some plugin line appeared at all.
	if want := version.Explain(version.Same, version.Number, version.Number); !strings.Contains(report, want) {
		t.Errorf("report does not give the document's plugin relation as %q:\n%s", want, report)
	}
	if !strings.Contains(report, path) {
		t.Errorf("report does not name the document it read:\n%s", report)
	}
}

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

func TestCheckFailsForADocumentPastTheWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rpc-state.json")
	old := time.Now().Add(-time.Hour).Unix()
	writeStateDocument(t, path, map[string]any{"heartbeat": old, "updated_at": old})

	code, report := runCheckReport(t, stateFileConfig(path))

	// A document left behind by a crash is not a working transport, and saying
	// so is the point: the run loop would be showing nothing.
	if code != 1 {
		t.Fatalf("exit code = %d, want 1\n%s", code, report)
	}
	if !strings.Contains(report, "1m") {
		t.Fatalf("report does not name the window the document missed:\n%s", report)
	}
}

func TestCheckReportsThePluginRelationFromTheDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rpc-state.json")
	writeStateDocument(t, path, map[string]any{"plugin_version": olderLine(t)})

	code, report := runCheckReport(t, stateFileConfig(path))

	// A plugin older than the daemon warns rather than fails: the daemon can
	// still run, which is the same rule the run loop follows.
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\n%s", code, report)
	}
	if !strings.Contains(report, "plugin is the half that is behind") {
		t.Fatalf("report does not say which half is behind:\n%s", report)
	}
}

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

	// The daemon is doing what it was told, so this is a warning. It is here
	// because a mismatch looks exactly like a broken Cliamp from the outside:
	// the daemon reads one source while the plugin writes the other.
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
	// The line has to be ok, not merely present: a warning here is the whole
	// thing this report exists to distinguish.
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
