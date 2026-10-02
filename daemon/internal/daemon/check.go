package daemon

// This file is the diagnostic behind cliamp-rpcd --check: it probes the
// environment the daemon runs in and prints one line per probe, so that "nothing
// shows up on Discord" has an answer rather than a guess.
//
// The probes differ in what a bad answer costs. A transport the daemon needs
// cannot be worked around, and fails; everything optional — artwork, and even an
// unreadable config file, since the built-in defaults are a working
// configuration — warns and leaves the exit code alone. A working setup is
// therefore never reported as broken, and a broken one cannot exit 0.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/artwork"
	cliampipc "github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/cliamp"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/config"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/diag"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/discord"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/playback"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/statewatch"
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
//
// The Discord client is silenced: it logs its connection line with a timestamp
// that would land in the middle of a deliberately timestamp-free report, and
// nothing is lost, since every outcome it would announce already appears in the
// report as a line of its own. The discarding logger it is handed is what
// replaces the process-wide redirect this once performed, which had to be put
// back afterwards and would have raced anything else in the process that logs.
func Check(ctx context.Context, cfg config.Config) int {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	return check(ctx, cfg, discord.NewClient(cfg.ApplicationID, diag.Discard()), artwork.NewLastFM(cfg.LastFMAPIKey), os.Stdout)
}

// check is the report itself. Its probes run in a deliberate order: the transport
// comes first because it decides which of the two Cliamp probes below can say
// anything, and because a mismatch in it looks exactly like a broken Cliamp from
// the outside — the daemon reading one source while the plugin writes the other.
//
// The Cliamp probe differs by transport. Over IPC, subscribing is the honest
// probe, because it is the call that exercises the version 2 envelope, and a
// Cliamp predating the cutover is reported here rather than appearing as a silent
// absence of events. The file transport's equivalent is reading the document: it
// is what the run loop does, and it is the only way to tell a Cliamp that has
// stopped from one that never started. The plugin's release is reported from
// whichever of the two the transport can read.
//
// Discord is probed with a real handshake, because "the socket exists" and
// "Discord accepts us" are different answers and only the second one matters.
// Artwork is probed in the knowledge that a missing or rejected key leaves the
// daemon fully functional, so neither outcome is a hard failure.
//
// The config probe asks whether the daemon can use the file, which is not what
// os.Stat answers: a directory stat succeeds while being no config file at all,
// so the probe reported ok for a path that contributes nothing. Reading it is
// what Load does, so reading it is what this reports on. An unusable config is
// still not a hard failure, since the built-in defaults are a working
// configuration, so it warns and the exit code stays 0.
//
// The report goes to stdout without timestamps, matching --version: it is a
// result to read or pipe, not a log.
func check(ctx context.Context, cfg config.Config, client discordClient, resolver validator, out io.Writer) int {
	code := 0
	line := func(status, probe, detail string) {
		fmt.Fprintf(out, "%-9s %-5s %s\n", probe, status, detail)
	}
	fail := func(probe, detail string) {
		line("fail", probe, detail)
		code = 1
	}

	fmt.Fprintf(out, "cliamp-rpcd %s\n\n", version.Number)

	transport := cfg.Transport
	if transport == "" {
		transport = config.TransportIPC
	}
	source := cfg.TransportSource
	if source == "" {
		source = config.SourceDefault
	}
	if warning := cfg.TransportWarning(); warning != "" {
		line("warn", "transport", warning)
	} else {
		line("ok", "transport", fmt.Sprintf("%s, from the %s", transport, source))
	}

	reportPlugin := func(detail statewatch.Detail) {
		if detail.State.PluginVersion == "" {
			line("warn", "plugin", "the state document carries no plugin version, so the plugin predates the version report")
			return
		}
		status, report := relation(detail.State.PluginVersion)
		line(status, "plugin", report)
	}

	if transport == config.TransportFile {
		detail := statewatch.Inspect(cfg.StatePath, cfg.StateMaxAge)
		switch {
		case !detail.Present:
			fail("cliamp", fmt.Sprintf("no state document at %s, so Cliamp is not running or the plugin is not writing one", cfg.StatePath))
			line("skip", "plugin", "not readable without a state document")
		case detail.Problem != nil:
			fail("cliamp", fmt.Sprintf("%s cannot be read: %v", cfg.StatePath, detail.Problem))
			line("skip", "plugin", "not readable without a state document")
		case detail.Lapsed:
			fail("cliamp", fmt.Sprintf("the state document at %s was last written %s ago, past the %s window", cfg.StatePath, detail.Age.Round(time.Second), cfg.StateMaxAge))
			reportPlugin(detail)
		default:
			line("ok", "cliamp", fmt.Sprintf("read %s, last written %s ago", cfg.StatePath, detail.Age.Round(time.Second)))
			reportPlugin(detail)
		}
	} else {
		states, err := cliampipc.Subscribe(ctx, cfg.CliampSocket, diag.Discard())
		if err != nil {
			fail("cliamp", err.Error())
			line("skip", "plugin", "not readable without a Cliamp subscription")
		} else {
			line("ok", "cliamp", fmt.Sprintf("subscribed to %s at %s", cliampipc.PlaybackTopic, cfg.CliampSocket))
			status, detail := pluginVersion(ctx, states)
			line(status, "plugin", detail)
		}
	}

	if err := client.Connect(ctx); err != nil {
		fail("discord", err.Error())
	} else {
		line("ok", "discord", fmt.Sprintf("handshake completed as application %s", redact(cfg.ApplicationID)))
		_ = client.Close()
	}

	if cfg.LastFMAPIKey == "" {
		line("warn", "last.fm", "no API key configured, artwork disabled")
	} else if err := resolver.Validate(ctx); err != nil {
		line("warn", "last.fm", err.Error())
	} else {
		line("ok", "last.fm", "the API key was accepted")
	}

	if _, err := os.ReadFile(cfg.CliampConfig); err != nil {
		var pathError *os.PathError
		if errors.As(err, &pathError) {
			err = pathError.Err
		}
		line("warn", "config", fmt.Sprintf("%s is not readable, using defaults: %v", cfg.CliampConfig, err))
	} else {
		line("ok", "config", cfg.CliampConfig)
	}

	return code
}

// pluginVersion reads Cliamp's retained snapshot, when one exists, and reports
// how the plugin's release line compares to this daemon's. The running daemon
// logs the same relation, so this answers the question the warning raises
// without requiring the user to read startup logs.
//
// Both transports report the relation the same way, so the helper below words it
// once for this path and for the state document's.
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
			return relation(state.PluginVersion)
		case <-timer.C:
			return "warn", "no retained snapshot, so the plugin version is unknown"
		case <-ctx.Done():
			return "warn", "timed out waiting for a snapshot"
		}
	}
}

// relation describes how a plugin's reported release compares to this daemon's.
// Both transports report the same version in the same way, so they share this:
// naming which half is behind is the answer either way. The sentence itself comes
// from version.Explain, which is what the running daemon's warning is worded
// from too, so the log and the report cannot describe one pairing two ways.
//
// Skew warns rather than fails, exactly as the running daemon does: a mismatch is
// not this command's own failure, and the user may be gating a start on its exit
// code.
func relation(reported string) (string, string) {
	reported = normalize(reported)
	rel := version.Relate(reported, version.Number)
	status := "warn"
	if rel == version.Same {
		status = "ok"
	}
	return status, version.Explain(rel, reported, version.Number)
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
