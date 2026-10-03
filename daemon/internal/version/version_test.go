package version_test

// This file tests the release-line comparison: which pairings of plugin and
// daemon versions relate as same, plugin-behind, daemon-behind, or unknown, and
// the exact sentence each relation is worded as. It also tests the ordering the
// release check uses, which is a stricter question asked of the same strings.

import (
	"testing"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/version"
)

// TestVersionRelateComparesReleaseLines walks the release-line shapes a plugin
// can report — identical, skewed by a patch or a second component, older or
// newer on either a minor or a major, absent, unparseable, v-prefixed, and
// space-padded — and asserts which relation each pairing proves. The two-digit
// minor cases are the ones that hold the comparison numeric: comparing the
// rendered strings would call 1.10 older than 1.7 and invert the answer.
func TestVersionRelateComparesReleaseLines(t *testing.T) {
	tests := []struct {
		name   string
		plugin string
		daemon string
		want   version.Relation
	}{
		{"identical", "1.7.0", "1.7.0", version.Same},
		{"patch skew stays quiet", "1.7.0", "1.7.1", version.Same},
		{"two component version", "1.7", "1.7.0", version.Same},
		{"plugin on an older minor", "1.6.0", "1.7.0", version.PluginBehind},
		{"plugin on an older major", "1.6.0", "2.0.0", version.PluginBehind},
		{"daemon on an older minor", "1.7.0", "1.6.0", version.DaemonBehind},
		{"daemon on an older major", "2.0.0", "1.6.0", version.DaemonBehind},
		{"two digit minor compares numerically", "1.10.0", "1.7.0", version.DaemonBehind},
		{"two digit minor against itself", "1.10.0", "1.10.3", version.Same},
		{"absence is not a comparison", "", "1.7.0", version.Unknown},
		{"unparseable plugin is not a comparison", "not-a-version", "1.7.0", version.Unknown},
		{"unparseable daemon is not a comparison", "1.7.0", "dev", version.Unknown},
		{"both absent", "", "", version.Unknown},
		{"v prefix is tolerated", "v1.8.0", "1.7.0", version.DaemonBehind},
		{"surrounding space is tolerated", " 1.8.0 ", "1.7.0", version.DaemonBehind},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := version.Relate(test.plugin, test.daemon); got != test.want {
				t.Fatalf("Relate(%q, %q) = %v, want %v", test.plugin, test.daemon, got, test.want)
			}
		})
	}
}

// A plugin reports its version in whatever spelling it was built with, and both
// the comparison here and the daemon's log lines and release URLs are written
// from one rendering of it. These are the spellings that collapse to one value.
//
// Only a leading "v" is dropped: the daemon puts this value back in front of
// itself when it renders a tag, so a "v" left anywhere else in the string would
// become a second one.
func TestVersionNormalizesEverySpellingOfOneRelease(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"bare", "1.8.0", "1.8.0"},
		{"v prefixed", "v1.8.0", "1.8.0"},
		{"surrounding space", " 1.8.0 ", "1.8.0"},
		{"space and v", "  v1.8.0\t", "1.8.0"},
		{"empty", "", ""},
		{"whitespace only", "   ", ""},
		{"v alone", "v", ""},
		{"v not leading", "1.8.0v", "1.8.0v"},
		{"inner space", "1.8.0 beta", "1.8.0 beta"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := version.Normalize(test.value); got != test.want {
				t.Fatalf("Normalize(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

// The daemon's warning and the --check report describe one relation to the same
// reader, so they share one sentence. These are the sentences; both consumers
// are asserted to carry them verbatim.
func TestVersionExplainWordsEveryRelation(t *testing.T) {
	tests := []struct {
		name     string
		relation version.Relation
		plugin   string
		want     string
	}{
		{
			"same line",
			version.Same,
			"1.8.0",
			"plugin v1.8.0 matches daemon v1.8.0",
		},
		{
			"plugin behind",
			version.PluginBehind,
			"1.4.0",
			"plugin v1.4.0 is older than daemon v1.8.0, so the plugin is the half that is behind",
		},
		{
			"daemon behind",
			version.DaemonBehind,
			"1.9.0",
			"plugin v1.9.0 is newer than daemon v1.8.0, so the daemon is the half that is behind",
		},
		{
			"not comparable",
			version.Unknown,
			"dev",
			"plugin vdev is not comparable to daemon v1.8.0",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := version.Explain(test.relation, test.plugin, "1.8.0")
			if got != test.want {
				t.Fatalf("Explain(%v, %q, %q) = %q, want %q", test.relation, test.plugin, "1.8.0", got, test.want)
			}
		})
	}
}

// TestVersionNewerOrdersEveryComponent covers the ordering the release check
// asks for, which the handshake's comparison deliberately cannot answer. The
// difference is the patch component: Relate calls 1.11.0 and 1.11.1 the same
// release line, because a patch cannot change the pub/sub payload, so reusing it
// here would let a patch release ship unnoticed.
func TestVersionNewerOrdersEveryComponent(t *testing.T) {
	tests := []struct {
		name      string
		current   string
		candidate string
		want      bool
	}{
		{"a later patch is newer", "1.11.0", "1.11.1", true},
		{"a later minor is newer", "1.11.0", "1.12.0", true},
		{"a later major is newer", "1.11.0", "2.0.0", true},
		{"the same release is not newer", "1.11.0", "1.11.0", false},
		{"an earlier patch is not newer", "1.11.1", "1.11.0", false},
		{"an earlier minor is not newer", "1.12.0", "1.11.0", false},
		{"an earlier major is not newer", "2.0.0", "1.11.0", false},
		{"a two digit minor compares numerically", "1.9.0", "1.10.0", true},
		{"a two digit patch compares numerically", "1.11.9", "1.11.10", true},
		{"a missing patch is zero", "1.11", "1.11.0", false},
		{"a missing patch is behind a real one", "1.11", "1.11.1", true},
		{"a v prefix is tolerated", "1.11.0", "v1.11.1", true},
		{"a build suffix is not an ordering", "1.11.0", "1.11.1-rc1", false},
		{"a word is not an ordering", "1.11.0", "nightly", false},
		{"an unreadable current is not an ordering", "dev", "1.12.0", false},
		{"a fourth component is not a release line", "1.11.0", "1.11.0.1", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := version.Newer(test.current, test.candidate); got != test.want {
				t.Fatalf("Newer(%q, %q) = %v, want %v", test.current, test.candidate, got, test.want)
			}
		})
	}
}

// TestVersionIsVersionAcceptsOnlyReleaseLines covers the boundary the release
// checker judges a stranger's string against before anything is compared to it.
// The checker refuses a tag it cannot order rather than reading it as "not
// newer", so "is this a release line" has to be answerable on its own.
func TestVersionIsVersionAcceptsOnlyReleaseLines(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{"three components", "1.11.0", true},
		{"the v prefix this project tags with", "v1.11.0", true},
		{"two components", "1.11", true},
		{"surrounding space", " 1.11.0 ", true},
		{"four components", "1.11.0.1", false},
		{"a non numeric patch", "1.11.x", false},
		{"a name", "nightly", false},
		{"one component", "1", false},
		{"empty", "", false},
		{"the prefix alone", "v", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := version.IsVersion(test.value); got != test.want {
				t.Fatalf("IsVersion(%q) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}
