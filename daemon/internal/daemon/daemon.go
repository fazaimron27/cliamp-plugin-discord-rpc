// Package daemon coordinates Cliamp event subscriptions, artwork, and Discord IPC.
package daemon

import (
	"context"
	"log"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/artwork"
	cliampipc "github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/cliamp"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/config"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/discord"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/playback"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/presence"
)

const presenceRefresh = 15 * time.Second

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
	log.Printf("starting cliamp-rpcd (Cliamp IPC: %s)", cfg.CliampSocket)
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
			stream, err := cliampipc.Subscribe(ctx, cfg.CliampSocket)
			if err != nil {
				if ctx.Err() != nil {
					clear()
					return nil
				}
				reset(cliampTimer, reconnectDelay)
				reconnectDelay = min(2*reconnectDelay, 15*time.Second)
				continue
			}
			states = stream
			reconnectDelay = time.Second
			log.Printf("subscribed to Cliamp playback events")
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
