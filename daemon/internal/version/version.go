// Package version is the release identity of the daemon and its Cliamp plugin.
package version

import (
	"strconv"
	"strings"
)

// Number is the released version of this project. The Lua plugin manifest and
// the release tag must carry the same value; install.sh is bundled into release
// archives and pins the same line, so bump all of them together.
const Number = "1.7.0"

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
func Relate(pluginVersion, daemonVersion string) Relation {
	pluginMajor, pluginMinor, pluginOK := releaseLine(pluginVersion)
	daemonMajor, daemonMinor, daemonOK := releaseLine(daemonVersion)
	if !pluginOK || !daemonOK {
		return Unknown
	}
	if pluginMajor == daemonMajor && pluginMinor == daemonMinor {
		return Same
	}
	// Compare numerically: minor 10 is later than minor 7, which a comparison of
	// the rendered "1.10" and "1.7" strings would invert.
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

// releaseLine parses a dotted version into its major and minor components,
// reporting false when the value does not start with two numeric components.
func releaseLine(value string) (int, int, bool) {
	fields := strings.Split(strings.TrimPrefix(strings.TrimSpace(value), "v"), ".")
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
