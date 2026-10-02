package diag_test

// This file tests the two things the seam promises. One is that a component's
// name reaches every line it writes, which is the attribution the daemon's
// journal did not have before it. The other is that a discarding logger really
// writes nothing, since that is what lets the --check report silence a probe
// without redirecting the process-wide log.

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/diag"
)

// A line must arrive carrying the name of the component that wrote it, because
// that attribution is the whole reason the seam exists: before it, a Discord
// line and a daemon line were the same shape in the journal.
func TestNewPrefixesEachLineWithItsComponent(t *testing.T) {
	var out bytes.Buffer
	diag.New(&out, "discord").Printf("connected to Discord at %s", "/run/socket")

	if got := out.String(); !strings.Contains(got, "discord: connected to Discord at /run/socket") {
		t.Errorf("line = %q, want it to name the component that wrote it", got)
	}
}

// Two components writing to one writer must keep their own names, which is the
// arrangement Run builds: one writer for the process, one logger per component
// sharing it. A single shared prefix would make the journal legible about the
// process and silent about the part that failed.
func TestTwoComponentsShareAWriterWithoutSharingAName(t *testing.T) {
	var out bytes.Buffer
	diag.New(&out, "daemon").Printf("%s", "starting cliamp-rpcd")
	diag.New(&out, "cliamp").Printf("discarding undecodable frame: %v", errors.New("unexpected EOF"))

	got := out.String()
	for _, want := range []string{
		"daemon: starting cliamp-rpcd",
		"cliamp: discarding undecodable frame: unexpected EOF",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log = %q, want it to contain %q", got, want)
		}
	}
}

// Discard is handed to probes whose output would land beside a report that
// already says the same thing, so the property that matters is that the line
// never reaches the writer it was given.
func TestDiscardWritesNothing(t *testing.T) {
	var out bytes.Buffer
	diag.Discard().Printf("connect to Discord: %v", errors.New("refused"))

	if got := out.String(); got != "" {
		t.Errorf("discarding logger wrote %q, want nothing", got)
	}
}
