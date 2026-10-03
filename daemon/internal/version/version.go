// Package version is the release identity of the daemon and its Cliamp plugin.
package version

// This file is that identity: the one version constant the Lua manifest, the
// release tag, and the bundled installer all restate, plus the functions that
// compare a plugin's release line against the daemon's and word the result for
// the daemon's warning and the --check report.

import (
	"fmt"
	"strconv"
	"strings"
)

// Number is the released version of this project. The Lua plugin manifest and
// the release tag must carry the same value; install.sh is bundled into release
// archives and pins the same line, so bump all of them together.
const Number = "1.11.0"

// Relation describes how a plugin's release line compares to the daemon's.
type Relation int

const (
	// Same means both halves released on the same line, so the pairing is
	// supported.
	Same Relation = iota
	// PluginBehind means the plugin is on an older line than the daemon, so the
	// plugin is the half to replace.
	PluginBehind
	// DaemonBehind means the daemon is on an older line than the plugin, so the
	// daemon is the half to replace.
	DaemonBehind
	// Unknown means a version was absent or unparseable, leaving nothing to
	// compare.
	Unknown
)

// String names the relation, so test failures and log lines read as words
// rather than as an opaque integer.
func (r Relation) String() string {
	switch r {
	case Same:
		return "same"
	case PluginBehind:
		return "plugin-behind"
	case DaemonBehind:
		return "daemon-behind"
	default:
		return "unknown"
	}
}

// Relate compares the release lines of the plugin and the daemon. Only the major
// and minor components are compared: a patch difference cannot change the
// pub/sub payload, so it reports Same.
//
// A version that is absent or unparseable reports Unknown. A plugin old enough to
// omit its version predates the report, and warning about a value it never sent
// would only produce an alert the user cannot act on.
//
// Each component is compared numerically rather than as the string that renders
// it: "1.10" sorts before "1.7" as text, so a string comparison would invert a
// two-digit minor and call the newer release the older one.
func Relate(pluginVersion, daemonVersion string) Relation {
	pluginMajor, pluginMinor, pluginOK := releaseLine(pluginVersion)
	daemonMajor, daemonMinor, daemonOK := releaseLine(daemonVersion)
	if !pluginOK || !daemonOK {
		return Unknown
	}
	if pluginMajor == daemonMajor && pluginMinor == daemonMinor {
		return Same
	}
	if pluginMajor != daemonMajor {
		if pluginMajor < daemonMajor {
			return PluginBehind
		}
		return DaemonBehind
	}
	if pluginMinor < daemonMinor {
		return PluginBehind
	}
	return DaemonBehind
}

// Explain words a relation as one sentence, naming both release lines and, when
// one half is behind, which half that is.
//
// The running daemon's warning and the --check report answer the same question
// about the same snapshot, and they used to answer it in different words: the
// daemon said the versions "do not match", the diagnostic said the plugin "is
// older than" the daemon. A reader with both in front of them had two accounts of
// one state to reconcile. One function words both, so there is one account.
//
// The sentence carries no trailing punctuation, leaving a caller free to follow
// it with the remedy for the half it names.
func Explain(relation Relation, plugin, daemon string) string {
	switch relation {
	case Same:
		return fmt.Sprintf("plugin v%s matches daemon v%s", plugin, daemon)
	case PluginBehind:
		return fmt.Sprintf("plugin v%s is older than daemon v%s, so the plugin is the half that is behind", plugin, daemon)
	case DaemonBehind:
		return fmt.Sprintf("plugin v%s is newer than daemon v%s, so the daemon is the half that is behind", plugin, daemon)
	default:
		return fmt.Sprintf("plugin v%s is not comparable to daemon v%s", plugin, daemon)
	}
}

// Normalize trims a reported version and drops one leading "v", giving the
// spelling every consumer of a release line writes from.
//
// The value arrives from the plugin, so it may carry the prefix, surrounding
// space, or neither, and the same release reaches this daemon as several
// strings. Comparison tolerates that by parsing, but the daemon also renders
// the value into log lines and release URLs, where two spellings of one release
// are two different strings — and a tag rendered from an un-normalized value
// would carry "vv". Normalizing first is what makes one release one value.
//
// Only a leading "v" is dropped. A "v" anywhere else is left where it is: it is
// not the prefix this exists to remove, and removing it would hide a value the
// plugin did not send.
func Normalize(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "v")
}

// releaseLine parses a dotted version into its major and minor components,
// reporting false when the value does not start with two numeric components.
func releaseLine(value string) (int, int, bool) {
	fields := strings.Split(Normalize(value), ".")
	if len(fields) < 2 {
		return 0, 0, false
	}
	major, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, 0, false
	}
	minor, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// releaseNumber parses a dotted version into all three numeric components,
// reporting false when a component is missing, non-numeric, or one too many to
// be a release line: four components name something this project does not
// publish.
//
// It sits beside releaseLine rather than replacing it because the two have
// different right answers for one input. releaseLine reads the first two
// components and ignores whatever follows, which is what keeps the handshake
// quiet about a build reporting "1.11.0-dev": that is the same release line, and
// warning about it would be a false mismatch. An ordering cannot be that lenient,
// because a suffix is not evidence of a newer release and reading it as one would
// send a user to a download that may not exist, so this refuses instead.
func releaseNumber(value string) (int, int, int, bool) {
	fields := strings.Split(Normalize(value), ".")
	if len(fields) < 2 || len(fields) > 3 {
		return 0, 0, 0, false
	}
	var numbers [3]int
	for index, field := range fields {
		number, err := strconv.Atoi(field)
		if err != nil {
			return 0, 0, 0, false
		}
		numbers[index] = number
	}
	return numbers[0], numbers[1], numbers[2], true
}

// IsVersion reports whether value names a release line this package can order:
// two or three dot-separated numeric components, with a leading "v" and
// surrounding space tolerated as everywhere else here.
//
// It is exported because the release checker has to tell a tag it cannot read
// from a tag that is merely not newer, and the alternative is a second copy of
// this rule somewhere else that could drift from the comparison's.
func IsVersion(value string) bool {
	_, _, _, ok := releaseNumber(value)
	return ok
}

// Newer reports whether candidate is a later release than current, comparing all
// three components. A missing component counts as zero, so "1.12" and "1.12.0"
// name one release.
//
// Relate deliberately ignores the patch component, because a patch difference
// cannot change the pub/sub payload. This deliberately does not, because a patch
// release is still something to install. Folding the two together would either
// make every patch bump look like a handshake mismatch or make every patch
// release invisible, and the second is the quieter failure: v1.10.1 shipped and
// nothing in this package would have mentioned it.
//
// A side that does not parse reports false rather than true. An unreadable value
// is not evidence of a newer release, and guessing would send a user to download
// something that may not exist.
func Newer(current, candidate string) bool {
	currentMajor, currentMinor, currentPatch, currentOK := releaseNumber(current)
	candidateMajor, candidateMinor, candidatePatch, candidateOK := releaseNumber(candidate)
	if !currentOK || !candidateOK {
		return false
	}
	if candidateMajor != currentMajor {
		return candidateMajor > currentMajor
	}
	if candidateMinor != currentMinor {
		return candidateMinor > currentMinor
	}
	return candidatePatch > currentPatch
}
