// Package daemon coordinates state notifications, artwork lookup, and Discord IPC.
package daemon

import (
	"context"
	"errors"
	"log"
	"os"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/artwork"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/config"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/discord"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/playback"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/presence"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/statewatch"
)

const (
	presenceRefresh = 15 * time.Second
	readRetryDelay  = 10 * time.Millisecond
)

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

// Run constructs production dependencies and blocks until context cancellation.
func Run(ctx context.Context, cfg config.Config) error {
	log.Printf("starting cliamp-rpcd (state: %s)", cfg.StatePath)
	if cfg.LastFMAPIKey == "" {
		log.Printf("Last.fm artwork disabled: plugins.discord-rpc.lastfm_api_key is empty")
	}
	client := discord.NewClient(cfg.ApplicationID)
	resolver := artwork.NewLastFM(cfg.LastFMAPIKey)
	return run(ctx, cfg, client, resolver, time.Now)
}

func run(ctx context.Context, cfg config.Config, client discordClient, resolver artworkResolver, now func() time.Time) error {
	defer client.Close()

	var watcher *statewatch.Watcher
	openWatcher := func() {
		if watcher != nil {
			return
		}
		candidate, err := statewatch.New(cfg.StatePath)
		if err != nil {
			return
		}
		watcher = candidate
		log.Printf("watching playback state directory")
	}
	openWatcher()

	var lastRevision string
	var publishedKey string
	var publishedAt time.Time
	var lastState playback.State
	var haveState bool
	var reconnectDelay = time.Second

	readState := func() (playback.State, error) {
		var err error
		for _, delay := range []time.Duration{0, readRetryDelay, 25 * time.Millisecond, 50 * time.Millisecond} {
			if delay > 0 {
				time.Sleep(delay)
			}
			state, readErr := playback.Read(cfg.StatePath)
			if readErr == nil {
				return state, nil
			}
			err = readErr
		}
		return playback.State{}, err
	}

	staleTimer := time.NewTimer(time.Hour)
	if !staleTimer.Stop() {
		<-staleTimer.C
	}
	refreshTimer := time.NewTimer(time.Hour)
	if !refreshTimer.Stop() {
		<-refreshTimer.C
	}
	reconnectTimer := time.NewTimer(time.Hour)
	if !reconnectTimer.Stop() {
		<-reconnectTimer.C
	}
	fallbackTimer := time.NewTimer(time.Hour)
	if !fallbackTimer.Stop() {
		<-fallbackTimer.C
	}
	defer staleTimer.Stop()
	defer refreshTimer.Stop()
	defer reconnectTimer.Stop()
	defer fallbackTimer.Stop()

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

	scheduleFallback := func() {
		if watcher == nil {
			reset(fallbackTimer, cfg.PollInterval)
		} else {
			reset(fallbackTimer, 0)
		}
	}

	reconcile := func() {
		currentTime := now()
		if !haveState {
			return
		}
		if !lastState.IsPlaying(cfg.StateMaxAge, currentTime) {
			if publishedKey != "clear" && client.Connected() {
				if err := client.ClearActivity(); err != nil {
					log.Printf("clear Discord presence: %v", err)
					_ = client.Close()
				} else {
					publishedKey = "clear"
					publishedAt = currentTime
				}
			} else if !client.Connected() {
				publishedKey = "clear"
			}
			reset(refreshTimer, 0)
			reset(reconnectTimer, 0)
			reconnectDelay = time.Second
			return
		}

		image := ""
		var err error
		image, err = resolver.Resolve(ctx, lastState.Artist, lastState.Title)
		if err != nil {
			log.Printf("resolve Last.fm artwork: %v", err)
		}
		desiredKey := lastState.PresenceKey(cfg.StateMaxAge, currentTime) + "\x00" + image
		if desiredKey == publishedKey && client.Connected() && currentTime.Sub(publishedAt) < presenceRefresh {
			reset(reconnectTimer, 0)
			reset(refreshTimer, presenceRefresh-currentTime.Sub(publishedAt))
			return
		}
		if err := client.Connect(ctx); err != nil {
			reset(reconnectTimer, reconnectDelay)
			reconnectDelay = min(2*reconnectDelay, 15*time.Second)
			return
		}
		activity := presence.Build(lastState, presence.Options{LargeImage: cfg.LargeImage, LargeText: cfg.LargeText}, image, currentTime)
		if err := client.SetActivity(activity); err != nil {
			log.Printf("update Discord presence: %v", err)
			_ = client.Close()
			reset(reconnectTimer, reconnectDelay)
			reconnectDelay = min(2*reconnectDelay, 15*time.Second)
			return
		}
		publishedKey = desiredKey
		publishedAt = currentTime
		reconnectDelay = time.Second
		reset(reconnectTimer, 0)
		reset(refreshTimer, presenceRefresh)
	}

	accept := func(state playback.State) {
		if haveState && state.Revision() == lastRevision {
			return
		}
		lastState = state
		lastRevision = state.Revision()
		haveState = true
		reconnectDelay = time.Second
		reset(staleTimer, time.Unix(state.Heartbeat, 0).Add(cfg.StateMaxAge).Sub(now()))
		reconcile()
	}

	readAndReconcile := func() {
		state, err := readState()
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				log.Printf("read state: %v", err)
			}
			return
		}
		accept(state)
	}

	readAndReconcile()
	scheduleFallback()
	for {
		var events <-chan struct{}
		var watcherErrors <-chan error
		if watcher != nil {
			events = watcher.Events()
			watcherErrors = watcher.Errors()
		}
		select {
		case <-ctx.Done():
			if client.Connected() {
				_ = client.ClearActivity()
			}
			if watcher != nil {
				_ = watcher.Close()
			}
			return nil
		case _, ok := <-events:
			if !ok {
				if watcher != nil {
					_ = watcher.Close()
					watcher = nil
				}
				scheduleFallback()
				continue
			}
			readAndReconcile()
		case _, ok := <-watcherErrors:
			if !ok {
				if watcher != nil {
					_ = watcher.Close()
					watcher = nil
				}
				scheduleFallback()
				continue
			}
			if watcher != nil {
				_ = watcher.Close()
				watcher = nil
			}
			readAndReconcile()
			scheduleFallback()
			log.Printf("state watcher failed; using fallback polling")
		case <-fallbackTimer.C:
			if watcher == nil {
				openWatcher()
				readAndReconcile()
				scheduleFallback()
			}
		case <-staleTimer.C:
			reconcile()
		case <-refreshTimer.C:
			reconcile()
		case <-reconnectTimer.C:
			reconcile()
		}
	}
}
