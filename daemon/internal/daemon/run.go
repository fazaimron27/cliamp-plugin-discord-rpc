package daemon

// This file is how the run loop is entered. Every dependency the loop needs
// arrives here as an argument and is handed straight to the session that owns
// it; the loop's state and behaviour are in session.go.

import (
	"context"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/config"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/diag"
)

// run starts the daemon's event loop and returns when ctx is cancelled.
//
// Nothing here is constructed from anything else: the Discord client, the
// artwork resolver, the clock and both loggers are all things a test replaces,
// and where production gets them is Run's decision. This function exists so that
// decision has exactly one place to be made, and so the arguments have names —
// as the session's fields — rather than being a positional list passed down.
//
// refresh is a parameter rather than the presenceRefresh constant so a test can
// drive the loop's timed behavior without waiting out the real interval;
// production passes the constant.
//
// Two loggers arrive rather than one: the loop's own lines go through logger,
// and the Cliamp subscription's discarded-frame lines through cliampLogger, so
// each reaches the journal already naming the component that produced it.
func run(
	ctx context.Context,
	cfg config.Config,
	client discordClient,
	resolver artworkResolver,
	logger diag.Logger,
	cliampLogger diag.Logger,
	now func() time.Time,
	refresh time.Duration,
) error {
	s := &session{
		cfg:            cfg,
		client:         client,
		resolver:       resolver,
		logger:         logger,
		cliampLogger:   cliampLogger,
		now:            now,
		refresh:        refresh,
		tracker:        timelineTracker{nowUnix: func() int64 { return now().Unix() }},
		reconnectDelay: time.Second,
		resolved:       make(chan artworkResult, 1),
	}
	return s.loop(ctx)
}
