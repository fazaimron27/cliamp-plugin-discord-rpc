package version_test

// This file is the release-pin guard: it reads every file a release pin is
// written into and fails when one disagrees with version.Number. The set of
// files matters as much as the check, because a pin moved into a file the scan
// does not read is a pin no check can see — nothing else reads docs/, so the
// split that created docs/building.md moved a `git clone --branch vX.Y.Z` into
// it, and leaving the scan on README.md alone would have retired that pin from
// the guard silently.

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
//
// The installer carries two pins — the default version and the --version help
// text — and both ship inside the archive, so both have to be current.
//
// The documentation pins are derived from the current release rather than
// hardcoded, so a pin left behind by a partial bump is rejected instead of
// tolerated. The allowed set holds the current release and nothing else: it used
// to carry v1.4.0 and v1.5.0 as sanctioned legacy references, which meant a
// document that still recommended a retired line satisfied the guard that was
// supposed to notice a stale pin. A retired line is not a permitted pin, so
// recommending one again fails here.
//
// Those documents write the release both ways — v-prefixed in install commands
// and bare in sample output and prose — and scanning only the prefixed form
// leaves the bare ones to rot through a bump. Every version-shaped string is
// therefore scanned, with two shapes exempted: a match inside a v-prefixed
// version, which the prefixed pass already covered, and a Go version, which
// names the toolchain a contributor needs rather than this project's release.
//
// The README also quotes the diagnostic's report, and a quote that no longer
// matches what the command prints is documentation gone stale in silence — the
// same class of drift as a missed version pin, and one the scans above cannot
// catch, since they read versions rather than wording. The quoted sentence is
// computed from version.Explain rather than restated, so rewording the report
// fails here until the README line is updated to match.
//
// The release workflow is the last pin. release.yml is the only place that
// checks the tag against version.Number, and it reads the constant at run time
// rather than restating it, so any version written into a file is a pin
// nothing guards. That is what the comment beside the tag check used to be: it
// named a version pair that went stale on the next release, in a file the guard
// did not see. Action pins are exempt — the v-prefixed string in
// `uses: actions/checkout@<sha> # v5.0.0` names another project's release, so
// bumping an action has nothing to do with version.Number — and no other
// version-shaped string belongs in the file.
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
		allowed := map[string]bool{
			"v" + number: true,
		}
		for _, name := range []string{"README.md", "docs/building.md"} {
			source := repoFile(t, name)

			for _, loc := range regexp.MustCompile(`v[0-9]+\.[0-9]+\.[0-9]+`).FindAllStringIndex(source, -1) {
				found := source[loc[0]:loc[1]]
				if !allowed[found] {
					line := 1 + strings.Count(source[:loc[0]], "\n")
					t.Errorf("%s:%d pins %s, which is not the current release", name, line, found)
				}
			}

			bare := regexp.MustCompile(`[0-9]+\.[0-9]+\.[0-9]+`)
			for _, loc := range bare.FindAllStringIndex(source, -1) {
				found := source[loc[0]:loc[1]]
				before := source[:loc[0]]
				if strings.HasSuffix(before, "v") || strings.HasSuffix(before, "Go ") {
					continue
				}
				if found != number {
					line := 1 + strings.Count(before, "\n")
					t.Errorf("%s:%d pins %s, want %s", name, line, found, number)
				}
			}
		}
	})

	t.Run("quoted report", func(t *testing.T) {
		quoted := version.Explain(version.Same, number, number)
		if !strings.Contains(repoFile(t, "README.md"), quoted) {
			t.Errorf("README.md does not quote the diagnostic's matching plugin line; want a line reading %q", quoted)
		}
	})

	t.Run("release workflow", func(t *testing.T) {
		source := repoFile(t, ".github/workflows/release.yml")
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
