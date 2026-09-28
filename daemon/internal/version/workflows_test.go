package version_test

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// runnerLine matches the value of a runs-on key. An expression-valued runner
// (${{ matrix.os }}) or a list ([self-hosted, linux]) does not match a single
// bare word, so those are skipped rather than misread as a floating image.
var runnerLine = regexp.MustCompile(`(?m)^\s*runs-on:\s*([^\s#]+)\s*(?:#.*)?$`)

// TestWorkflowRunnersNameTheirImage asserts that no workflow accepts a floating
// "latest" runner image.
//
// ubuntu-latest is not a fixed platform: GitHub moved that label to Ubuntu 26.04
// over the weeks from 2026-10-19, so a workflow on it changes its operating
// system, its glibc, and its preinstalled tool versions without a commit here.
// That is the class of change CI exists to catch, and it should not arrive
// inside CI itself. GitHub's documented remedy is to pin the image that has been
// tested against, which holds only while nothing quietly puts the floating label
// back.
func TestWorkflowRunnersNameTheirImage(t *testing.T) {
	workflows, err := filepath.Glob(filepath.Join("..", "..", "..", ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(workflows) == 0 {
		t.Fatal("no workflows matched; the path is wrong rather than the repository having none")
	}

	inspected := 0
	for _, workflow := range workflows {
		name := filepath.Base(workflow)
		lines := runnerLine.FindAllStringSubmatch(repoFile(t, filepath.Join(".github", "workflows", name)), -1)
		for _, line := range lines {
			inspected++
			if strings.HasSuffix(line[1], "-latest") {
				t.Errorf("%s runs on %q, an image GitHub replaces under it; name the version instead", name, line[1])
			}
		}
	}
	if inspected == 0 {
		t.Fatal("no runs-on value was inspected; the pattern no longer matches these workflows")
	}
}
