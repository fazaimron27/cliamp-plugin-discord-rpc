package daemon

// This file drives the run loop: the version watch that warns once per
// mismatched plugin pairing, the timeline tracker that keeps a track's
// started-at anchor stable across snapshots, and the artwork lookup that must
// not stall a track change or a stop behind it. Its fakes — a mutex-guarded
// Discord client, a Cliamp IPC session, and artwork resolvers a test can gate
// or count — live here because those tests share them.

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
	clearCalls int
}

func (f *fakeDiscord) Connected() bool               { return true }
func (f *fakeDiscord) Connect(context.Context) error { return nil }
func (f *fakeDiscord) Close() error                  { return nil }

func (f *fakeDiscord) SetActivity(activity *presence.Activity) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activities = append(f.activities, *activity)
	return nil
}

func (f *fakeDiscord) ClearActivity() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clearCalls++
	return nil
}

// snapshot returns the published activities, oldest first. A copy, because the
// daemon appends to the real slice from its own goroutine.
func (f *fakeDiscord) snapshot() []presence.Activity {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]presence.Activity(nil), f.activities...)
}

func (f *fakeDiscord) published() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.activities)
}

func (f *fakeDiscord) cleared() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clearCalls
}

// waitFor polls until the condition holds. The daemon publishes from its own
// goroutine, so every observation of it has to wait rather than assert.
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
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

// A plugin on an older release line must produce exactly one warning naming
// both versions, and the daemon must keep publishing: "warn, keep running" is
// the chosen behavior, so the activity still reaches Discord. The warning is
// asserted as the sentence version.Explain words, because the run loop and the
// --check report share one wording rather than each wording it privately.
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
	go func() {
		_ = run(ctx, config.Config{CliampSocket: socket}, client, noArtwork{}, time.Now, presenceRefresh)
	}()

	expected := "discord-rpc " + version.Explain(version.PluginBehind, "1.4.0", version.Number)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(logs.String(), expected) {
		time.Sleep(5 * time.Millisecond)
	}
	output := logs.String()
	if !strings.Contains(output, "subscribed to Cliamp playback events") {
		t.Fatalf("daemon never subscribed:\n%s", output)
	}
	if !strings.Contains(output, expected) {
		t.Fatalf("mismatched plugin version produced no warning:\n%s", output)
	}
	if count := strings.Count(output, expected); count != 1 {
		t.Fatalf("warning logged %d times, want 1:\n%s", count, output)
	}
	if client.published() == 0 {
		t.Fatal("daemon stopped publishing after the mismatch")
	}
}

// The version watch warns once for a mismatched plugin version, names both
// versions so the user can act on it, stays quiet when the same version is
// observed again, and warns afresh for a different plugin version because that
// is a new pairing.
func TestVersionWatchReportsMismatchOnce(t *testing.T) {
	var watch versionWatch
	warning := watch.observe("1.4.0")
	if warning == "" {
		t.Fatal("mismatched plugin version produced no warning")
	}
	if !strings.Contains(warning, "1.4.0") || !strings.Contains(warning, version.Number) {
		t.Fatalf("warning does not name both versions: %q", warning)
	}
	if repeat := watch.observe("1.4.0"); repeat != "" {
		t.Fatalf("same plugin version warned twice: %q", repeat)
	}
	if upgrade := watch.observe("1.5.0"); upgrade == "" {
		t.Fatal("new plugin version produced no warning")
	}
}

// The plugin reports whatever its manifest spells, which may carry the release
// tag's leading "v" and may carry surrounding space. Both spellings normalize to
// the same version, so they render the same warning — and a warning that renders
// identically is the duplicate the watch exists to suppress.
func TestVersionWatchReportsAnEquivalentVersionOnce(t *testing.T) {
	older := olderLine(t)
	var watch versionWatch
	if warning := watch.observe(older); warning == "" {
		t.Fatal("older plugin line produced no warning")
	}
	for _, equivalent := range []string{"v" + older, " " + older + " "} {
		if repeat := watch.observe(equivalent); repeat != "" {
			t.Fatalf("observe(%q) repeated the warning for %q: %q", equivalent, older, repeat)
		}
	}
}

// A plugin on the daemon's own release line, one carrying a pre-release
// suffix, one that omits the version field, and one that sends junk must all
// stay quiet. The compatible versions are derived from the daemon's own release
// rather than hardcoded, so a version bump cannot leave this fixture asserting
// a pairing whose sides have since moved apart.
func TestVersionWatchStaysQuietForCompatiblePlugins(t *testing.T) {
	compatible := []string{version.Number, version.Number + "-dev.1", "", "dev"}
	for _, pluginVersion := range compatible {
		var watch versionWatch
		if warning := watch.observe(pluginVersion); warning != "" {
			t.Fatalf("observe(%q) = %q, want no warning", pluginVersion, warning)
		}
	}
}

// An older plugin line warns, names both versions, and points the user at the
// plugin update rather than the daemon: when the plugin is the half that is
// behind, sending the user at the daemon is the wrong direction.
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
	if strings.Contains(warning, "install.sh") {
		t.Fatalf("warning points at the daemon, but the plugin is behind: %q", warning)
	}
}

// A newer plugin line warns and names the daemon as the half that is behind,
// pointing at the install script for the newer release line and at the
// from-source path. It must never tell the user to replace the plugin when the
// plugin is the newer half, which is the bug this direction fixes.
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
	if strings.Contains(warning, "cliamp plugins install") {
		t.Fatalf("warning points at the plugin, but the daemon is behind: %q", warning)
	}
}

// A plugin version carrying the release tag's leading "v" or surrounding space
// must not malform the install URL in the daemon-behind advice: every spelling
// of the same line yields the same versioned install.sh path.
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

// Every spelling of a reported plugin version — bare, v-prefixed, and
// space-padded — renders the right relation sentence without doubling the v
// prefix, so the same release is described the same way however the plugin
// spelled it.
func TestVersionWatchRendersReportedVersionCleanly(t *testing.T) {
	newer, older := newerLine(t), olderLine(t)
	tests := []struct {
		plugin   string
		expected string
	}{
		{newer, "plugin v" + newer + " is newer than daemon"},
		{"v" + newer, "plugin v" + newer + " is newer than daemon"},
		{" " + newer + " ", "plugin v" + newer + " is newer than daemon"},
		{"v" + older, "plugin v" + older + " is older than daemon v" + version.Number},
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

// A track playing straight through keeps its started-at anchor across
// snapshots, a forward jump past the continuity tolerance re-anchors it as a
// seek, a pause clears the anchor, and a resume anchors afresh — the rules that
// keep Discord's progress bar honest.
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

// A snapshot carrying no observation time of its own is stamped with the
// tracker's clock, so a state from a source that omits the timestamp still gets
// a stable anchor instead of a zero one.
func TestTimelineTrackerUsesObservationFallback(t *testing.T) {
	tracker := timelineTracker{nowUnix: func() int64 { return 1000 }}
	state := tracker.Accept(playback.State{Status: "playing", Title: "Track", Duration: 100, Position: 25})
	if state.ObservedAt != 1000 || state.StartedAt != 975 {
		t.Fatalf("state = %#v", state)
	}
}

// cliampSession is a Cliamp IPC peer a test can publish snapshots through. The
// daemon subscribes to it exactly as it subscribes to the real plugin, and stays
// subscribed for as long as the test runs.
type cliampSession struct {
	snapshots chan map[string]any
	done      chan struct{}
}

// serveCliampSession starts a Cliamp IPC peer on socket and returns the session
// a test publishes snapshots through. Its snapshot channel is buffered, with
// room for more than these tests send, because the daemon reads the stream from
// its own goroutine while a test may be holding the loop up.
func serveCliampSession(t *testing.T, socket string) *cliampSession {
	t.Helper()
	session := &cliampSession{
		snapshots: make(chan map[string]any, 16),
		done:      make(chan struct{}),
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		close(session.done)
		_ = listener.Close()
	})
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
		for {
			select {
			case <-session.done:
				return
			case snapshot := <-session.snapshots:
				event, err := json.Marshal(map[string]any{
					"event": cliampipc.PlaybackTopic,
					"time":  1000,
					"data":  snapshot,
				})
				if err != nil {
					return
				}
				if _, err := conn.Write(append(event, '\n')); err != nil {
					return
				}
			}
		}
	}()
	return session
}

// publish sends a snapshot to the daemon as the plugin would.
func (s *cliampSession) publish(t *testing.T, snapshot map[string]any) {
	t.Helper()
	select {
	case s.snapshots <- snapshot:
	case <-time.After(2 * time.Second):
		t.Fatal("the daemon never accepted a snapshot")
	}
}

// playingSnapshot and stoppedSnapshot are the two events these tests publish.
// The version is the daemon's own so nothing but the playback state varies.
func playingSnapshot(title string) map[string]any {
	return map[string]any{
		"status": "playing", "title": title, "artist": "Artist",
		"duration": 200, "position": 10, "plugin_version": version.Number,
	}
}

func stoppedSnapshot(title string) map[string]any {
	return map[string]any{
		"status": "stopped", "title": title, "artist": "Artist",
		"duration": 200, "position": 10, "plugin_version": version.Number,
	}
}

// gatedArtwork holds every lookup until the test releases it, so a test can
// observe what the daemon does while a request is still in flight. Releasing
// answers with the supplied image.
type gatedArtwork struct {
	entered chan struct{}
	release chan string
}

func newGatedArtwork() *gatedArtwork {
	return &gatedArtwork{entered: make(chan struct{}, 8), release: make(chan string, 8)}
}

func (g *gatedArtwork) Resolve(ctx context.Context, _, _ string) (string, error) {
	select {
	case g.entered <- struct{}{}:
	default:
	}
	select {
	case image := <-g.release:
		return image, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// awaitLookup fails the test unless a lookup starts, which is what makes the
// rest of the test meaningful: there is only a stall to observe once a request
// is open.
func (g *gatedArtwork) awaitLookup(t *testing.T) {
	t.Helper()
	select {
	case <-g.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the daemon never asked Last.fm for artwork")
	}
}

// startDaemon runs the loop against a session and a resolver, and returns the
// Discord fake it publishes to. Production passes presenceRefresh; a test that
// needs the loop's timer to fire sooner passes its own interval.
func startDaemon(t *testing.T, socket string, resolver artworkResolver, refresh time.Duration) *fakeDiscord {
	t.Helper()
	client := &fakeDiscord{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = run(ctx, config.Config{CliampSocket: socket}, client, resolver, time.Now, refresh)
	}()
	return client
}

// countingArtwork answers every lookup with the same image, so a test can see how
// many times the loop asked and change what it gets back.
type countingArtwork struct {
	mu       sync.Mutex
	requests int
	image    string
}

func (c *countingArtwork) Resolve(context.Context, string, string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests++
	return c.image, nil
}

func (c *countingArtwork) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requests
}

func (c *countingArtwork) answerWith(image string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.image = image
}

// Artwork is looked up over the network and the loop is the only thing that
// talks to Discord, so a lookup it waits on delays every presence update behind
// it. A track change must not queue behind the artwork of the track it replaced:
// with the lookup for the first track open and never answered, the second track
// reaching Discord proves it was published without waiting for it.
func TestRunPublishesATrackChangeWhileALookupIsInFlight(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "cliamp.sock")
	session := serveCliampSession(t, socket)
	artwork := newGatedArtwork()
	client := startDaemon(t, socket, artwork, presenceRefresh)

	session.publish(t, playingSnapshot("First"))
	artwork.awaitLookup(t)

	session.publish(t, playingSnapshot("Second"))
	waitFor(t, "the second track to reach Discord", func() bool {
		for _, activity := range client.snapshot() {
			if activity.Details == "Second" {
				return true
			}
		}
		return false
	})
}

// A stop has to reach Discord promptly for the same reason: presence that
// lingers after playback ends is wrong for as long as the lookup takes.
func TestRunClearsPresenceWhileALookupIsInFlight(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "cliamp.sock")
	session := serveCliampSession(t, socket)
	artwork := newGatedArtwork()
	client := startDaemon(t, socket, artwork, presenceRefresh)

	session.publish(t, playingSnapshot("First"))
	artwork.awaitLookup(t)

	session.publish(t, stoppedSnapshot("First"))
	waitFor(t, "the presence to clear", func() bool { return client.cleared() > 0 })
}

// The loop publishes as soon as it knows the track, then again when the artwork
// lands. Showing the track immediately is the point of the change: the artwork
// is an enhancement, and waiting for it is what stalled everything else. The
// first activity therefore carries no artwork — nothing has answered the lookup
// yet — and the second carries the image the resolver releases.
func TestRunRepublishesPresenceWhenTheArtworkArrives(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "cliamp.sock")
	session := serveCliampSession(t, socket)
	artwork := newGatedArtwork()
	client := startDaemon(t, socket, artwork, presenceRefresh)

	session.publish(t, playingSnapshot("Track"))
	waitFor(t, "the track to reach Discord", func() bool { return len(client.snapshot()) > 0 })

	if activity := client.snapshot()[0]; activity.Assets != nil {
		t.Fatalf("presence carried artwork before the lookup answered: %#v", activity.Assets)
	}

	const image = "https://img/large.jpg"
	artwork.release <- image
	waitFor(t, "the artwork to reach Discord", func() bool {
		for _, activity := range client.snapshot() {
			if activity.Assets != nil && activity.Assets.LargeImage == image {
				return true
			}
		}
		return false
	})
}

// A lookup that comes back with nothing is not final. Artwork is an enhancement,
// so one that is missing costs the track its picture rather than the session, and
// the retry is what lets a track that failed once recover while it is still
// playing — which is what the loop did before the lookup moved off it.
func TestRunRetriesAnEmptyLookupOnTheNextRefresh(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "cliamp.sock")
	session := serveCliampSession(t, socket)
	artwork := &countingArtwork{}
	client := startDaemon(t, socket, artwork, 50*time.Millisecond)

	session.publish(t, playingSnapshot("Track"))
	waitFor(t, "the empty lookup to be retried", func() bool { return artwork.count() >= 2 })

	artwork.answerWith("https://img/large.jpg")
	waitFor(t, "the retry's artwork to reach Discord", func() bool {
		for _, activity := range client.snapshot() {
			if activity.Assets != nil && activity.Assets.LargeImage == "https://img/large.jpg" {
				return true
			}
		}
		return false
	})
}

// An answer must not be able to ask for itself: retrying from the reply would
// re-ask as fast as the resolver answers, which for a hit on a cached miss is a
// busy loop rather than a retry. Only the timer may retry, so with the timer set
// an hour out nothing here can legitimately ask twice.
func TestRunDoesNotAskAgainBecauseALookupCameBackEmpty(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "cliamp.sock")
	session := serveCliampSession(t, socket)
	artwork := &countingArtwork{}
	client := startDaemon(t, socket, artwork, time.Hour)

	session.publish(t, playingSnapshot("Track"))
	waitFor(t, "the track to reach Discord", func() bool { return len(client.snapshot()) > 0 })

	if asked := artwork.count(); asked != 1 {
		t.Fatalf("lookups = %d, want 1", asked)
	}
	time.Sleep(200 * time.Millisecond)
	if asked := artwork.count(); asked != 1 {
		t.Fatalf("lookups = %d after settling, want 1: an answer asked for itself", asked)
	}
}
