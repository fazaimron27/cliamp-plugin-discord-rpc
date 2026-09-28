package tests

import (
	"testing"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/version"
)

func TestVersionMismatchComparesReleaseLines(t *testing.T) {
	tests := []struct {
		name     string
		plugin   string
		daemon   string
		mismatch bool
	}{
		{"identical", "1.6.1", "1.6.1", false},
		{"patch skew stays quiet", "1.6.0", "1.6.1", false},
		{"minor skew", "1.6.1", "1.7.0", true},
		{"major skew", "1.5.0", "1.6.1", true},
		{"across the incompatible line", "1.4.0", "1.6.1", true},
		{"absence is not a mismatch", "", "1.6.1", false},
		{"unparseable plugin is not a mismatch", "not-a-version", "1.6.1", false},
		{"unparseable daemon is not a mismatch", "1.6.1", "dev", false},
		{"both absent", "", "", false},
		{"v prefix is tolerated", "v1.7.0", "1.6.1", true},
		{"two component version", "1.6", "1.6.1", false},
		{"surrounding space is tolerated", " 1.7.0 ", "1.6.1", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := version.Mismatch(test.plugin, test.daemon); got != test.mismatch {
				t.Fatalf("Mismatch(%q, %q) = %v, want %v", test.plugin, test.daemon, got, test.mismatch)
			}
		})
	}
}
