package daemon

// This file is the daemon's own release check: it asks, on a timer, whether a
// newer release exists, and says so once in the journal.
//
// It is deliberately not part of the run loop. It reads no playback state,
// publishes nothing to Discord, and shares no state with the session, so it runs
// on a goroutine of its own, where a slow or unreachable GitHub cannot delay the
// presence the loop exists to publish.

import (
	"context"
	"fmt"
	"time"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/diag"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/version"
)

// releaseInterval is how often a running daemon asks whether a newer release
// exists. A user service runs for weeks, so a check that happened only at startup
// would never see a release that came out a month later; and a release is not
// urgent, so once a day is often enough to be useful without being traffic.
const releaseInterval = 24 * time.Hour

// releaseLookupFailed is the latch key for a lookup that could not be made. It
// names the condition rather than the error, for the reason unreachableDiscord
// does: every failed lookup is the same condition, and a latch keyed on the error
// text would report again whenever the reason changed.
const releaseLookupFailed = "release lookup failed"

// releaseChecker is the slice of the release package the daemon needs: the one
// question it asks, and nothing about how the answer is fetched.
type releaseChecker interface {
	Latest(context.Context) (string, error)
}

// watchReleases reports a newer release once per distinct tag, and returns when
// ctx is cancelled.
//
// The interval is a parameter rather than releaseInterval so a test can drive this
// without waiting out a day, the same way run takes refresh.
//
// A lookup that fails is reported once and then left alone until one succeeds,
// because a machine that is offline is an ordinary state for this daemon rather
// than a fault. A successful lookup that finds nothing newer clears the latch, so
// a failure after it is reported afresh — the clause a latch that never cleared
// would lose, exactly when it mattered.
//
// A cancellation is not a failed lookup: the context being done is this function
// being asked to stop, and logging it would put a line in the journal every time
// the daemon shut down.
func watchReleases(ctx context.Context, check releaseChecker, interval time.Duration, logger diag.Logger) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	var reported latch
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			switch tag, err := check.Latest(ctx); {
			case err != nil:
				if ctx.Err() != nil {
					return
				}
				if reported.first(releaseLookupFailed) {
					logger.Printf("check for a newer release: %v", err)
				}
			case version.Newer(version.Number, tag):
				if reported.first(tag) {
					logger.Printf("%s", newerReleaseWarning(tag))
				}
			default:
				reported.clear()
			}
			reset(timer, interval)
		}
	}
}

// newerReleaseWarning is the sentence both the journal line and the --check report
// are worded from, so the two cannot describe one release two ways.
//
// It names the one command that installs both halves. It used to name one command
// per half, which was two chances to update one of them and be left with the
// mismatched pairing this project's version check exists to warn about; the flag
// it names now drives both, and it restarts the daemon the user would otherwise
// have had to remember to restart themselves.
func newerReleaseWarning(tag string) string {
	return fmt.Sprintf(
		"a newer release exists: %s, and this daemon is v%s. Update both halves with: cliamp-rpcd --update",
		tag, version.Number,
	)
}
