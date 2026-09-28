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

// Mismatch reports whether the plugin was released on a different line than
// this daemon. Only the major and minor components are compared: a patch
// difference cannot change the pub/sub payload, so it stays quiet.
//
// An absent or unparseable version reports false. A plugin old enough to omit
// its version predates the report, and warning about a value it never sent
// would only produce an alert the user cannot act on.
func Mismatch(pluginVersion, daemonVersion string) bool {
	plugin, daemon := releaseLine(pluginVersion), releaseLine(daemonVersion)
	if plugin == "" || daemon == "" {
		return false
	}
	return plugin != daemon
}

// releaseLine reduces a dotted version to its "major.minor" prefix, or returns
// an empty string when the value does not start with two numeric components.
func releaseLine(value string) string {
	fields := strings.Split(strings.TrimPrefix(strings.TrimSpace(value), "v"), ".")
	if len(fields) < 2 {
		return ""
	}
	major, err := strconv.Atoi(fields[0])
	if err != nil {
		return ""
	}
	minor, err := strconv.Atoi(fields[1])
	if err != nil {
		return ""
	}
	return strconv.Itoa(major) + "." + strconv.Itoa(minor)
}
