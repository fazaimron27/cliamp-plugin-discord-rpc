// Package daemon coordinates CLIamp state, artwork lookup, and Discord IPC.
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
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	var lastRevision string
	var publishedKey string
	var publishedAt time.Time
	var lastState playback.State
	var haveState bool

	update := func() {
		current, err := playback.Read(cfg.StatePath)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				log.Printf("read state: %v", err)
			}
			return
		}
		if revision := current.Revision(); !haveState || revision != lastRevision {
			lastState = current
			lastRevision = revision
			haveState = true
		}

		currentTime := now()
		image := ""
		if lastState.IsPlaying(cfg.StateMaxAge, currentTime) {
			image, err = resolver.Resolve(ctx, lastState.Artist, lastState.Title)
			if err != nil {
				log.Printf("resolve Last.fm artwork: %v", err)
			}
		}
		desiredKey := lastState.PresenceKey(cfg.StateMaxAge, currentTime) + "\x00" + image
		if desiredKey == publishedKey && client.Connected() && currentTime.Sub(publishedAt) < presenceRefresh {
			return
		}
		if err := client.Connect(ctx); err != nil {
			return
		}

		if lastState.IsPlaying(cfg.StateMaxAge, currentTime) {
			activity := presence.Build(lastState, presence.Options{LargeImage: cfg.LargeImage, LargeText: cfg.LargeText}, image, currentTime)
			err = client.SetActivity(activity)
		} else {
			err = client.ClearActivity()
		}
		if err != nil {
			log.Printf("update Discord presence: %v", err)
			_ = client.Close()
			return
		}
		publishedKey = desiredKey
		publishedAt = currentTime
	}

	update()
	for {
		select {
		case <-ctx.Done():
			if client.Connected() {
				_ = client.ClearActivity()
			}
			return nil
		case <-ticker.C:
			update()
		}
	}
}
