// Command cliamp-rpcd mirrors Cliamp playback into a Discord Rich Presence
// activity. It is the daemon half of the plugin; the Lua half ships at the
// repository root.
package main

// This file wires the process together. Configuration is read once at startup,
// and the process then either answers and exits (--version, --check) or runs
// until it is interrupted, clearing the activity on its way out.
//
// The explicit stop() before os.Exit in the --check path is load-bearing.
// os.Exit skips deferred calls, so the deferred stop() never runs on that path,
// and deleting the explicit one because the defer looks like it covers the case
// is the exact mistake this paragraph exists to prevent.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/config"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/daemon"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/version"
)

func main() {
	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.ShowVersion {
		fmt.Println(version.Number)
		return
	}
	if cfg.ShowCheck {
		code := daemon.Check(ctx, cfg)
		stop()
		os.Exit(code)
	}

	if err := daemon.Run(ctx, cfg); err != nil {
		log.Fatal(err)
	}
}
