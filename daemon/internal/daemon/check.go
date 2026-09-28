package daemon

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/artwork"
	cliampipc "github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/cliamp"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/config"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/discord"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/playback"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/version"
)

// checkTimeout bounds the whole diagnostic. Every probe is a local socket or a
// single HTTP request, so a slow answer means the environment is broken rather
// than busy.
const checkTimeout = 10 * time.Second

// snapshotWait bounds the wait for Cliamp's retained snapshot. A plugin that has
// published anything replays its newest snapshot immediately; one that never has
// sends nothing at all, so this must not hold up the report.
const snapshotWait = time.Second

// validator is the slice of the artwork resolver a diagnostic needs. The run
// loop never validates a key, so this is deliberately narrower than
// artworkResolver.
type validator interface {
	Validate(context.Context) error
}

// Check probes the runtime environment, writes a report, and returns the process
// exit code: 0 when the daemon could run, 1 when a transport it needs is
// unavailable. Optional features warn rather than fail, so a working setup is
// never reported as broken.
func Check(ctx context.Context, cfg config.Config) int {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	// The report is the output, and the Discord client logs its own connection
	// line with a timestamp that would land in the middle of a deliberately
	// timestamp-free report. Nothing is lost: every outcome it would announce
	// already appears here as an ok/fail line. Check runs once from main,
	// immediately before the process exits, so this global has no other reader.
	previous := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(previous)

	return check(ctx, cfg, discord.NewClient(cfg.ApplicationID), artwork.NewLastFM(cfg.LastFMAPIKey), os.Stdout)
}

func check(ctx context.Context, cfg config.Config, client discordClient, resolver validator, out io.Writer) int {
	code := 0
	line := func(status, probe, detail string) {
		fmt.Fprintf(out, "%-9s %-5s %s\n", probe, status, detail)
	}
	fail := func(probe, detail string) {
		line("fail", probe, detail)
		code = 1
	}

	// The report goes to stdout without timestamps, matching --version: it is a
	// result to read or pipe, not a log.
	fmt.Fprintf(out, "cliamp-rpcd %s\n\n", version.Number)

	// Cliamp: subscribing is the honest probe, because it is the call that
	// exercises the version 2 envelope. A Cliamp predating the cutover is
	// reported here rather than appearing as a silent absence of events.
	states, err := cliampipc.Subscribe(ctx, cfg.CliampSocket)
	if err != nil {
		fail("cliamp", err.Error())
		line("skip", "plugin", "not readable without a Cliamp subscription")
	} else {
		line("ok", "cliamp", fmt.Sprintf("subscribed to %s at %s", cliampipc.PlaybackTopic, cfg.CliampSocket))
		status, detail := pluginVersion(ctx, states)
		line(status, "plugin", detail)
	}

	// Discord: a real handshake, because "the socket exists" and "Discord
	// accepts us" are different answers and only the second one matters.
	if err := client.Connect(ctx); err != nil {
		fail("discord", err.Error())
	} else {
		line("ok", "discord", fmt.Sprintf("handshake completed as application %s", redact(cfg.ApplicationID)))
		_ = client.Close()
	}

	// Artwork is an optional enhancement. A missing or rejected key leaves the
	// daemon fully functional, so neither is a hard failure.
	if cfg.LastFMAPIKey == "" {
		line("warn", "last.fm", "no API key configured, artwork disabled")
	} else if err := resolver.Validate(ctx); err != nil {
		line("warn", "last.fm", err.Error())
	} else {
		line("ok", "last.fm", "the API key was accepted")
	}

	if _, err := os.Stat(cfg.CliampConfig); err == nil {
		line("ok", "config", cfg.CliampConfig)
	} else {
		line("warn", "config", fmt.Sprintf("%s is not readable, using defaults", cfg.CliampConfig))
	}

	return code
}

// pluginVersion reads Cliamp's retained snapshot, when one exists, and reports
// how the plugin's release line compares to this daemon's. The running daemon
// logs the same relation, so this answers the question the warning raises
// without requiring the user to read startup logs.
func pluginVersion(ctx context.Context, states <-chan playback.State) (string, string) {
	timer := time.NewTimer(snapshotWait)
	defer timer.Stop()
	for {
		select {
		case state, ok := <-states:
			if !ok {
				return "warn", "Cliamp closed the subscription before sending a snapshot"
			}
			if state.PluginVersion == "" {
				return "warn", "the plugin published no version, so it predates the version report"
			}
			reported := normalize(state.PluginVersion)
			relation := version.Relate(reported, version.Number)
			// The wording is the daemon's own; this only decides how loudly to
			// report it. Skew warns exactly as the running daemon does, rather
			// than failing a command the user may be gating a start on.
			status := "warn"
			if relation == version.Same {
				status = "ok"
			}
			return status, version.Explain(relation, reported, version.Number)
		case <-timer.C:
			return "warn", "no retained snapshot, so the plugin version is unknown"
		case <-ctx.Done():
			return "warn", "timed out waiting for a snapshot"
		}
	}
}

// redact shortens an application ID to a recognizable prefix. The daemon's own
// --help hides this value, and the report follows the same rule: enough to
// compare against the Discord Developer Portal, not enough to leak.
func redact(value string) string {
	const shown = 8
	if len(value) <= shown {
		return value
	}
	return value[:shown] + "..."
}
