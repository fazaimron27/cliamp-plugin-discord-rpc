package daemon

// This file holds the report-once latch the loop's three reporters share —
// a plugin/daemon release-line mismatch, an activity Discord refused, and an
// unreachable Discord — together with the release-line rendering and the
// install/update commands they put in the warning text. The --check report
// reads normalize from here too, since it words the same mismatch from the
// other side.

import (
	"fmt"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/release"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/version"
)

// normalize trims a plugin-reported version and drops any leading "v". The value
// arrives from the plugin, so it may carry the prefix, surrounding space, or
// neither, and it is rendered into log lines as well as URLs.
//
// The rule lives in the version package because the release-line comparison
// applies it too, and both sides must agree on the spelling: a tag rendered from
// one spelling and compared under another would describe two releases.
func normalize(value string) string {
	return version.Normalize(value)
}

// tag renders a reported version as a release tag, carrying exactly one leading
// "v".
func tag(value string) string {
	return "v" + normalize(value)
}

// latch remembers the one condition its holder last reported, so a condition
// that persists is not reported again on every attempt to observe it.
//
// The three conditions this package latches differ — a release-line mismatch, a
// refused activity, an unreachable Discord — but the rule is one rule: report a
// condition the first time it is seen, stay quiet while it is the same
// condition, and report it again once it has cleared and come back. A reporter
// that forgot the last clause would fall silent exactly when the daemon began
// failing again, which is the state its report exists to surface.
//
// The remembered value is a string rather than the condition itself because
// what makes two conditions the same differs per reporter: a release is the
// same release however it is spelled, so the version watch normalizes before
// keying; a refusal is the same refusal when Discord's detail matches; an
// outage is one outage for as long as it lasts, whatever reason the failing
// dial gives, so the session keys it on the outage rather than the error.
//
// The empty string is the state of having reported nothing, so no caller may
// key a latch on an empty condition: it would read as a repeat of the initial
// silence. Every condition here is non-empty by construction.
type latch struct {
	reported string
}

// first reports whether key is a condition this latch has not reported, and
// remembers it.
func (l *latch) first(key string) bool {
	if key == l.reported {
		return false
	}
	l.reported = key
	return true
}

// clear forgets the reported condition, so the same one after it is reported
// again.
func (l *latch) clear() {
	l.reported = ""
}

// versionWatch reports a plugin/daemon release-line mismatch once per distinct
// plugin version. Repeating it on every snapshot would bury the genuine error
// traffic, and a mismatch is a one-time discovery rather than a per-track event.
type versionWatch struct {
	latch
}

// observe returns the warning to log for a plugin version, or an empty string
// when the pairing is compatible, the plugin is too old to report a version, or
// this version has already been reported.
//
// The remembered value is the normalized one, because that is what the warning
// is written from: two spellings that render the same line are the same report,
// and deduping on the raw string would print it twice.
//
// The sentence it returns is the same one the --check report prints; only the
// remedy differs, because only this consumer knows that it is running. Naming
// the half that is behind is the point of that remedy: this daemon is usually a
// source build running ahead of the installed plugin, so a warning that always
// pointed at the plugin would have that user downgrade the half that is current.
//
// The removal in that remedy is not decoration. A plugin that reported a version
// is already installed, and Cliamp's install refuses a plugin that is already
// there, so the install alone could only ever fail.
func (w *versionWatch) observe(pluginVersion string) string {
	reported := normalize(pluginVersion)
	if reported == "" || !w.first(reported) {
		return ""
	}
	relation := version.Relate(reported, version.Number)
	explained := version.Explain(relation, reported, version.Number)
	switch relation {
	case version.PluginBehind:
		return fmt.Sprintf(
			"discord-rpc %s; these release lines use incompatible transports. Install matching halves with: cliamp plugins remove %s, then cliamp plugins install %s@v%s",
			explained, pluginName, release.Repository, version.Number,
		)
	case version.DaemonBehind:
		return fmt.Sprintf(
			"discord-rpc %s. Update cliamp-rpcd with: curl -fsSL %s%s/install.sh | sh (or rebuild from source), then restart it.",
			explained, release.RawBase, tag(reported),
		)
	default:
		return ""
	}
}

// rejectionWatch reports a refused activity once per distinct rejection. The
// loop re-tries a refused payload on every refresh, so a line per attempt would
// bury the journal it exists to explain, and would do it at the refresh rate for
// as long as the payload stays refused.
//
// It is keyed on Discord's own detail rather than on the activity, because the
// detail is what names the fault: a second and different refusal is news, while
// the same one repeated is not. A successful publish clears it, because a latch
// that never cleared would fall silent exactly when the daemon began refusing
// activities again, which is the state the report exists to surface.
type rejectionWatch struct {
	latch
}

// observe records a rejection and reports whether it is one still worth
// logging, which is the first of its kind since the last accepted activity.
func (w *rejectionWatch) observe(err error) bool {
	return w.first(err.Error())
}

// accepted records that Discord took an activity, so a rejection after it is
// reported afresh.
func (w *rejectionWatch) accepted() {
	w.clear()
}
