package daemon

// This file tests the self-update sequence with every outside world replaced:
// the commands it would run, the PATH they would be found on, and the release it
// would fetch. The exact argv and the exact order are the feature, so they are
// what is asserted.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/config"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/release"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/version"
)

// recorded is one command as the sequence ran it, kept with the directory it ran
// in, because that directory is load-bearing rather than incidental.
type recorded struct {
	dir  string
	name string
	args []string
}

// recorder is the runner these tests drive: it records every command in the order
// it was asked for one, and answers with an error at the call a test names.
type recorder struct {
	mu       sync.Mutex
	calls    []recorded
	failAt   int
	failWith error
}

// Run records the command and fails when it is the one a test named.
func (r *recorder) Run(ctx context.Context, dir, name string, args ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, recorded{dir: dir, name: name, args: args})
	if r.failAt > 0 && len(r.calls) == r.failAt {
		return r.failWith
	}
	return nil
}

// commands renders each recorded call as one string, so a test can compare the
// whole sequence in one assertion.
func (r *recorder) commands() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	rendered := make([]string, 0, len(r.calls))
	for _, call := range r.calls {
		rendered = append(rendered, strings.Join(append([]string{call.name}, call.args...), " "))
	}
	return rendered
}

// dirs returns each recorded call's working directory, in the same order.
func (r *recorder) dirs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	rendered := make([]string, 0, len(r.calls))
	for _, call := range r.calls {
		rendered = append(rendered, call.dir)
	}
	return rendered
}

// count returns how many commands were run.
func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// fakeSource is the release host these tests drive: what it would answer, and
// which questions were actually put to it. A test that expects no lookup asserts
// on asked being empty rather than trusting that none happened.
type fakeSource struct {
	latest string
	tags   []string
	script []byte
	err    error
	asked  []string
}

// Latest answers with the newest tag, or the error the fake holds.
func (f *fakeSource) Latest(ctx context.Context) (string, error) {
	f.asked = append(f.asked, "Latest")
	if f.err != nil {
		return "", f.err
	}
	return f.latest, nil
}

// Tags answers with the released tags, or the error the fake holds.
func (f *fakeSource) Tags(ctx context.Context) ([]string, error) {
	f.asked = append(f.asked, "Tags")
	if f.err != nil {
		return nil, f.err
	}
	return f.tags, nil
}

// Script answers with the installer body, recording the tag it was asked for.
func (f *fakeSource) Script(ctx context.Context, tag string) ([]byte, error) {
	f.asked = append(f.asked, "Script "+tag)
	if f.err != nil {
		return nil, f.err
	}
	return f.script, nil
}

// lookups returns the questions that ask GitHub which release to install,
// excluding the installer fetch, which every run that gets as far as installing
// makes. An explicit tag is meant to skip the question, not the download.
func (f *fakeSource) lookups() []string {
	var asked []string
	for _, question := range f.asked {
		if question == "Latest" || question == "Tags" {
			asked = append(asked, question)
		}
	}
	return asked
}

// lookPathWithout answers as a PATH holding every command but the named ones.
func lookPathWithout(missing ...string) func(string) (string, error) {
	absent := make(map[string]bool, len(missing))
	for _, name := range missing {
		absent[name] = true
	}
	return func(name string) (string, error) {
		if absent[name] {
			return "", errors.New("executable file not found in $PATH")
		}
		return "/usr/bin/" + name, nil
	}
}

// harness is the whole sequence with every outside world replaced, plus the
// terminal it writes to.
type harness struct {
	deps upgradeDeps
	out  bytes.Buffer
	run  *recorder
	src  *fakeSource
}

// newHarness returns a harness whose release host and PATH are entirely under the
// test's control.
func newHarness() *harness {
	h := &harness{
		run: &recorder{failWith: errors.New("exit status 1")},
		src: &fakeSource{script: []byte("#!/bin/sh\n")},
	}
	h.deps = upgradeDeps{
		runner:   h.run,
		lookPath: lookPathWithout(),
		releases: h.src,
		binary:   "/home/user/.local/bin/cliamp-rpcd",
	}
	return h
}

// upgrade runs the sequence and returns its exit code.
func (h *harness) upgrade(mode upgradeMode, cfg config.Config) int {
	return upgrade(context.Background(), cfg, mode, h.deps, &h.out)
}

// setBinDir makes the environment name a bin directory, which is what
// CLIAMP_RPC_BIN_DIR does in the exported commands.
func (h *harness) setBinDir(dir string) {
	h.deps.binDir = dir
}

// The installer runs in a directory holding the script and nothing else, with the
// tag and the bin directory named on its command line. That directory is the
// whole design: install.sh installs what sits beside it without verifying
// anything, so the script must be run where nothing sits beside it.
func TestUpdateInstallsTheDaemonHalf(t *testing.T) {
	h := newHarness()
	h.deps.binDir = "/home/user/.local/bin"
	h.src.latest = "v1.12.0"

	code := h.upgrade(modeUpdate, config.Config{})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0:\n%s", code, h.out.String())
	}
	want := "sh " + filepath.Join(h.run.dirs()[0], "install.sh") + " --version v1.12.0 --bin-dir /home/user/.local/bin"
	if got := h.run.commands(); got[0] != want {
		t.Fatalf("commands = %v, want the installer first: %s", got, want)
	}
	if h.run.dirs()[0] == "" {
		t.Fatal("the installer ran with no working directory, so it could have found a binary beside it")
	}
	if !strings.Contains(h.out.String(), "installing v1.12.0 into /home/user/.local/bin") {
		t.Fatalf("output does not name the directory it is installing into:\n%s", h.out.String())
	}
	if !strings.Contains(h.out.String(), "updated cliamp-rpcd v"+version.Number+" -> v1.12.0") {
		t.Fatalf("output omits the update:\n%s", h.out.String())
	}
}

// The running binary's own directory is the default, so a user who put the daemon
// somewhere else has that copy replaced rather than a second one installed into
// the documented location.
func TestUpdateInstallsIntoTheRunningBinarysDirectory(t *testing.T) {
	h := newHarness()
	h.src.latest = "v1.12.0"

	h.upgrade(modeUpdate, config.Config{})

	want := "sh " + filepath.Join(h.run.dirs()[0], "install.sh") + " --version v1.12.0 --bin-dir /home/user/.local/bin"
	if got := h.run.commands(); got[0] != want {
		t.Fatalf("commands = %v, want %s", got, want)
	}
}

// The environment variable wins over the running binary's directory, matching
// install.sh's own precedence.
func TestUpdateHonorsTheBinDirOverride(t *testing.T) {
	h := newHarness()
	h.setBinDir("/opt/cliamp/bin")
	h.src.latest = "v1.12.0"

	h.upgrade(modeUpdate, config.Config{})

	want := "sh " + filepath.Join(h.run.dirs()[0], "install.sh") + " --version v1.12.0 --bin-dir /opt/cliamp/bin"
	if got := h.run.commands(); got[0] != want {
		t.Fatalf("commands = %v, want %s", got, want)
	}
}

// The common case rather than an edge one: version.Number is the last released
// version, so every build off main between releases takes this path. Nothing is
// fetched and nothing is installed.
func TestUpdateDoesNothingWhenThisIsAlreadyTheNewestRelease(t *testing.T) {
	h := newHarness()
	h.src.latest = "v" + version.Number

	code := h.upgrade(modeUpdate, config.Config{})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0:\n%s", code, h.out.String())
	}
	if h.run.count() != 0 {
		t.Fatalf("commands = %v, want none", h.run.commands())
	}
	if !strings.Contains(h.out.String(), "is already the newest release") {
		t.Fatalf("output does not say the release is current:\n%s", h.out.String())
	}
}

// An explicit tag is how a version is pinned or a downgrade made on purpose, so
// it is obeyed without asking GitHub anything and without a newer-than test.
// A bare version is the same request: version.Normalize drops the "v" and
// install.sh is handed the "v" back, because it refuses a version without one.
func TestUpdateObeysAnExplicitTagWithoutAsking(t *testing.T) {
	for _, given := range []string{"v1.10.1", "1.10.1", " v1.10.1 "} {
		t.Run(given, func(t *testing.T) {
			h := newHarness()
			h.src.latest = "v9.9.9"

			code := h.upgrade(modeUpdate, config.Config{ReleaseTag: given})

			if code != 0 {
				t.Fatalf("exit code = %d, want 0:\n%s", code, h.out.String())
			}
			if len(h.src.lookups()) != 0 {
				t.Fatalf("the release host was asked %v, want no lookup", h.src.lookups())
			}
			want := "sh " + filepath.Join(h.run.dirs()[0], "install.sh") + " --version v1.10.1 --bin-dir /home/user/.local/bin"
			if got := h.run.commands(); got[0] != want {
				t.Fatalf("commands = %v, want %s", got, want)
			}
		})
	}
}

// A tag that names no release is refused before anything is asked or run, so the
// answer is immediate rather than a failed download two steps later. PATH is
// deliberately broken here as well: the refusal must come from the tag check,
// which runs first, and not from the preflight that follows it.
func TestUpdateRefusesATagThatIsNotARelease(t *testing.T) {
	h := newHarness()
	h.deps.lookPath = lookPathWithout("gh")

	code := h.upgrade(modeUpdate, config.Config{ReleaseTag: "nightly"})

	if code == 0 {
		t.Fatalf("exit code = 0 for a tag that is not a release:\n%s", h.out.String())
	}
	if len(h.src.asked) != 0 || h.run.count() != 0 {
		t.Fatalf("a bad tag reached %v and ran %v", h.src.asked, h.run.commands())
	}
	if !strings.Contains(h.out.String(), "not a release tag: nightly") {
		t.Fatalf("output does not name the tag:\n%s", h.out.String())
	}
	if strings.Contains(h.out.String(), "required command not found") {
		t.Fatalf("the tag was checked after the preflight:\n%s", h.out.String())
	}
}

// The tools are checked before the first request, so a missing one is one
// sentence naming it rather than curl failing from inside a child process.
func TestUpdateRefusesBeforeAskingWhenAToolIsMissing(t *testing.T) {
	for _, missing := range []string{"sh", "curl", "gh", "install"} {
		t.Run(missing, func(t *testing.T) {
			h := newHarness()
			h.deps.lookPath = lookPathWithout(missing)

			code := h.upgrade(modeUpdate, config.Config{})

			if code == 0 {
				t.Fatalf("exit code = 0 without %s:\n%s", missing, h.out.String())
			}
			if len(h.src.asked) != 0 || h.run.count() != 0 {
				t.Fatalf("a missing tool still reached %v and ran %v", h.src.asked, h.run.commands())
			}
			if !strings.Contains(h.out.String(), "required command not found: "+missing) {
				t.Fatalf("output does not name %s:\n%s", missing, h.out.String())
			}
		})
	}
}

// A release host that cannot answer, and one whose installer for the tag does not
// exist, both stop the run before a child process exists. The two are different
// requests reached by different paths, so they are two cases: the first never gets
// a tag to install, and the second fails after the tag is known.
func TestUpdateStopsWhenTheReleaseCannotBeFetched(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
	}{
		{"the newest release cannot be looked up", config.Config{}},
		{"the installer cannot be fetched", config.Config{ReleaseTag: "v1.12.0"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness()
			h.src.err = errors.New("installer download failed")

			code := h.upgrade(modeUpdate, test.cfg)

			if code == 0 {
				t.Fatalf("exit code = 0 for an unreachable release:\n%s", h.out.String())
			}
			if h.run.count() != 0 {
				t.Fatalf("commands = %v, want none", h.run.commands())
			}
		})
	}
}

// The rollback lookup has the same two failure points, reached through Tags.
func TestRollbackStopsWhenTheReleaseCannotBeFetched(t *testing.T) {
	h := newHarness()
	h.src.err = errors.New("release feed lookup failed")

	code := h.upgrade(modeRollback, config.Config{})

	if code == 0 {
		t.Fatalf("exit code = 0 for an unreachable feed:\n%s", h.out.String())
	}
	if h.run.count() != 0 {
		t.Fatalf("commands = %v, want none", h.run.commands())
	}
}

// A failed install stops the run and installs nothing more: install.sh verifies
// before it writes, so a refusal leaves the previous binary where it was. This is
// also what a read-only $HOME produces when --update is run from inside the unit.
func TestUpdateStopsWhenTheInstallerFails(t *testing.T) {
	h := newHarness()
	h.src.latest = "v1.12.0"
	h.run.failAt = 1

	code := h.upgrade(modeUpdate, config.Config{})

	if code == 0 {
		t.Fatalf("exit code = 0 after a failed install:\n%s", h.out.String())
	}
	if h.run.count() != 1 {
		t.Fatalf("commands = %v, want the installer alone", h.run.commands())
	}
	if strings.Contains(h.out.String(), "updated cliamp-rpcd") {
		t.Fatalf("a failed install was reported as an update:\n%s", h.out.String())
	}
}

// A release moves both halves, so one command leaves a matched pair rather than
// the mismatch this project's version check exists to warn about. The plugin is
// installed before the daemon is restarted, so the restarted daemon never comes
// up against a half-updated pair.
//
// The installed plugin is removed first, and there is no separate trust step:
// cliamp's install refuses a plugin that is already there, and records the trust
// itself once the user approves its prompt.
func TestUpdateDrivesThePluginHalfAndTheRestart(t *testing.T) {
	h := newHarness()
	h.src.latest = "v1.12.0"

	code := h.upgrade(modeUpdate, config.Config{})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0:\n%s", code, h.out.String())
	}
	want := []string{
		"sh " + filepath.Join(h.run.dirs()[0], "install.sh") + " --version v1.12.0 --bin-dir /home/user/.local/bin",
		"cliamp plugins remove discord-rpc",
		"cliamp plugins install " + release.Repository + "@v1.12.0",
		"systemctl --user try-restart cliamp-rpcd.service",
	}
	got := h.run.commands()
	if len(got) != len(want) {
		t.Fatalf("commands = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("command %d = %q, want %q", index, got[index], want[index])
		}
	}
	for _, line := range []string{
		"updated cliamp-rpcd v" + version.Number + " -> v1.12.0",
		"plugin updated to v1.12.0",
		"restarted cliamp-rpcd.service",
		"to go back: cliamp-rpcd --rollback",
	} {
		if !strings.Contains(h.out.String(), line) {
			t.Fatalf("output omits %q:\n%s", line, h.out.String())
		}
	}
}

// install.sh leaves the unit installed, disabled, and stopped, and a plain
// restart would start it for someone who deliberately runs the daemon in a
// terminal — possibly while that foreground daemon holds the socket.
func TestUpdateUsesTryRestart(t *testing.T) {
	h := newHarness()
	h.src.latest = "v1.12.0"

	h.upgrade(modeUpdate, config.Config{})

	restartCommand := h.run.commands()[3]
	if restartCommand != "systemctl --user try-restart cliamp-rpcd.service" {
		t.Fatalf("restart command = %q, want try-restart", restartCommand)
	}
}

// A missing cliamp leaves the daemon half installed and the pair mismatched, so
// the run is not a success: it names both commands for the user to run, and it
// stops before the restart rather than bringing the new daemon up against the
// plugin it no longer matches. It still names the way back, because the half it
// did install is the half the user might want undone.
func TestUpdateReportsAMissingCliampAndFails(t *testing.T) {
	h := newHarness()
	h.src.latest = "v1.12.0"
	h.deps.lookPath = lookPathWithout("cliamp")

	code := h.upgrade(modeUpdate, config.Config{})

	if code == 0 {
		t.Fatalf("exit code = 0 with no plugin half installed:\n%s", h.out.String())
	}
	if got := h.run.commands(); len(got) != 1 {
		t.Fatalf("commands = %v, want the installer alone", got)
	}
	for _, want := range []string{
		"cliamp plugins remove discord-rpc",
		"cliamp plugins install " + release.Repository + "@v1.12.0",
		"to go back: cliamp-rpcd --rollback",
	} {
		if !strings.Contains(h.out.String(), want) {
			t.Fatalf("output omits %q:\n%s", want, h.out.String())
		}
	}
	if strings.Contains(h.out.String(), "plugins trust") {
		t.Fatalf("output names a separate trust step:\n%s", h.out.String())
	}
}

// A restart that cannot be run is a warning and nothing more. try-restart cannot
// tell "nothing was running" from "restarted", and a machine with no systemd at
// all is doing nothing wrong, so the two halves that were installed stay a
// success.
func TestUpdateWarnsButSucceedsWhenTheRestartCannotRun(t *testing.T) {
	tests := []struct {
		name     string
		lookPath func(string) (string, error)
		failAt   int
	}{
		{"no systemctl on PATH", lookPathWithout("systemctl"), 0},
		{"systemctl refused", lookPathWithout(), 4},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness()
			h.src.latest = "v1.12.0"
			h.deps.lookPath = test.lookPath
			h.run.failAt = test.failAt

			code := h.upgrade(modeUpdate, config.Config{})

			if code != 0 {
				t.Fatalf("exit code = %d, want 0:\n%s", code, h.out.String())
			}
			if !strings.Contains(h.out.String(), "cliamp-rpcd.service was not restarted") {
				t.Fatalf("output does not warn about the restart:\n%s", h.out.String())
			}
		})
	}
}

// Every step stops the sequence: a failure at one leaves every later step
// untouched, so a half-updated machine is never left mid-sequence by this
// program.
func TestUpdateStopsAtTheFirstFailedStep(t *testing.T) {
	tests := []struct {
		name    string
		failAt  int
		want    int
		omitted string
	}{
		{"the installer", 1, 1, "plugin updated"},
		{"the plugin install", 3, 1, "restarted cliamp-rpcd.service"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness()
			h.src.latest = "v1.12.0"
			h.run.failAt = test.failAt

			code := h.upgrade(modeUpdate, config.Config{})

			if code == 0 {
				t.Fatalf("exit code = 0 after %s failed:\n%s", test.name, h.out.String())
			}
			if h.run.count() != test.failAt {
				t.Fatalf("commands = %v, want %d of them", h.run.commands(), test.failAt)
			}
			if strings.Contains(h.out.String(), test.omitted) {
				t.Fatalf("output claims %q after %s failed:\n%s", test.omitted, test.name, h.out.String())
			}
		})
	}
}

// A failed plugin install is reported with the command that finishes the job by
// hand. That command needs no removal of its own: the old copy was taken away
// before the install was attempted.
func TestUpdateNamesTheInstallCommandWhenThePluginInstallFails(t *testing.T) {
	h := newHarness()
	h.src.latest = "v1.12.0"
	h.run.failAt = 3

	h.upgrade(modeUpdate, config.Config{})

	want := "Run: cliamp plugins install " + release.Repository + "@v1.12.0"
	if !strings.Contains(h.out.String(), want) {
		t.Fatalf("output omits %q:\n%s", want, h.out.String())
	}
}

// Removing the old plugin is best effort. Install is the step that has to
// succeed, and on a machine with no plugin the removal fails for the ordinary
// reason that there is nothing to remove — which is not a failure of the update.
func TestUpdateInstallsThePluginEvenWhenTheRemovalFails(t *testing.T) {
	h := newHarness()
	h.src.latest = "v1.12.0"
	h.run.failAt = 2

	code := h.upgrade(modeUpdate, config.Config{})

	if code != 0 {
		t.Fatalf("exit code = %d after a failed removal, want 0:\n%s", code, h.out.String())
	}
	if h.run.count() != 4 {
		t.Fatalf("commands = %v, want all four", h.run.commands())
	}
	for _, line := range []string{"plugin updated to v1.12.0", "restarted cliamp-rpcd.service"} {
		if !strings.Contains(h.out.String(), line) {
			t.Fatalf("output omits %q:\n%s", line, h.out.String())
		}
	}
}

// Rolling back is the same sequence with a tag chosen from the other end of the
// list: the newest tag strictly older than this daemon.
func TestRollbackStepsDownOneRelease(t *testing.T) {
	h := newHarness()
	h.src.tags = []string{"v1.12.0", "v1.11.0", "v1.10.1", "nightly", "v1.12.0-rc1"}

	code := h.upgrade(modeRollback, config.Config{})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0:\n%s", code, h.out.String())
	}
	want := "sh " + filepath.Join(h.run.dirs()[0], "install.sh") + " --version v1.10.1 --bin-dir /home/user/.local/bin"
	if got := h.run.commands(); got[0] != want {
		t.Fatalf("commands = %v, want %s", got, want)
	}
}

// A tag newer than this daemon is never chosen by the derivation, which is the
// rule that stops a stale machine being jumped forward to the oldest release
// GitHub still lists.
func TestRollbackRefusesToStepForward(t *testing.T) {
	h := newHarness()
	h.src.tags = []string{"v1.12.0", "v" + version.Number}

	code := h.upgrade(modeRollback, config.Config{})

	if code == 0 {
		t.Fatalf("exit code = 0 with no older release:\n%s", h.out.String())
	}
	if h.run.count() != 0 {
		t.Fatalf("commands = %v, want none", h.run.commands())
	}
	if !strings.Contains(h.out.String(), "no release older than v"+version.Number+" is listed") {
		t.Fatalf("output does not explain the refusal:\n%s", h.out.String())
	}
}

// An explicit tag overrides the derivation in both directions, including this
// one, where it names something newer.
func TestRollbackObeysAnExplicitTag(t *testing.T) {
	h := newHarness()
	h.src.tags = []string{"v1.10.1"}

	code := h.upgrade(modeRollback, config.Config{ReleaseTag: "v1.12.0"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0:\n%s", code, h.out.String())
	}
	if len(h.src.lookups()) != 0 {
		t.Fatalf("the release host was asked %v, want no lookup", h.src.lookups())
	}
	want := "sh " + filepath.Join(h.run.dirs()[0], "install.sh") + " --version v1.12.0 --bin-dir /home/user/.local/bin"
	if got := h.run.commands(); got[0] != want {
		t.Fatalf("commands = %v, want %s", got, want)
	}
}

// previousTag is the whole rollback policy, so it is tested directly as well as
// through the sequence.
func TestPreviousTag(t *testing.T) {
	tests := []struct {
		name    string
		tags    []string
		current string
		want    string
		found   bool
	}{
		{"the newest of several older ones", []string{"v1.12.0", "v1.11.0", "v1.10.1"}, "1.11.0", "v1.10.1", true},
		{"an older patch is older", []string{"v1.11.0", "v1.10.2"}, "1.11.0", "v1.10.2", true},
		{"the same release is not older", []string{"v1.11.0"}, "1.11.0", "", false},
		{"a newer release is not older", []string{"v1.12.0"}, "1.11.0", "", false},
		{"nothing listed", nil, "1.11.0", "", false},
		{"unorderable entries are skipped", []string{"nightly", "v1.12.0-rc1"}, "1.11.0", "", false},
		{"order in the list does not matter", []string{"v1.10.1", "v1.12.0", "v1.10.2"}, "1.11.0", "v1.10.2", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, found := previousTag(test.tags, test.current)
			if got != test.want || found != test.found {
				t.Fatalf("previousTag(%v, %q) = %q, %v; want %q, %v", test.tags, test.current, got, found, test.want, test.found)
			}
		})
	}
}

// The stub installer writes down where it was run from, what it was called as,
// and what it was passed. It is the evidence the assertions below read.
const stubInstaller = `#!/bin/sh
{
  pwd
  printf '%s\n' "$0"
  printf '%s\n' "$*"
  ls -A
} > "$EVIDENCE"
`

// The one claim a fake runner cannot make: that the fetched installer runs, in a
// directory holding the script and nothing else. install.sh installs what sits
// beside it without verifying anything, so the second half of that is the whole
// design — and it depends on the script's own path as well as the child's working
// directory, which is why both are asserted.
//
// cliamp and systemctl are deliberately absent from PATH: neither half of the
// rest of the sequence should run a real command from a test.
func TestUpdateRunsTheInstallerAloneInItsOwnDirectory(t *testing.T) {
	evidence := filepath.Join(t.TempDir(), "evidence")
	t.Setenv("EVIDENCE", evidence)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, stubInstaller)
	}))
	defer server.Close()

	h := newHarness()
	h.deps.releases = release.New(release.WithRawURL(server.URL))
	h.deps.runner = execRunner{out: &h.out}
	h.deps.lookPath = lookPathWithout("cliamp", "systemctl")

	h.upgrade(modeUpdate, config.Config{ReleaseTag: "v1.12.0"})

	body, err := os.ReadFile(evidence)
	if err != nil {
		t.Fatalf("the installer did not run: %v\n%s", err, h.out.String())
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) < 4 {
		t.Fatalf("the installer recorded %q, want a directory, a path, arguments, and a listing", lines)
	}
	directory, calledAs, arguments := lines[0], lines[1], lines[2]
	if filepath.Dir(calledAs) != directory {
		t.Fatalf("the installer was run as %q from %q, want the script inside the directory it ran in", calledAs, directory)
	}
	if arguments != "--version v1.12.0 --bin-dir /home/user/.local/bin" {
		t.Fatalf("arguments = %q", arguments)
	}
	if listing := strings.Join(lines[3:], "\n"); listing != "install.sh" {
		t.Fatalf("the installer's directory holds %q, want the script alone", listing)
	}
	if strings.HasPrefix(directory, "/home/user") {
		t.Fatalf("the installer ran in %q, want a temporary directory", directory)
	}
}

// A child must die with the context rather than outliving the terminal that
// started it, which is what Ctrl-C during an update is.
func TestExecRunnerRefusesACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := execRunner{out: io.Discard}.Run(ctx, "", "sh", "-c", "true")
	if err == nil {
		t.Fatal("a cancelled context started a child process")
	}
}
