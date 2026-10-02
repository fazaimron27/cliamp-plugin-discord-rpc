// Package daemon coordinates Cliamp event subscriptions, artwork, and Discord IPC.
package daemon

// This file is how the daemon is put together: the production resolver
// newResolver assembles, and Run, which names the source it is about to read and
// hands the loop its dependencies. The loop itself is in run.go.

import (
	"context"
	"log"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/artwork"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/config"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/discord"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/tracklink"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/version"
)

// presenceRefresh is how often Discord is told the same thing again. Discord
// drops an activity that is not re-sent, so a playing track is republished on
// this interval even when nothing about it changed.
const presenceRefresh = 15 * time.Second

// Run constructs production dependencies and blocks until cancellation.
//
// The line it logs at startup names the source it is about to read, which is the
// first thing to check when nothing shows up on Discord.
func Run(ctx context.Context, cfg config.Config) error {
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
	return run(ctx, cfg, discord.NewClient(cfg.ApplicationID), newResolver(cfg), time.Now, presenceRefresh)
}

// newResolver builds the artwork resolver the daemon runs with. It is a
// function rather than a literal inside Run so a test can hold the assembled
// resolver — the wiring is what decides which tiers exist, and a literal inside
// a constructor that dials Discord is a wiring nothing can check.
func newResolver(cfg config.Config) artwork.Resolver {
	return artwork.Resolver{
		Derived: tracklink.Artwork,
		Player:  &artwork.Player{Socket: cfg.CliampSocket},
		LastFM:  artwork.NewLastFM(cfg.LastFMAPIKey),
	}
}
