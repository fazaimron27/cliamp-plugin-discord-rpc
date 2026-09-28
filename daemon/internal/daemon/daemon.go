// Package daemon coordinates Cliamp event subscriptions, artwork, and Discord IPC.
package daemon

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/artwork"
	cliampipc "github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/cliamp"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/config"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/discord"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/playback"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/presence"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/statewatch"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/version"
)

// presenceRefresh is how often Discord is told the same thing again. Discord
// drops an activity that is not re-sent, so a playing track is republished on
// this interval even when nothing about it changed.
const presenceRefresh = 15 * time.Second

const (
	repository = "fazaimron27/cliamp-plugin-discord-rpc"
	rawBase    = "https://raw.githubusercontent.com/" + repository + "/"
)

// normalize trims a plugin-reported version and drops any leading "v". The value
// arrives from the plugin, so it may carry the prefix, surrounding space, or
// neither, and it is rendered into log lines as well as URLs.
func normalize(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "v")
}

// tag renders a reported version as a release tag, carrying exactly one leading
// "v".
func tag(value string) string {
	return "v" + normalize(value)
}

// versionWatch reports a plugin/daemon release-line mismatch once per distinct
// plugin version. Repeating it on every snapshot would bury the genuine error
// traffic, and a mismatch is a one-time discovery rather than a per-track event.
type versionWatch struct {
	reported string
}

// observe returns the warning to log for a plugin version, or an empty string
// when the pairing is compatible, the plugin is too old to report a version, or
// this version has already been reported.
//
// The remembered value is the normalized one, because that is what the warning
// is written from: two spellings that render the same line are the same report,
// and deduping on the raw string would print it twice.
func (w *versionWatch) observe(pluginVersion string) string {
	reported := normalize(pluginVersion)
	if reported == "" || reported == w.reported {
		return ""
	}
	w.reported = reported
	relation := version.Relate(reported, version.Number)
	// The sentence is the same one the --check report prints; only the remedy
	// differs, because only this consumer knows when it is running.
	explained := version.Explain(relation, reported, version.Number)
	switch relation {
	case version.PluginBehind:
		return fmt.Sprintf(
			"discord-rpc %s; these release lines use incompatible transports. Install matching halves with: cliamp plugins install %s@v%s",
			explained, repository, version.Number,
		)
	case version.DaemonBehind:
		// Naming the half that is behind matters here: this daemon is usually a
		// source build running ahead of the installed plugin, and pointing that
		// user at the plugin would have them downgrade the half that is current.
		return fmt.Sprintf(
			"discord-rpc %s. Update cliamp-rpcd with: curl -fsSL %s%s/install.sh | sh (or rebuild from source), then restart it.",
			explained, rawBase, tag(reported),
		)
	default:
		return ""
	}
}

type discordClient interface {
	Connected() bool
	Connect(context.Context) error
	SetActivity(*presence.Activity) error
	ClearActivity() error
	Close() error
}

type artworkResolver interface {
	Resolve(context.Context, string, string) (string, error)
}

// artworkResult is a lookup's outcome, tagged with the track it was asked about
// so the loop can discard an answer that arrived after the track changed.
type artworkResult struct {
	track string
	image string
	err   error
}

type timelineTracker struct {
	last    playback.State
	have    bool
	nowUnix func() int64
}

func (t *timelineTracker) Accept(state playback.State) playback.State {
	observed := state.ObservedAt
	if observed <= 0 {
		if t.nowUnix != nil {
			observed = t.nowUnix()
		} else {
			observed = time.Now().Unix()
		}
	}
	state.ObservedAt = observed
	if state.IsPlaying() {
		keepTimeline := t.have && t.last.IsPlaying() && state.TrackKey() == t.last.TrackKey()
		if keepTimeline {
			expected := t.last.Position + max(observed-t.last.ObservedAt, 0)
			delta := state.Position - expected
			if delta < 0 {
				delta = -delta
			}
			keepTimeline = delta <= 2
		}
		if keepTimeline {
			state.StartedAt = t.last.StartedAt
		} else {
			state.StartedAt = observed - min(max(state.Position, 0), state.Duration)
		}
	}
	t.last = state
	t.have = true
	return state
}

// Run constructs production dependencies and blocks until cancellation.
func Run(ctx context.Context, cfg config.Config) error {
	// The startup line names the source the daemon is about to read, which is
	// the first thing to check when nothing shows up on Discord.
	switch cfg.Transport {
	case config.TransportFile:
		log.Printf("starting cliamp-rpcd %s (state file: %s)", version.Number, cfg.StatePath)
	default:
		log.Printf("starting cliamp-rpcd %s (Cliamp IPC: %s)", version.Number, cfg.CliampSocket)
	}
	if warning := cfg.TransportWarning(); warning != "" {
		log.Print(warning)
	}
	if cfg.LastFMAPIKey == "" {
		log.Printf("Last.fm artwork disabled: plugins.discord-rpc.lastfm_api_key is empty")
	}
	return run(ctx, cfg, discord.NewClient(cfg.ApplicationID), artwork.NewLastFM(cfg.LastFMAPIKey), time.Now, presenceRefresh)
}

// run is the daemon's event loop. refresh is a parameter rather than the
// presenceRefresh constant so a test can drive the loop's timed behavior without
// waiting out the real interval; production passes the constant.
func run(ctx context.Context, cfg config.Config, client discordClient, resolver artworkResolver, now func() time.Time, refresh time.Duration) error {
	defer client.Close()

	var states <-chan playback.State
	var lastState playback.State
	var haveState bool
	tracker := timelineTracker{nowUnix: func() int64 { return now().Unix() }}
	var publishedKey string
	var publishedAt time.Time
	reconnectDelay := time.Second
	var watch versionWatch

	// Artwork is looked up by a goroutine and delivered here. A request can take
	// the resolver's whole HTTP timeout, and this loop is the only thing that
	// talks to Discord, so waiting for one inline would queue every pause, stop
	// and track change behind Last.fm.
	resolved := make(chan artworkResult, 1)
	// What the loop knows about the track it is showing. The resolver caches too,
	// but asking it is only cheap when it already has the answer, so the loop asks
	// on a track change and on the refresh rather than on every reconcile.
	var artworkTrack string
	var artworkImage string

	requestArtwork := func(track, artist, title string) {
		go func() {
			image, err := resolver.Resolve(ctx, artist, title)
			select {
			case resolved <- artworkResult{track: track, image: image, err: err}:
			// A send with no reader left would outlive the loop.
			case <-ctx.Done():
			}
		}()
	}

	refreshTimer := time.NewTimer(time.Hour)
	if !refreshTimer.Stop() {
		<-refreshTimer.C
	}
	cliampTimer := time.NewTimer(0)
	defer refreshTimer.Stop()
	defer cliampTimer.Stop()

	reset := func(timer *time.Timer, duration time.Duration) {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		if duration > 0 {
			timer.Reset(duration)
		}
	}

	// The two transports differ in the call that starts reading and in what the
	// log calls it. Everything downstream sees the same channel of snapshots.
	// A Cliamp that cannot be reached at all is a failed subscribe on either
	// one, so the retry below covers both: the file transport fails while the
	// directory its document lives in does not exist yet.
	subscribe := func(ctx context.Context) (<-chan playback.State, error) {
		if cfg.Transport == config.TransportFile {
			return statewatch.Subscribe(ctx, cfg.StatePath, cfg.StateMaxAge)
		}
		return cliampipc.Subscribe(ctx, cfg.CliampSocket)
	}
	subscription := "subscribed to Cliamp playback events"
	if cfg.Transport == config.TransportFile {
		subscription = fmt.Sprintf("watching for Cliamp state in %s", cfg.StatePath)
	}

	clear := func() {
		reset(refreshTimer, 0)
		if client.Connected() && publishedKey != "clear" {
			if err := client.ClearActivity(); err != nil {
				log.Printf("clear Discord presence: %v", err)
				_ = client.Close()
			}
		}
		publishedKey = "clear"
		publishedAt = now()
	}

	reconcile := func() {
		if !haveState || !lastState.IsPlaying() {
			clear()
			return
		}
		track := lastState.TrackKey()
		if track != artworkTrack {
			// A different track. Drop the previous answer rather than showing it,
			// publish with what is known now, and let the reply republish: waiting
			// here is what made a pause or a skip queue behind the network.
			artworkTrack = track
			artworkImage = ""
			requestArtwork(track, lastState.Artist, lastState.Title)
		}
		image := artworkImage
		currentTime := now()
		desiredKey := lastState.PresenceKey() + "\x00" + image
		if desiredKey == publishedKey && client.Connected() && currentTime.Sub(publishedAt) < refresh {
			reset(refreshTimer, refresh-currentTime.Sub(publishedAt))
			return
		}
		if err := client.Connect(ctx); err != nil {
			reset(refreshTimer, time.Second)
			return
		}
		activity := presence.Build(lastState, presence.Options{LargeImage: cfg.LargeImage, LargeText: cfg.LargeText}, image, currentTime)
		if err := client.SetActivity(activity); err != nil {
			log.Printf("update Discord presence: %v", err)
			_ = client.Close()
			reset(refreshTimer, time.Second)
			return
		}
		publishedKey = desiredKey
		publishedAt = currentTime
		reset(refreshTimer, refresh)
	}

	accept := func(state playback.State) {
		if warning := watch.observe(state.PluginVersion); warning != "" {
			log.Print(warning)
		}
		lastState = tracker.Accept(state)
		haveState = true
		reconcile()
	}

	for {
		select {
		case <-ctx.Done():
			clear()
			return nil
		case <-cliampTimer.C:
			stream, err := subscribe(ctx)
			if err != nil {
				if ctx.Err() != nil {
					clear()
					return nil
				}
				log.Printf("subscribe to Cliamp events: %v", err)
				reset(cliampTimer, reconnectDelay)
				reconnectDelay = min(2*reconnectDelay, 15*time.Second)
				continue
			}
			states = stream
			reconnectDelay = time.Second
			log.Print(subscription)
		case state, ok := <-states:
			if !ok {
				states = nil
				haveState = false
				clear()
				reset(cliampTimer, reconnectDelay)
				reconnectDelay = min(2*reconnectDelay, 15*time.Second)
				log.Printf("Cliamp event stream disconnected; reconnecting")
				continue
			}
			accept(state)
		case result := <-resolved:
			if result.track != artworkTrack {
				// The track changed while the lookup was open. Discord is already
				// showing the new one, so this answer is stale.
				continue
			}
			if result.err != nil {
				log.Printf("resolve Last.fm artwork: %v", result.err)
			}
			// An empty answer is remembered for this track rather than retried
			// here: an answer must not be able to ask for itself, or the loop
			// would re-ask on every reply. The resolver's expiry decides when the
			// next attempt is worth making.
			artworkImage = result.image
			reconcile()
		case <-refreshTimer.C:
			// The refresh is the one event no answer can cause, so it is the only
			// place a lookup that came back with nothing may be retried. That is
			// what lets a track recover from a request that failed or ran before
			// Last.fm had the artwork, without asking on every reconcile.
			//
			// artworkTrack is passed as the identity to match the reply against,
			// not as the artist: it names the track the loop is currently showing,
			// which is lastState by the time this fires.
			if artworkImage == "" && lastState.IsPlaying() {
				requestArtwork(artworkTrack, lastState.Artist, lastState.Title)
			}
			reconcile()
		}
	}
}
