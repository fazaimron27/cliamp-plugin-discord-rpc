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
	"errors"
	"log"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/artwork"
	cliampipc "github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/cliamp"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/config"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/discord"
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

// errActivityRejected is the client's own rejection type, carrying the detail
// Discord sends for a payload it will not take. The daemon only ever sees this
// value, so the test hands it the real type rather than a look-alike string.
var errActivityRejected = &discord.RejectionError{Detail: `{"code":4000,"message":"Invalid payload"}`}

// errDiscordWrite is a transport failure rather than a refusal: the socket went
// away mid-write, so there is nothing to keep and reconnecting is the answer.
var errDiscordWrite = errors.New("write unix /run/user/1000/discord-ipc-0: broken pipe")

// rejectingDiscord models a Discord that talks and refuses what it is told: the
// socket and the handshake are accepted, every activity is turned down, and so
// is every clear — a clear is a SET_ACTIVITY like any other, so the same rule
// covers both call sites. That distinction is the one the daemon has to act on
// — everything here is reachable and the payload is the problem — so the fake
// records what the daemon does to the *socket* as well as what it sends.
//
// Connects and closes are counted rather than inferred because the wrong
// behaviour is silent: reconnecting on a rejection looks exactly like a
// successful publish from every input the daemon passes in, and only the count
// of teardowns tells them apart. Accepting is a switch rather than a separate
// fake so one test can put an accepted activity between two rejections.
type rejectingDiscord struct {
	mu        sync.Mutex
	connects  int
	closes    int
	sets      int
	clears    int
	accepted  int
	accepting bool
	clearErr  error
	up        bool
}

func (f *rejectingDiscord) Connected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.up
}

func (f *rejectingDiscord) Connect(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connects++
	f.up = true
	return nil
}

func (f *rejectingDiscord) SetActivity(*presence.Activity) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets++
	if f.accepting {
		f.accepted++
		return nil
	}
	return errActivityRejected
}

func (f *rejectingDiscord) ClearActivity() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clears++
	if f.clearErr != nil {
		return f.clearErr
	}
	return errActivityRejected
}

func (f *rejectingDiscord) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes++
	f.up = false
	return nil
}

func (f *rejectingDiscord) clearCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clears
}

// clearFailsWith makes the clear fail for a reason other than Discord refusing
// it, which is the case that must still be answered by reconnecting.
func (f *rejectingDiscord) clearFailsWith(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clearErr = err
}

func (f *rejectingDiscord) attempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sets
}

func (f *rejectingDiscord) teardowns() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closes
}

func (f *rejectingDiscord) acceptedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.accepted
}

// accept lets the next activity through, and refuse turns Discord against the
// payload again, so one test can put an accepted activity between two
// rejections.
func (f *rejectingDiscord) accept() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accepting = true
}

func (f *rejectingDiscord) refuse() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accepting = false
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

func (noArtwork) Resolve(_ context.Context, _ artwork.Request, report func(artwork.TrackInfo, error)) {
	report(artwork.TrackInfo{}, nil)
}

// resolveFully runs a staged resolve to completion and returns the answer it
// finished on, which is what a test asserting the merged result cares about.
// A test that cares about the staging itself collects every report instead.
func resolveFully(ctx context.Context, resolver artworkResolver, request artwork.Request) (artwork.TrackInfo, error) {
	var info artwork.TrackInfo
	var err error
	resolver.Resolve(ctx, request, func(reported artwork.TrackInfo, reportedErr error) {
		info, err = reported, reportedErr
	})
	return info, err
}

// serveCliampEvent performs the v2 handshake and publishes one snapshot carrying
// the supplied plugin version, then holds the stream open until release closes.
func serveCliampEvent(t *testing.T, socket, pluginVersion string, release <-chan struct{}) {
	t.Helper()
	serveCliampStatus(t, socket, pluginVersion, "playing", release)
}

// serveCliampStatus is the same session with the playback status in the
// caller's hands, because the status is what decides whether the daemon
// publishes a card or clears the one it has.
func serveCliampStatus(t *testing.T, socket, pluginVersion, status string, release <-chan struct{}) {
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
				"status":         status,
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

func (g *gatedArtwork) Resolve(ctx context.Context, _ artwork.Request, report func(artwork.TrackInfo, error)) {
	select {
	case g.entered <- struct{}{}:
	default:
	}
	select {
	case image := <-g.release:
		report(artwork.TrackInfo{Image: image}, nil)
	case <-ctx.Done():
		report(artwork.TrackInfo{}, ctx.Err())
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

// stagedArtwork reports the answer that needed no request at once, then holds
// the rest of the merge until the test releases it. It is what makes the
// staging observable from the loop: nothing but the first report ever carries
// the free image, so an activity showing it can only have been published while
// the resolver was still blocked.
type stagedArtwork struct {
	free    artwork.TrackInfo
	release chan artwork.TrackInfo
}

func (s *stagedArtwork) Resolve(ctx context.Context, _ artwork.Request, report func(artwork.TrackInfo, error)) {
	report(s.free, nil)
	select {
	case rest := <-s.release:
		report(rest, nil)
	case <-ctx.Done():
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

func (c *countingArtwork) Resolve(_ context.Context, _ artwork.Request, report func(artwork.TrackInfo, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests++
	report(artwork.TrackInfo{Image: c.image}, nil)
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

// A thumbnail derived from the playback path costs no request, so the card can
// carry it while the lookup for the pages is still open — which is the point of
// reporting the merge in stages. Before that, the resolver spoke once, when
// Last.fm had answered, and the fallback asset stayed on the card for the whole
// round trip.
//
// The loop needed no change for this: it republishes on every answer it is
// handed and discards the ones for a track it has left. This test is what holds
// that property down, since the resolver is a stub here and the staging it
// exercises belongs to the real one.
func TestRunPublishesTheDerivedThumbnailWhileTheLookupIsOpen(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "cliamp.sock")
	session := serveCliampSession(t, socket)
	const (
		thumbnail = "https://i.ytimg.com/vi/dQw4w9WgXcQ/mqdefault.jpg"
		trackURL  = "https://www.last.fm/music/Artist/_/Track"
	)
	lookup := &stagedArtwork{
		free:    artwork.TrackInfo{Image: thumbnail},
		release: make(chan artwork.TrackInfo, 1),
	}
	client := startDaemon(t, socket, lookup, presenceRefresh)

	session.publish(t, playingSnapshot("Track"))
	waitFor(t, "the thumbnail to reach Discord", func() bool {
		for _, activity := range client.snapshot() {
			if activity.Assets != nil && activity.Assets.LargeImage == thumbnail {
				return true
			}
		}
		return false
	})

	lookup.release <- artwork.TrackInfo{Image: thumbnail, TrackURL: trackURL}
	waitFor(t, "the pages to reach Discord", func() bool {
		for _, activity := range client.snapshot() {
			if activity.DetailsURL == trackURL {
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

// A provider path is the whole point of the design: it becomes a link with no
// key and no network, and the artist gets that service's search because no
// artist id exists anywhere in the pipeline to build an exact page from.
func TestLinksForDerivesAProviderLinkAndArtistSearch(t *testing.T) {
	state := playback.State{Title: "Track", Artist: "AC/DC", Path: "spotify:track:4uLU6hMCjMI75M1A2tKUQC"}
	links := linksFor(state, artwork.TrackInfo{})

	if links.Provider != "Spotify" || links.ProviderURL != "https://open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC" {
		t.Errorf("links = %+v; want the Spotify track page", links)
	}
	if links.ArtistSearchURL != "https://open.spotify.com/search/AC%2FDC" {
		t.Errorf("ArtistSearchURL = %q; want the escaped Spotify search", links.ArtistSearchURL)
	}
}

// The guard the provider-link design exists to satisfy. A self-hosted stream's
// path is a live credential and a local track's is a filesystem path, so
// neither may reach the card even in part. linksFor is the one place a path
// becomes a public URL, which is why the assertion is made here rather than on
// the payload builder alone.
func TestLinksForNeverPublishesACredentialFromThePath(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		mustMiss string
	}{
		{"navidrome password hash", "https://music.example.com/rest/stream?id=1&u=faza&t=deadbeef", "deadbeef"},
		{"plex token", "https://plex.example.com/library/parts/9/file.flac?X-Plex-Token=secret-token", "secret-token"},
		{"jellyfin api key", "https://jf.example.com/media/Items/track-1/Download?api_key=new-token", "new-token"},
		{"audiobookshelf token", "https://abs.example.com/api/items/i1/file/1?token=auth-token", "auth-token"},
		{"local filesystem path", "/home/faza/Music/AC-DC/Back in Black.flac", "Back in Black"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			state := playback.State{Status: "playing", Title: "Track", Artist: "Artist", Path: testCase.path, Stream: true}
			links := linksFor(state, artwork.TrackInfo{})
			data, err := json.Marshal(presence.Build(state, presence.Options{}, "https://img/cover.jpg", links, time.Unix(1000, 0)))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), testCase.mustMiss) {
				t.Errorf("payload leaked %q from the path: %s", testCase.mustMiss, data)
			}
			if strings.Contains(string(data), testCase.path) {
				t.Errorf("payload carries the whole path: %s", data)
			}
		})
	}
}

// The card is re-sent only when republishKey changes, so the derived links have
// to be part of it: the provider link comes from the playback path, and
// PresenceKey deliberately excludes the path.
//
// This guards the key rather than the loop, and the distinction is deliberate.
// The loop republishes on the keepalive interval whatever the key says, so a
// missing input is masked in any end-to-end test — one that waits for the card
// to be re-sent will see it re-sent either way. The property asserted here is
// the one that actually matters: two snapshots with the same public identity
// and different links must not share a key.
func TestRepublishKeyCoversTheDerivedLinks(t *testing.T) {
	state := playback.State{Status: "playing", Title: "Track", Artist: "Artist", Duration: 200, StartedAt: 1000}
	base := republishKey(state, artwork.TrackInfo{}, linksFor(state, artwork.TrackInfo{}))

	if republishKey(state, artwork.TrackInfo{Image: "https://img/cover.jpg"}, presence.Links{}) == base {
		t.Error("the artwork does not change the key")
	}
	cases := []struct {
		name  string
		links presence.Links
	}{
		{"provider track page", presence.Links{Provider: "Spotify", ProviderURL: "https://open.spotify.com/track/x"}},
		{"provider artist search", presence.Links{Provider: "Spotify", ArtistSearchURL: "https://open.spotify.com/search/A"}},
		{"Last.fm track page", presence.Links{TrackURL: "https://www.last.fm/music/A/_/T"}},
		{"Last.fm artist page", presence.Links{ArtistURL: "https://www.last.fm/music/A"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if republishKey(state, artwork.TrackInfo{}, testCase.links) == base {
				t.Errorf("a changed %s does not change the republish key", testCase.name)
			}
		})
	}
}

// TestNewResolverDerivesTheThumbnailForAVideoPath pins the wiring: the resolver
// the daemon actually builds must answer from the playback path before it asks
// anyone. The unit tests in artwork drive a Derived the test supplies; this is
// the one that fails if production forgets to supply it at all.
func TestNewResolverDerivesTheThumbnailForAVideoPath(t *testing.T) {
	resolver := newResolver(config.Config{})
	info, err := resolveFully(context.Background(), resolver, artwork.Request{
		Path: "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if want := "https://i.ytimg.com/vi/dQw4w9WgXcQ/mqdefault.jpg"; info.Image != want {
		t.Errorf("Image = %q; want %q", info.Image, want)
	}
}

// TestNewResolverAsksThePlayerOverTheConfiguredSocket is the player tier's
// wiring, driven the way production drives it: the resolver the daemon builds,
// pointed at a socket that answers state.get, must publish the artwork that
// answer carries. Everything between the configuration and the card is real
// here except the socket's owner.
func TestNewResolverAsksThePlayerOverTheConfiguredSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "cliamp.sock")
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
		if _, err := bufio.NewReader(conn).ReadString('\n'); err != nil {
			return
		}
		_, _ = conn.Write([]byte(`{"version":2,"id":"discord-rpc-state","ok":true,"snapshot":{"track":{"path":"spotify:track:abc","album_art_url":"https://i.scdn.co/image/x"}}}` + "\n"))
	}()

	resolver := newResolver(config.Config{CliampSocket: socket})
	info, err := resolveFully(context.Background(), resolver, artwork.Request{Path: "spotify:track:abc"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if info.Image != "https://i.scdn.co/image/x" {
		t.Errorf("Image = %q; want the player's artwork", info.Image)
	}
}

// A refused payload says nothing about the socket. Discord read the request and
// turned down what it said, so the connection is still good, and tearing it down
// throws away a completed handshake that the next attempt has to pay for again.
// The count that matters is teardowns rather than sends: reconnecting and
// republishing is what the old path did, and it is invisible in what the daemon
// sends.
func TestRunLeavesTheConnectionOpenWhenDiscordRejectsAnActivity(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "cliamp.sock")
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	serveCliampEvent(t, socket, version.Number, release)

	client := &rejectingDiscord{up: true}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = run(ctx, config.Config{CliampSocket: socket}, client, noArtwork{}, time.Now, 20*time.Millisecond)
	}()

	waitFor(t, "three rejected activities", func() bool { return client.attempts() >= 3 })
	if teardowns := client.teardowns(); teardowns != 0 {
		t.Fatalf("daemon tore the connection down %d times over %d rejections; a refused payload leaves the socket good",
			teardowns, client.attempts())
	}
}

// The report belongs to the fault rather than to the attempt. A rejected
// activity is re-tried on every refresh, so a line per attempt would bury the
// journal it exists to explain and would do it at the refresh rate for as long
// as the payload stays refused.
func TestRunReportsARejectedActivityOnce(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "cliamp.sock")
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	serveCliampEvent(t, socket, version.Number, release)

	logs := &syncBuffer{}
	previous := log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(previous) })

	client := &rejectingDiscord{up: true}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = run(ctx, config.Config{CliampSocket: socket}, client, noArtwork{}, time.Now, 20*time.Millisecond)
	}()

	waitFor(t, "three rejected activities", func() bool { return client.attempts() >= 3 })

	output := logs.String()
	if !strings.Contains(output, errActivityRejected.Error()) {
		t.Fatalf("a rejected activity produced no report:\n%s", output)
	}
	if count := strings.Count(output, "update Discord presence:"); count != 1 {
		t.Fatalf("rejection reported %d times across %d attempts, want 1:\n%s",
			count, client.attempts(), output)
	}
}

// An accepted activity ends the outage of the payload's own making, so the next
// rejection is a new one and is reported afresh -- the same rule the connect
// report follows, for the same reason: a latch that never cleared would fall
// silent precisely when the daemon began refusing activities again.
func TestRunReportsANewRejectionAfterAnAcceptedActivity(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "cliamp.sock")
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	serveCliampEvent(t, socket, version.Number, release)

	logs := &syncBuffer{}
	previous := log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(previous) })

	client := &rejectingDiscord{up: true}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = run(ctx, config.Config{CliampSocket: socket}, client, noArtwork{}, time.Now, 20*time.Millisecond)
	}()

	waitFor(t, "the first rejection", func() bool {
		return strings.Count(logs.String(), "update Discord presence:") >= 1
	})
	client.accept()
	waitFor(t, "an accepted activity", func() bool { return client.acceptedCount() > 0 })
	client.refuse()
	waitFor(t, "the second rejection to be reported", func() bool {
		return strings.Count(logs.String(), "update Discord presence:") == 2
	})
}

// A rejection is a content failure, so the retry belongs to the refresh rather
// than to a shorter delay of its own: no amount of waiting changes a payload
// Discord has already read and refused, and the refresh is when the loop
// re-examines the card anyway. This is measured rather than asserted
// structurally, because the failure it guards against is a cadence: a
// one-second retry republishes the same refused bytes for as long as the fault
// lasts, and every republish tears down a connection to do it.
func TestRunRetriesARejectedActivityOnTheRefresh(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "cliamp.sock")
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	serveCliampEvent(t, socket, version.Number, release)

	const refresh = 200 * time.Millisecond
	client := &rejectingDiscord{up: true}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	started := time.Now()
	go func() {
		_ = run(ctx, config.Config{CliampSocket: socket}, client, noArtwork{}, time.Now, refresh)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && client.attempts() < 3 {
		time.Sleep(5 * time.Millisecond)
	}
	if client.attempts() < 3 {
		t.Fatalf("daemon made %d activity attempts, want at least 3", client.attempts())
	}
	if elapsed := time.Since(started); elapsed > 700*time.Millisecond {
		t.Fatalf("third activity attempt took %v with a %v refresh; a rejected activity is being retried on a delay of its own",
			elapsed, refresh)
	}
}

// A clear is a SET_ACTIVITY like any other, so a Discord that refuses one has
// refused the payload rather than broken the socket. Pause and stop are what a
// clear reports, which makes this the common case rather than a corner: any
// user whose card will not clear would otherwise pay a teardown for every pause.
func TestRunKeepsTheConnectionWhenDiscordRefusesToClear(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "cliamp.sock")
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	serveCliampStatus(t, socket, version.Number, "stopped", release)

	logs := &syncBuffer{}
	previous := log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(previous) })

	client := &rejectingDiscord{up: true}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = run(ctx, config.Config{CliampSocket: socket}, client, noArtwork{}, time.Now, 20*time.Millisecond)
	}()

	waitFor(t, "the clear to be attempted", func() bool { return client.clearCount() >= 1 })
	if teardowns := client.teardowns(); teardowns != 0 {
		t.Fatalf("daemon tore the connection down %d times over a refused clear; the socket was never broken", teardowns)
	}
	if output := logs.String(); !strings.Contains(output, "clear Discord presence:") {
		t.Fatalf("a refused clear produced no report:\n%s", output)
	}
}

// The rule is about what the failure says, not about clear being special. A
// clear that failed on the socket leaves the connection in doubt exactly as a
// publish does, so it must keep the reconnect it has always had -- otherwise
// "never close on a refusal" would quietly become "never close".
func TestRunClosesTheConnectionWhenClearingFailsForAnotherReason(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "cliamp.sock")
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	serveCliampStatus(t, socket, version.Number, "stopped", release)

	client := &rejectingDiscord{up: true}
	client.clearFailsWith(errDiscordWrite)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = run(ctx, config.Config{CliampSocket: socket}, client, noArtwork{}, time.Now, 20*time.Millisecond)
	}()

	waitFor(t, "the clear to be attempted", func() bool { return client.clearCount() >= 1 })
	waitFor(t, "the broken socket to be dropped", func() bool { return client.teardowns() >= 1 })
}
