package daemon

// This file is the self-update: one command that installs a newer release, and
// one that installs the release before it.
//
// Nothing here runs on its own and nothing here runs inside the unit. The unit
// is hardened with ProtectHome=read-only, so a daemon started by it cannot write
// the binary it would be replacing; installing is a process the user starts, and
// this program is that process when it is given --update.
//
// The installing itself is not implemented here. The target release's own
// install.sh is fetched and run, so the download, attestation, and checksum
// rules have one implementation rather than two that can drift.

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/config"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/release"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/version"
)

// upgradeMode names which of the two commands is running. The tag is chosen
// differently for each and every step after that is the same.
type upgradeMode int

const (
	modeUpdate upgradeMode = iota
	modeRollback
)

// releaseSource is the slice of the release package the update path needs.
type releaseSource interface {
	Latest(context.Context) (string, error)
	Tags(context.Context) ([]string, error)
	Script(context.Context, string) ([]byte, error)
}

// runner runs one child process. The real one is execRunner; the tests replace
// it, because the commands and their order are the feature.
type runner interface {
	Run(ctx context.Context, dir, name string, args ...string) error
}

// upgradeDeps is everything the sequence reaches outside itself for, so a test
// can supply all of it.
type upgradeDeps struct {
	runner   runner
	lookPath func(string) (string, error)
	releases releaseSource
	binary   string
	binDir   string
}

// upgradeTools are the commands a release install needs: the seven install.sh
// checks on the download branch it will take, plus install, which it checks
// unconditionally, and sh, which this program invokes itself.
var upgradeTools = []string{"sh", "awk", "curl", "gh", "sha256sum", "tar", "uname", "mktemp", "install"}

// previousTag returns the newest tag in tags that is strictly older than current,
// reporting false when there is none.
//
// Strictly older is version.Newer with the arguments the other way round, and
// "newest" is the same comparison over the candidates, so no ordering rule is
// written here that could disagree with the one the release check uses.
//
// The list's order is deliberately not used. GitHub publishes the feed newest
// first, but a rule that read position instead of comparing versions would pick a
// different release the day that changed.
func previousTag(tags []string, current string) (string, bool) {
	best := ""
	for _, candidate := range tags {
		if !version.Newer(candidate, current) {
			continue
		}
		if best == "" || version.Newer(best, candidate) {
			best = candidate
		}
	}
	return best, best != ""
}

// missingTools returns the first command in upgradeTools that PATH does not
// hold, or an empty string when it holds them all.
//
// Checking here rather than letting install.sh fail means a missing command is
// one sentence naming it, before the first request, instead of an error from
// inside a child process after the update has begun.
func missingTools(lookPath func(string) (string, error)) string {
	for _, name := range upgradeTools {
		if _, err := lookPath(name); err != nil {
			return name
		}
	}
	return ""
}

// explicitTag validates a tag named on the command line, returning the normalized
// release line and whether it may be used.
//
// It is separate from resolveTarget because it must run before the preflight, and
// resolveTarget must run after it. The spec's order is deliberate: a typo in the
// tag is answered immediately, rather than after a PATH complaint about a command
// that a valid run would never have reached.
//
// An explicit tag is obeyed as given, including when it is not newer than this
// daemon: pinning a version and stepping back on purpose are the same request,
// and second-guessing either would make --rollback's explicit form useless.
func explicitTag(given string, out io.Writer) (string, bool) {
	if given == "" {
		return "", true
	}
	target := version.Normalize(given)
	if !version.IsVersion(target) {
		fmt.Fprintf(out, "not a release tag: %s\n", given)
		return "", false
	}
	return target, true
}

// resolveTarget asks GitHub which release to install when the user named none.
// It is the only request the tag step makes, and it is reached after the
// preflight has already passed. It reports stop when the run is over before
// anything was installed, with the code to return.
func resolveTarget(ctx context.Context, mode upgradeMode, releases releaseSource, out io.Writer) (string, bool, int) {
	if mode == modeUpdate {
		latest, err := releases.Latest(ctx)
		if err != nil {
			fmt.Fprintf(out, "could not ask GitHub for the newest release: %v\n", err)
			return "", true, 1
		}
		target := version.Normalize(latest)
		if !version.Newer(version.Number, target) {
			fmt.Fprintf(out,
				"v%s is already the newest release; nothing to update. To reinstall it: cliamp-rpcd --update v%s\n",
				version.Number, version.Number)
			return "", true, 0
		}
		return target, false, 0
	}

	tags, err := releases.Tags(ctx)
	if err != nil {
		fmt.Fprintf(out, "could not ask GitHub which releases exist: %v\n", err)
		return "", true, 1
	}
	previous, ok := previousTag(tags, version.Number)
	if !ok {
		fmt.Fprintf(out,
			"no release older than v%s is listed for %s; name one with: cliamp-rpcd --rollback <tag>\n",
			version.Number, release.Repository)
		return "", true, 1
	}
	return version.Normalize(previous), false, 0
}

// installRelease fetches the target release's own installer and runs it.
//
// The installed tag and the bin directory are passed on the command line, and
// the script is written into a fresh temporary directory that it is run from.
// That directory is the point of the whole arrangement: install.sh installs what
// sits beside it without verifying anything, so the only safe place to run it is
// somewhere nothing sits beside it. The script's absolute path is passed as well
// as the directory, so the branch it takes depends on where the script is rather
// than on the child honoring its working directory.
func installRelease(ctx context.Context, target, binDir string, deps upgradeDeps, out io.Writer) int {
	script, err := deps.releases.Script(ctx, tag(target))
	if err != nil {
		fmt.Fprintf(out, "could not fetch the installer for %s: %v\n", tag(target), err)
		return 1
	}
	dir, err := os.MkdirTemp("", "cliamp-rpc-update.")
	if err != nil {
		fmt.Fprintf(out, "could not create a working directory: %v\n", err)
		return 1
	}
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "install.sh")
	if err := os.WriteFile(path, script, 0o600); err != nil {
		fmt.Fprintf(out, "could not write the installer: %v\n", err)
		return 1
	}
	if err := deps.runner.Run(ctx, dir, "sh", path, "--version", tag(target), "--bin-dir", binDir); err != nil {
		fmt.Fprintf(out, "the installer failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "updated cliamp-rpcd v%s -> %s\n", version.Number, tag(target))
	return 0
}

// pluginName is the plugin's name inside Cliamp, which is what trust takes.
const pluginName = "discord-rpc"

// serviceName is the systemd user unit a release installs.
const serviceName = "cliamp-rpcd.service"

// restartWarning is what a restart that could not be run leaves behind.
const restartWarning = "cliamp-rpcd.service was not restarted: run systemctl --user try-restart cliamp-rpcd.service, or restart your daemon yourself"

// installPlugin updates the plugin half through Cliamp.
//
// It is driven rather than described because one command should leave a matched
// pair, and because the daemon half has already been replaced by the time this
// runs: leaving the plugin behind would produce exactly the mismatch the version
// check warns about, from the command that was supposed to prevent it.
//
// The installed plugin is removed first, because Cliamp's install refuses a
// plugin that is already there and offers no way to overwrite one. That removal
// is best effort: install is the step that has to succeed, and on a machine with
// no plugin yet the removal fails for the ordinary reason that there is nothing
// to remove. It is best effort for a second reason too, which is why a failed
// install is reported with the install command — the old copy is already gone by
// then, so that command is a plain re-run rather than one that has to clear the
// way first.
//
// There is no separate trust step. Install records the trust itself once the
// user approves its prompt, so a trust command after it would ask the user to
// approve content they have just approved.
//
// A missing cliamp is not a failure of this program's own work, but it does leave
// the pair mismatched, so it is reported with both commands and a false return
// rather than silently skipped.
func installPlugin(ctx context.Context, target string, deps upgradeDeps, out io.Writer) bool {
	if _, err := deps.lookPath("cliamp"); err != nil {
		fmt.Fprintf(out,
			"plugin not updated: cliamp is not on PATH. Run: cliamp plugins remove discord-rpc (harmless if it is not installed), then cliamp plugins install %s@%s\n",
			release.Repository, tag(target))
		return false
	}
	_ = deps.runner.Run(ctx, "", "cliamp", "plugins", "remove", pluginName)
	if err := deps.runner.Run(ctx, "", "cliamp", "plugins", "install", release.Repository+"@"+tag(target)); err != nil {
		fmt.Fprintf(out, "plugin not updated: %v\nRun: cliamp plugins install %s@%s\n", err, release.Repository, tag(target))
		return false
	}
	fmt.Fprintf(out, "plugin updated to %s\n", tag(target))
	return true
}

// restart restarts a running daemon and says nothing about a stopped one.
//
// try-restart rather than restart: install.sh leaves the unit installed,
// disabled, and stopped, and a restart would start it for a user who deliberately
// runs the daemon in a terminal — possibly while that daemon is still running and
// holding the socket. A restart that could not be run is a warning, because
// try-restart cannot report "nothing was running" as distinct from "restarted",
// and because a machine without systemd is doing nothing wrong.
func restart(ctx context.Context, deps upgradeDeps, out io.Writer) {
	if _, err := deps.lookPath("systemctl"); err != nil {
		fmt.Fprintf(out, "%s\n", restartWarning)
		return
	}
	if err := deps.runner.Run(ctx, "", "systemctl", "--user", "try-restart", serviceName); err != nil {
		fmt.Fprintf(out, "%s\n", restartWarning)
		return
	}
	fmt.Fprintf(out, "restarted %s\n", serviceName)
}

// upgrade is both commands: the tag is chosen differently for each and nothing
// after it differs.
//
// The order is the spec's and it is fixed: validate an explicit tag, preflight the
// tools, resolve the tag if none was given, install the daemon, install the
// plugin, restart. The first two make no request and the third is the only one
// that does, so a bad tag or a missing command is answered without touching the
// network at all.
//
// Every step stops the sequence on failure and nothing after it runs. That is
// what keeps the pair matched: the daemon binary is already replaced by the time
// the plugin step runs, so a failed plugin would leave a new daemon to be started
// against the plugin it no longer matches, which is the state this command
// exists to avoid.
//
// The way back is named whenever the daemon half reached disk, including when the
// plugin step then failed. That is the case most likely to want it: the user is
// left with a replaced binary and an un-updated plugin, and no other line of the
// report mentions that a release before this one can be installed.
func upgrade(ctx context.Context, cfg config.Config, mode upgradeMode, deps upgradeDeps, out io.Writer) int {
	target, ok := explicitTag(cfg.ReleaseTag, out)
	if !ok {
		return 1
	}
	if missing := missingTools(deps.lookPath); missing != "" {
		fmt.Fprintf(out, "required command not found: %s (the release's install.sh needs it)\n", missing)
		return 1
	}
	if target == "" {
		var stop bool
		var code int
		target, stop, code = resolveTarget(ctx, mode, deps.releases, out)
		if stop {
			return code
		}
	}

	binDir := deps.binDir
	if binDir == "" {
		binDir = filepath.Dir(deps.binary)
	}
	fmt.Fprintf(out, "installing %s into %s, then restarting %s\n", tag(target), binDir, serviceName)
	if code := installRelease(ctx, target, binDir, deps, out); code != 0 {
		return code
	}
	pluginInstalled := installPlugin(ctx, target, deps, out)
	if pluginInstalled {
		restart(ctx, deps, out)
	}
	fmt.Fprintf(out, "to go back: cliamp-rpcd --rollback\n")
	if !pluginInstalled {
		return 1
	}
	return 0
}

// execRunner runs a child with its output on the same terminal the report uses,
// and its input inherited so a prompt behaves as it would by hand.
//
// An empty dir leaves the child in this process's working directory, which is
// what the plugin and restart steps want: they are the user's own commands and
// should see the user's own directory.
type execRunner struct {
	out io.Writer
}

// Run runs one command, returning its failure as the error.
func (r execRunner) Run(ctx context.Context, dir, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	command.Stdout = r.out
	command.Stderr = r.out
	command.Stdin = os.Stdin
	return command.Run()
}

// executablePath is the running binary's own path, which is where a release
// install replaces it.
//
// os.Executable cannot fail on the platforms this ships to, so the fallback is
// for a case that should not arise rather than one that does: a path from
// os.Args[0] is still better than refusing to update.
func executablePath() string {
	path, err := os.Executable()
	if err != nil {
		return os.Args[0]
	}
	return path
}

// newUpgradeDeps is the production wiring, shared by both commands so they cannot
// reach outside themselves differently.
func newUpgradeDeps(out io.Writer) upgradeDeps {
	return upgradeDeps{
		runner:   execRunner{out: out},
		lookPath: exec.LookPath,
		releases: release.New(),
		binary:   executablePath(),
		binDir:   os.Getenv("CLIAMP_RPC_BIN_DIR"),
	}
}

// Update installs the newest release, or the tag the user named, and returns the
// process exit code: 0 when both halves were installed, non-zero when either was
// not.
//
// It runs the same way whatever started it. Started by hand it replaces the
// binary under $HOME; started by the unit it fails, because ProtectHome=read-only
// is what makes the daemon unable to write the files it reads, and that is a
// property worth keeping.
func Update(ctx context.Context, cfg config.Config) int {
	return upgrade(ctx, cfg, modeUpdate, newUpgradeDeps(os.Stdout), os.Stdout)
}

// Rollback installs the newest release strictly older than this daemon, or the
// tag the user named, and returns the process exit code.
//
// It steps down one release per run rather than toggling: the release to go back
// to is derived from GitHub's list each time, so nothing about the previous
// version is stored on this machine and uninstall.sh has nothing new to clean up.
func Rollback(ctx context.Context, cfg config.Config) int {
	return upgrade(ctx, cfg, modeRollback, newUpgradeDeps(os.Stdout), os.Stdout)
}
