package tests

import (
	"testing"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/version"
)

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
		// String comparison would call 1.10 older than 1.7 and invert this.
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
