package tests

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/version"
)

// repoFile reads a file from the repository root. Go runs a test binary with its
// working directory set to the package directory, so the root is two levels
// above this package.
func repoFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestReleasePinsAgreeWithVersionConstant guards against a missed version bump.
// install.sh is copied into every release archive, so a stale default there
// ships an archive whose bundled installer downloads the previous daemon: a
// failure that stays silent until someone inspects the wrong binary. The same
// check runs in CI through the repository's existing `go test` step.
func TestReleasePinsAgreeWithVersionConstant(t *testing.T) {
	number := version.Number

	t.Run("plugin manifest", func(t *testing.T) {
		source := repoFile(t, "discord-rpc.lua")
		match := regexp.MustCompile(`(?m)^local VERSION = "([^"]+)"`).FindStringSubmatch(source)
		if match == nil {
			t.Fatal("discord-rpc.lua declares no local VERSION")
		}
		if match[1] != number {
			t.Fatalf("discord-rpc.lua VERSION = %q, want %q", match[1], number)
		}
	})

	t.Run("installer default and help", func(t *testing.T) {
		source := repoFile(t, "install.sh")
		// Both of these ship inside the archive, so both have to be current.
		patterns := []string{
			`(?m)^version="\$\{CLIAMP_RPC_VERSION:-v([^}"]+)\}"`,
			`(?m)^\s*--version VERSION[^\n]*\(default: v([0-9.]+)\)`,
		}
		for _, pattern := range patterns {
			match := regexp.MustCompile(pattern).FindStringSubmatch(source)
			if match == nil {
				t.Fatalf("install.sh has no version pin matching %s", pattern)
			}
			if match[1] != number {
				t.Fatalf("install.sh pins %q, want %q", match[1], number)
			}
		}
	})

	t.Run("documentation", func(t *testing.T) {
		source := repoFile(t, "README.md")
		// Derived from the current release rather than hardcoded, so a pin left
		// behind by a partial bump is rejected instead of tolerated.
		allowed := map[string]bool{
			"v" + number: true, // the current release
			"v1.4.0":     true, // the sanctioned pinned-legacy installation
			"v1.5.0":     true, // named in prose as superseded
		}
		for _, found := range regexp.MustCompile(`v[0-9]+\.[0-9]+\.[0-9]+`).FindAllString(source, -1) {
			if !allowed[found] {
				t.Errorf("README.md pins %s, which is neither the current release nor a sanctioned legacy reference", found)
			}
		}

		// The documentation writes the release both ways: v-prefixed in install
		// commands and bare in sample output and prose. Scanning only the
		// prefixed form leaves those bare ones to rot through a bump, so every
		// version-shaped string is scanned and two shapes are exempted instead.
		//
		// A match inside a v-prefixed version is already covered by the loop
		// above, and a Go version names the toolchain a contributor needs rather
		// than this project's release.
		bare := regexp.MustCompile(`[0-9]+\.[0-9]+\.[0-9]+`)
		for _, loc := range bare.FindAllStringIndex(source, -1) {
			found := source[loc[0]:loc[1]]
			before := source[:loc[0]]
			if strings.HasSuffix(before, "v") || strings.HasSuffix(before, "Go ") {
				continue
			}
			if found != number {
				line := 1 + strings.Count(before, "\n")
				t.Errorf("README.md:%d pins %s, want %s", line, found, number)
			}
		}
	})
}
