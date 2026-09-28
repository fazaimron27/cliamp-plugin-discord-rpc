package version_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/version"
)

// repoFile reads a file from the repository root. Go runs a test binary with its
// working directory set to the package directory, so the root is three levels
// above this package.
func repoFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", name))
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
		// behind by a partial bump is rejected instead of tolerated. The allowed
		// set holds the current release and nothing else: it used to carry
		// v1.4.0 and v1.5.0 as sanctioned legacy references, which meant a
		// document that still recommended a retired line satisfied the guard
		// that was supposed to notice a stale pin. A retired line is not a
		// permitted pin, so recommending one again fails here.
		allowed := map[string]bool{
			"v" + number: true, // the current release
		}
		for _, loc := range regexp.MustCompile(`v[0-9]+\.[0-9]+\.[0-9]+`).FindAllStringIndex(source, -1) {
			found := source[loc[0]:loc[1]]
			if !allowed[found] {
				line := 1 + strings.Count(source[:loc[0]], "\n")
				t.Errorf("README.md:%d pins %s, which is not the current release", line, found)
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

	// The README quotes the diagnostic's report, and a quote that no longer
	// matches what the command prints is documentation that went stale in
	// silence — the same class of drift as a missed version pin, and undetected
	// by the scans above, which read versions rather than wording. The sentence
	// is derived from the function the report words itself with, so rewording
	// the report fails here until the quoted line is updated to match.
	t.Run("quoted report", func(t *testing.T) {
		quoted := version.Explain(version.Same, number, number)
		if !strings.Contains(repoFile(t, "README.md"), quoted) {
			t.Errorf("README.md does not quote the diagnostic's matching plugin line; want a line reading %q", quoted)
		}
	})

	t.Run("release workflow", func(t *testing.T) {
		source := repoFile(t, ".github/workflows/release.yml")
		// release.yml is the only place that checks the tag against
		// version.Number, and it reads the constant at run time rather than
		// restating it. So any version written into this file is a pin with
		// nothing guarding it, which is what the comment beside the tag check
		// used to be: it named a version pair that went stale on the next
		// release, and the guard could not see the file it lived in.
		//
		// Action pins are exempt. The v-prefixed string in
		// `uses: actions/checkout@<sha> # v5.0.0` names another project's
		// release, so bumping an action has nothing to do with version.Number.
		// No other version-shaped string belongs in this file.
		versioned := regexp.MustCompile(`v?[0-9]+\.[0-9]+\.[0-9]+`)
		for index, rawLine := range strings.Split(source, "\n") {
			if strings.Contains(rawLine, "uses:") {
				continue
			}
			for _, found := range versioned.FindAllString(rawLine, -1) {
				if found != number && found != "v"+number {
					t.Errorf("release.yml:%d names %s, which is neither the current release nor an action pin",
						index+1, found)
				}
			}
		}
	})
}
