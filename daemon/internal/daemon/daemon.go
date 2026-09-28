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
func (w *versionWatch) observe(pluginVersion string) string {
	if pluginVersion == "" || pluginVersion == w.reported {
		return ""
	}
	w.reported = pluginVersion
	switch version.Relate(pluginVersion, version.Number) {
	case version.PluginBehind:
		return fmt.Sprintf(
			"discord-rpc plugin v%s does not match daemon v%s; these release lines use incompatible transports. Install matching halves with: cliamp plugins install %s@v%s",
			normalize(pluginVersion), version.Number, repository, version.Number,
		)
	case version.DaemonBehind:
		// Naming the half that is behind matters here: this daemon is usually a
		// source build running ahead of the installed plugin, and pointing that
		// user at the plugin would have them downgrade the half that is current.
		return fmt.Sprintf(
			"discord-rpc plugin v%s is newer than daemon v%s, so the daemon is the half that is behind. Update cliamp-rpcd with: curl -fsSL %s%s/install.sh | sh (or rebuild from source), then restart it.",
			normalize(pluginVersion), version.Number, rawBase, tag(pluginVersion),
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
	return run(ctx, cfg, discord.NewClient(cfg.ApplicationID), artwork.NewLastFM(cfg.LastFMAPIKey), time.Now)
}

func run(ctx context.Context, cfg config.Config, client discordClient, resolver artworkResolver, now func() time.Time) error {
	defer client.Close()

	var states <-chan playback.State
	var lastState playback.State
	var haveState bool
	tracker := timelineTracker{nowUnix: func() int64 { return now().Unix() }}
	var publishedKey string
	var publishedAt time.Time
	reconnectDelay := time.Second
	var watch versionWatch

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
		image, err := resolver.Resolve(ctx, lastState.Artist, lastState.Title)
		if err != nil {
			log.Printf("resolve Last.fm artwork: %v", err)
		}
		currentTime := now()
		desiredKey := lastState.PresenceKey() + "\x00" + image
		if desiredKey == publishedKey && client.Connected() && currentTime.Sub(publishedAt) < presenceRefresh {
			reset(refreshTimer, presenceRefresh-currentTime.Sub(publishedAt))
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
		reset(refreshTimer, presenceRefresh)
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
		case <-refreshTimer.C:
			reconcile()
		}
	}
}
