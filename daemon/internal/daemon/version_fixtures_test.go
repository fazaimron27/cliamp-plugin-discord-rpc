package daemon

// This file holds the shared release-line fixtures: newerLine and olderLine,
// which derive a plugin version one step either side of the daemon's own, and
// shiftLine, the arithmetic under them, pinned at the major boundary where that
// arithmetic is easiest to get wrong.
//
// The fixtures live here rather than beside one of their users, because more
// than one test file needs a release version at a known distance from the
// daemon's own: the run loop's version watch in daemon_test.go, the --check
// diagnostic in check_test.go, and the release report in release_test.go.

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/version"
)

// newerLine and olderLine derive a release line one step from the daemon's own
// instead of hardcoding one, so a version bump cannot leave a fixture asserting
// a pairing whose sides have since moved apart.
func newerLine(t *testing.T) string {
	t.Helper()
	return mustShiftLine(t, version.Number, 1)
}

func olderLine(t *testing.T) string {
	t.Helper()
	return mustShiftLine(t, version.Number, -1)
}

// patchAheadLine derives the patch release one step above the daemon's own, as
// a v-prefixed tag: the nearest release the diagnostic exists to notice, and
// the one a minor step cannot express.
//
// The patch component is read from the daemon's own release rather than written
// out, because a hardcoded one collapses to that release the first time the
// release is itself a patch. The bump to v1.12.1 is what found this: the
// fixture still rendered v1.12.1, so the case asserting a newer patch was
// asserting an equal release instead, and only the assertion it carried said so.
func patchAheadLine(t *testing.T) string {
	t.Helper()
	fields := strings.SplitN(version.Number, ".", 3)
	if len(fields) != 3 {
		t.Fatalf("release %q is not major.minor.patch", version.Number)
	}
	patch, err := strconv.Atoi(fields[2])
	if err != nil {
		t.Fatalf("release %q has a non-numeric patch: %v", version.Number, err)
	}
	return fmt.Sprintf("v%s.%s.%d", fields[0], fields[1], patch+1)
}

func mustShiftLine(t *testing.T, value string, delta int) string {
	t.Helper()
	shifted, err := shiftLine(value, delta)
	if err != nil {
		t.Fatalf("shift %q by %d lines: %v", value, delta, err)
	}
	return shifted
}

// shiftLine returns the release line delta minors away from value, rendered as a
// major.minor.0 version. It is a function of its argument rather than of the
// daemon's own release so the boundary cases — which a release only reaches once
// every few years — can be asserted directly.
//
// There is no minor below 0, and the previous major's *last* minor is not
// something the value itself can say. Stepping back a whole major instead lands
// on that major's first line: not adjacent, but unambiguously older, which is
// the only property the callers need.
func shiftLine(value string, delta int) (string, error) {
	fields := strings.SplitN(value, ".", 3)
	if len(fields) < 2 {
		return "", fmt.Errorf("%q is not major.minor", value)
	}
	major, err := strconv.Atoi(fields[0])
	if err != nil {
		return "", fmt.Errorf("%q has a non-numeric major: %w", value, err)
	}
	minor, err := strconv.Atoi(fields[1])
	if err != nil {
		return "", fmt.Errorf("%q has a non-numeric minor: %w", value, err)
	}
	if minor+delta < 0 {
		major--
		minor = 0
	} else {
		minor += delta
	}
	if major < 0 {
		return "", fmt.Errorf("%q has no release line below it", value)
	}
	return fmt.Sprintf("%d.%d.0", major, minor), nil
}

// A version fixture that renders arithmetic going negative hands a test a string
// no release ever carries, so the test asserts a real relation on an impossible
// input and passes. The major boundary is both where that happens and the
// release most likely to move the line, so it is the case worth pinning.
func TestShiftLineHoldsAtMajorBoundaries(t *testing.T) {
	tests := []struct {
		value string
		delta int
		want  string
	}{
		{"1.8.0", 1, "1.9.0"},
		{"1.8.0", -1, "1.7.0"},
		{"1.9.0", 1, "1.10.0"},
		{"1.10.0", -1, "1.9.0"},
		{"2.0.0", -1, "1.0.0"},
		{"2.0.0", 1, "2.1.0"},
		{"2.1.0", -1, "2.0.0"},
		{"1.0.0", -1, "0.0.0"},
	}
	for _, test := range tests {
		got, err := shiftLine(test.value, test.delta)
		if err != nil {
			t.Errorf("shiftLine(%q, %d) returned %v, want %q", test.value, test.delta, err, test.want)
			continue
		}
		if got != test.want {
			t.Errorf("shiftLine(%q, %d) = %q, want %q", test.value, test.delta, got, test.want)
		}
	}
}

// Below 0.0.0 there is nothing, so the fixture must fail loudly rather than
// invent a version.
func TestShiftLineRefusesAReleaseLineWithNothingBelowIt(t *testing.T) {
	if got, err := shiftLine("0.0.0", -1); err == nil {
		t.Fatalf("shiftLine(\"0.0.0\", -1) = %q, want an error", got)
	}
}
