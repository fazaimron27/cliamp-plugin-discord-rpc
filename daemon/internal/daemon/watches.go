package daemon

// This file holds the two report-once latches the loop writes through — one for
// a plugin/daemon release-line mismatch, one for an activity Discord refused —
// together with the release-line rendering and the install/update commands they
// put in the warning text. The --check report reads normalize from here too,
// since it words the same mismatch from the other side.

import (
	"fmt"
	"strings"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/version"
)

const (
	repository = "fazaimron27/cliamp-plugin-discord-rpc"
	rawBase    = "https://raw.githubusercontent.com/" + repository + "/"
)

// normalize trims a plugin-reported version and drops any leading "v". The value
// arrives from the plugin, so it may carry the prefix, surrounding space, or
// neither, and it is rendered into log lines as well as URLs.
func normalize(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "v")
}

// tag renders a reported version as a release tag, carrying exactly one leading
// "v".
func tag(value string) string {
	return "v" + normalize(value)
}

// versionWatch reports a plugin/daemon release-line mismatch once per distinct
// plugin version. Repeating it on every snapshot would bury the genuine error
// traffic, and a mismatch is a one-time discovery rather than a per-track event.
type versionWatch struct {
	reported string
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
func (w *versionWatch) observe(pluginVersion string) string {
	reported := normalize(pluginVersion)
	if reported == "" || reported == w.reported {
		return ""
	}
	w.reported = reported
	relation := version.Relate(reported, version.Number)
	explained := version.Explain(relation, reported, version.Number)
	switch relation {
	case version.PluginBehind:
		return fmt.Sprintf(
			"discord-rpc %s; these release lines use incompatible transports. Install matching halves with: cliamp plugins install %s@v%s",
			explained, repository, version.Number,
		)
	case version.DaemonBehind:
		return fmt.Sprintf(
			"discord-rpc %s. Update cliamp-rpcd with: curl -fsSL %s%s/install.sh | sh (or rebuild from source), then restart it.",
			explained, rawBase, tag(reported),
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
	reported string
}

// observe records a rejection and reports whether it is one still worth
// logging, which is the first of its kind since the last accepted activity.
func (w *rejectionWatch) observe(err error) bool {
	detail := err.Error()
	if detail == w.reported {
		return false
	}
	w.reported = detail
	return true
}

// accepted records that Discord took an activity, so a rejection after it is
// reported afresh.
func (w *rejectionWatch) accepted() {
	w.reported = ""
}
