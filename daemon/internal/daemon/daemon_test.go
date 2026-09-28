package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	cliampipc "github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/cliamp"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/config"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/playback"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/presence"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/version"
)

// syncBuffer collects log output written from the daemon's goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// fakeDiscord records published activities so a test can assert the daemon kept
// working after a mismatch. Access is mutex-guarded because the daemon runs
// concurrently with the test body under -race.
type fakeDiscord struct {
	mu         sync.Mutex
	activities []presence.Activity
}

func (f *fakeDiscord) Connected() bool               { return true }
func (f *fakeDiscord) Connect(context.Context) error { return nil }
func (f *fakeDiscord) ClearActivity() error          { return nil }
func (f *fakeDiscord) Close() error                  { return nil }

func (f *fakeDiscord) SetActivity(activity *presence.Activity) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activities = append(f.activities, *activity)
	return nil
}

func (f *fakeDiscord) published() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.activities)
}

type noArtwork struct{}

func (noArtwork) Resolve(context.Context, string, string) (string, error) { return "", nil }

// serveCliampEvent performs the v2 handshake and publishes one snapshot carrying
// the supplied plugin version, then holds the stream open until release closes.
func serveCliampEvent(t *testing.T, socket, pluginVersion string, release <-chan struct{}) {
	t.Helper()
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		scanner := bufio.NewScanner(conn)
		if !scanner.Scan() {
			return
		}
		ack, err := json.Marshal(map[string]any{"version": 2, "id": "discord-rpc-subscribe", "ok": true})
		if err != nil {
			return
		}
		if _, err := conn.Write(append(ack, '\n')); err != nil {
			return
		}
		event, err := json.Marshal(map[string]any{
			"event": cliampipc.PlaybackTopic,
			"time":  1000,
			"data": map[string]any{
				"status":         "playing",
				"title":          "Track",
				"artist":         "Artist",
				"duration":       200,
				"position":       10,
				"plugin_version": pluginVersion,
			},
		})
		if err != nil {
			return
		}
		if _, err := conn.Write(append(event, '\n')); err != nil {
			return
		}
		<-release
	}()
}

func TestRunWarnsAndKeepsPublishingOnMismatchedPluginVersion(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "cliamp.sock")
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	serveCliampEvent(t, socket, "1.4.0", release)

	logs := &syncBuffer{}
	previous := log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(previous) })

	client := &fakeDiscord{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = run(ctx, config.Config{CliampSocket: socket}, client, noArtwork{}, time.Now) }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(logs.String(), "does not match daemon") {
		time.Sleep(5 * time.Millisecond)
	}
	output := logs.String()
	if !strings.Contains(output, "subscribed to Cliamp playback events") {
		t.Fatalf("daemon never subscribed:\n%s", output)
	}
	if !strings.Contains(output, "does not match daemon v"+version.Number) {
		t.Fatalf("mismatched plugin version produced no warning:\n%s", output)
	}
	if count := strings.Count(output, "does not match daemon"); count != 1 {
		t.Fatalf("warning logged %d times, want 1:\n%s", count, output)
	}
	// "Warn, keep running" is the chosen behavior: the activity must still publish.
	if client.published() == 0 {
		t.Fatal("daemon stopped publishing after the mismatch")
	}
}

func TestVersionWatchReportsMismatchOnce(t *testing.T) {
	var watch versionWatch
	warning := watch.observe("1.4.0")
	if warning == "" {
		t.Fatal("mismatched plugin version produced no warning")
	}
	// The message has to name both versions, or the user cannot act on it.
	if !strings.Contains(warning, "1.4.0") || !strings.Contains(warning, version.Number) {
		t.Fatalf("warning does not name both versions: %q", warning)
	}
	if repeat := watch.observe("1.4.0"); repeat != "" {
		t.Fatalf("same plugin version warned twice: %q", repeat)
	}
	// A different plugin version is a new pairing, so it must be reported.
	if upgrade := watch.observe("1.5.0"); upgrade == "" {
		t.Fatal("new plugin version produced no warning")
	}
}

func TestVersionWatchStaysQuietForCompatiblePlugins(t *testing.T) {
	// Derived from the daemon's own release rather than hardcoded, so a version
	// bump cannot leave this fixture asserting a stale pairing. A plugin on the
	// same release line, one carrying a pre-release suffix, one that omits the
	// field, and one that sends junk must all stay quiet.
	compatible := []string{version.Number, version.Number + "-dev.1", "", "dev"}
	for _, pluginVersion := range compatible {
		var watch versionWatch
		if warning := watch.observe(pluginVersion); warning != "" {
			t.Fatalf("observe(%q) = %q, want no warning", pluginVersion, warning)
		}
	}
}

func TestVersionWatchReportsPluginBehind(t *testing.T) {
	older := olderLine(t)
	var watch versionWatch
	warning := watch.observe(older)
	if warning == "" {
		t.Fatal("older plugin line produced no warning")
	}
	if !strings.Contains(warning, older) || !strings.Contains(warning, version.Number) {
		t.Fatalf("warning does not name both versions: %q", warning)
	}
	if !strings.Contains(warning, "cliamp plugins install fazaimron27/cliamp-plugin-discord-rpc@v"+version.Number) {
		t.Fatalf("warning does not tell the user to update the plugin: %q", warning)
	}
	// The whole point of the direction: do not send the user at the daemon when
	// the plugin is the half that is behind.
	if strings.Contains(warning, "install.sh") {
		t.Fatalf("warning points at the daemon, but the plugin is behind: %q", warning)
	}
}

func TestVersionWatchReportsDaemonBehind(t *testing.T) {
	newer := newerLine(t)
	var watch versionWatch
	warning := watch.observe(newer)
	if warning == "" {
		t.Fatal("newer plugin line produced no warning")
	}
	if !strings.Contains(warning, newer) || !strings.Contains(warning, version.Number) {
		t.Fatalf("warning does not name both versions: %q", warning)
	}
	if !strings.Contains(warning, "daemon is the half that is behind") {
		t.Fatalf("warning does not name the daemon as behind: %q", warning)
	}
	if !strings.Contains(warning, "raw.githubusercontent.com/fazaimron27/cliamp-plugin-discord-rpc/v"+newer+"/install.sh") {
		t.Fatalf("warning does not point at the daemon update on the newer line: %q", warning)
	}
	if !strings.Contains(warning, "rebuild from source") {
		t.Fatalf("warning omits the from-source path: %q", warning)
	}
	// The bug this direction fixes: never tell the user to replace the plugin
	// when the plugin is the newer half.
	if strings.Contains(warning, "cliamp plugins install") {
		t.Fatalf("warning points at the plugin, but the daemon is behind: %q", warning)
	}
}

func TestVersionWatchNormalizesTagInDaemonAdvice(t *testing.T) {
	newer := newerLine(t)
	for _, pluginVersion := range []string{newer, "v" + newer, " " + newer + " "} {
		var watch versionWatch
		warning := watch.observe(pluginVersion)
		if !strings.Contains(warning, "/v"+newer+"/install.sh") {
			t.Fatalf("observe(%q) produced a malformed tag: %q", pluginVersion, warning)
		}
	}
}

func TestVersionWatchRendersReportedVersionCleanly(t *testing.T) {
	newer, older := newerLine(t), olderLine(t)
	tests := []struct {
		plugin   string
		expected string
	}{
		{newer, "plugin v" + newer + " is newer than daemon"},
		{"v" + newer, "plugin v" + newer + " is newer than daemon"},
		{" " + newer + " ", "plugin v" + newer + " is newer than daemon"},
		{"v" + older, "plugin v" + older + " does not match daemon"},
	}
	for _, test := range tests {
		var watch versionWatch
		warning := watch.observe(test.plugin)
		if !strings.Contains(warning, test.expected) {
			t.Fatalf("observe(%q) renders the reported version badly: %q", test.plugin, warning)
		}
		if strings.Contains(warning, "vv") {
			t.Fatalf("observe(%q) doubled the v prefix: %q", test.plugin, warning)
		}
	}
}

func TestTimelineTrackerPreservesProgressAndDetectsSeek(t *testing.T) {
	tracker := timelineTracker{}
	first := tracker.Accept(playback.State{Status: "playing", Title: "Track", Path: "track", Duration: 200, Position: 10, ObservedAt: 1000})
	if first.StartedAt != 990 {
		t.Fatalf("first StartedAt = %d, want 990", first.StartedAt)
	}
	natural := tracker.Accept(playback.State{Status: "playing", Title: "Track", Path: "track", Duration: 200, Position: 20, ObservedAt: 1010})
	if natural.StartedAt != 990 {
		t.Fatalf("natural StartedAt = %d, want 990", natural.StartedAt)
	}
	seek := tracker.Accept(playback.State{Status: "playing", Title: "Track", Path: "track", Duration: 200, Position: 80, ObservedAt: 1011})
	if seek.StartedAt != 931 {
		t.Fatalf("seek StartedAt = %d, want 931", seek.StartedAt)
	}
	paused := tracker.Accept(playback.State{Status: "paused", Title: "Track", Path: "track", Duration: 200, Position: 80, ObservedAt: 1020})
	if paused.StartedAt != 0 {
		t.Fatalf("paused StartedAt = %d, want 0", paused.StartedAt)
	}
	resumed := tracker.Accept(playback.State{Status: "playing", Title: "Track", Path: "track", Duration: 200, Position: 80, ObservedAt: 1030})
	if resumed.StartedAt != 950 {
		t.Fatalf("resumed StartedAt = %d, want 950", resumed.StartedAt)
	}
}

func TestTimelineTrackerUsesObservationFallback(t *testing.T) {
	tracker := timelineTracker{nowUnix: func() int64 { return 1000 }}
	state := tracker.Accept(playback.State{Status: "playing", Title: "Track", Duration: 100, Position: 25})
	if state.ObservedAt != 1000 || state.StartedAt != 975 {
		t.Fatalf("state = %#v", state)
	}
}
