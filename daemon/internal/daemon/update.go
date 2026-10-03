package daemon

// This file is the self-update: one command that installs the newest release, or
// a release the user names. It installs forward and never back.
//
// Nothing here runs on its own and nothing here runs inside the unit. The unit
// is hardened with ProtectHome=read-only, so a daemon started by it cannot write
// the binary it would be replacing; installing is a process the user starts, and
// this program is that process when it is given --update.
//
// There is deliberately no way back. Nothing keeps the binary being replaced, so
// an older release is only reachable by installing it from GitHub, and a command
// that re-fetched one would be the same install with a different tag argument
// rather than a second capability. Installing the release a user names is what
// --update does, and a tag older than the running release is refused instead of
// served.
//
// The installing itself is not implemented here. The target release's own
// install.sh is fetched and run, so the download, attestation, and checksum
// rules have one implementation rather than two that can drift.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/config"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/release"
	"github.com/fazaimron27/cliamp-plugin-discord-rpc/daemon/internal/version"
)

// releaseSource is the slice of the release package the update path needs.
type releaseSource interface {
	Latest(context.Context) (string, error)
	Script(context.Context, string) ([]byte, error)
}

// runner runs one child process and hands back everything it wrote. The real one
// is execRunner; the tests replace it, because the commands and their order are
// the feature.
type runner interface {
	Run(ctx context.Context, dir, name string, args ...string) ([]byte, error)
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
// A tag older than the running release is refused, and the refusal is where that
// rule lives rather than in the installation: a request this command cannot serve
// is answered before anything is fetched, so a user who named the wrong tag is
// told so instead of watching an install they did not want begin. Nothing keeps
// the binary being replaced, so there is no release to go back to and no second
// command that could install one; an older release is only reachable from GitHub
// by hand, and this line is the whole of the refusal.
//
// The running release itself is allowed. Reinstalling it is a request this
// command can serve, and the no-op message resolveTarget prints names it.
func explicitTag(given string, out io.Writer) (string, bool) {
	if given == "" {
		return "", true
	}
	target := version.Normalize(given)
	if !version.IsVersion(target) {
		fmt.Fprintf(out, "not a release tag: %s\n", given)
		return "", false
	}
	if version.Newer(target, version.Number) {
		fmt.Fprintf(out,
			"%s is older than the running v%s, and this command does not install older releases\n",
			tag(target), version.Number)
		return "", false
	}
	return target, true
}

// resolveTarget asks GitHub which release to install when the user named none.
// It is reached after the preflight has already passed. It reports stop when the
// run is over before anything was installed, with the code to return.
//
// An answer that is not newer than this daemon stops the run as a success rather
// than a refusal. It is not a request the user made, so there is nothing to
// refuse: a machine already at or ahead of the newest release has nothing to do,
// and exit 0 is what "nothing to do" means. This is also the only path that can
// see a tag older than the running one, which happens when the running binary is
// newer than anything published — a local build, or a release withdrawn since.
func resolveTarget(ctx context.Context, releases releaseSource, out io.Writer) (string, bool, int) {
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

// installRelease fetches the target release's own installer and runs it.
//
// The installed tag and the bin directory are passed on the command line, and
// the script is written into a fresh temporary directory that it is run from.
// That directory is the point of the whole arrangement: install.sh installs what
// sits beside it without verifying anything, so the only safe place to run it is
// somewhere nothing sits beside it. The script's absolute path is passed as well
// as the directory, so the branch it takes depends on where the script is rather
// than on the child honoring its working directory.
//
// The line this reports claims that the attestation and the checksum were
// verified, which is a statement about install.sh rather than something this
// program watched happen. It holds because of the two facts above: the empty
// directory forces the download branch, and that branch runs gh attestation
// verify and sha256sum -c under `set -eu`, so an exit status of zero is only
// reachable once both have passed. A release whose install.sh dropped either
// check would make this line false, which is the one thing to re-read here if
// that ever changes.
func installRelease(ctx context.Context, target, binDir string, deps upgradeDeps, out io.Writer) int {
	script, err := deps.releases.Script(ctx, tag(target))
	if err != nil {
		reportLine(out, "fail", "daemon", fmt.Sprintf("could not fetch the installer for %s: %v", tag(target), err))
		return 1
	}
	dir, err := os.MkdirTemp("", "cliamp-rpc-update.")
	if err != nil {
		reportLine(out, "fail", "daemon", fmt.Sprintf("could not create a working directory: %v", err))
		return 1
	}
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "install.sh")
	if err := os.WriteFile(path, script, 0o600); err != nil {
		reportLine(out, "fail", "daemon", fmt.Sprintf("could not write the installer: %v", err))
		return 1
	}
	output, err := deps.runner.Run(ctx, dir, "sh", path, "--version", tag(target), "--bin-dir", binDir)
	if err != nil {
		replay(out, output)
		reportLine(out, "fail", "daemon", fmt.Sprintf("the installer failed: %v", err))
		return 1
	}
	reportLine(out, "ok", "daemon",
		fmt.Sprintf("v%s -> %s, attestation and checksum verified", version.Number, tag(target)))
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
// The trust is approved with --yes rather than asked for. Install records the
// trust itself, but without --yes it reads the approval from its own stdin and
// fails outright when there is none to read — so a run started from a script, a
// timer, or anywhere else without a terminal would stop at the plugin half with
// the daemon half already replaced. Update is a command the user chose to run, on
// this project's own release, so the approval is taken as given rather than put
// to a user who has already asked for the install.
//
// A missing cliamp is not a failure of this program's own work, but it does leave
// the pair mismatched, so it is reported with both commands and a false return
// rather than silently skipped.
func installPlugin(ctx context.Context, target string, deps upgradeDeps, out io.Writer) bool {
	if _, err := deps.lookPath("cliamp"); err != nil {
		reportLine(out, "fail", "plugin", "cliamp is not on PATH")
		fmt.Fprintf(out,
			"Run: cliamp plugins remove discord-rpc (harmless if it is not installed), then cliamp plugins install %s@%s\n",
			release.Repository, tag(target))
		return false
	}
	_, _ = deps.runner.Run(ctx, "", "cliamp", "plugins", "remove", pluginName)
	output, err := deps.runner.Run(ctx, "", "cliamp", "plugins", "install", release.Repository+"@"+tag(target), "--yes")
	if err != nil {
		replay(out, output)
		reportLine(out, "fail", "plugin", fmt.Sprintf("the install failed: %v", err))
		fmt.Fprintf(out, "Run: cliamp plugins install %s@%s\n", release.Repository, tag(target))
		return false
	}
	reportLine(out, "ok", "plugin", "installed "+tag(target))
	reportTrust(out, output)
	return true
}

// trustFacts are what Cliamp says about a plugin it installs, in the order it
// says them, paired with the name this program reports each one under.
var trustFacts = []struct {
	name   string
	prefix string
}{
	{"source", "Source: "},
	{"sha256", "SHA-256: "},
	{"permissions", "Declared permissions: "},
	{"access", "Implicit access: "},
}

// trustIndent puts a step's continuation lines in the same column reportLine puts
// a step's own detail in.
const trustIndent = reportWidth + 1 + statusWidth + 1

// reportTrust prints what Cliamp recorded about the plugin, one fact per line,
// indented under the step that recorded it.
//
// They are read back out of the install's own output rather than fetched again,
// because Cliamp is what computed the hash and read the declared permissions. A
// fact whose line is not in the output is left out rather than guessed at, so a
// Cliamp that words its report differently costs the line and not the update.
func reportTrust(out io.Writer, output []byte) {
	for _, fact := range trustFacts {
		for _, line := range strings.Split(string(output), "\n") {
			if !strings.HasPrefix(line, fact.prefix) {
				continue
			}
			fmt.Fprintf(out, "%*s%-12s %s\n",
				trustIndent, "", fact.name, strings.TrimSpace(line[len(fact.prefix):]))
			break
		}
	}
}

// replay writes a failed command's own output.
//
// Capturing a child's output is what makes a quiet success possible, and it must
// not make a failure quiet as well: when a step fails, the diagnosis is what the
// command said rather than this program's summary of it.
func replay(out io.Writer, output []byte) {
	if len(output) == 0 {
		return
	}
	out.Write(output)
	if output[len(output)-1] != '\n' {
		fmt.Fprintln(out)
	}
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
		reportLine(out, "warn", "service", restartWarning)
		return
	}
	output, err := deps.runner.Run(ctx, "", "systemctl", "--user", "try-restart", serviceName)
	if err != nil {
		replay(out, output)
		reportLine(out, "warn", "service", restartWarning)
		return
	}
	reportLine(out, "ok", "service", "restarted "+serviceName)
}

// upgrade is the whole of --update: validate an explicit tag, preflight the
// tools, resolve the tag if none was given, install the daemon, install the
// plugin, restart.
//
// The order is the spec's and it is fixed. The first two make no request and the
// third is the only one that does, so a bad tag or a missing command is answered
// without touching the network at all.
//
// Every step stops the sequence on failure and nothing after it runs. That is
// what keeps the pair matched: the daemon binary is already replaced by the time
// the plugin step runs, so a failed plugin would leave a new daemon to be started
// against the plugin it no longer matches, which is the state this command
// exists to avoid.
func upgrade(ctx context.Context, cfg config.Config, deps upgradeDeps, out io.Writer) int {
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
		target, stop, code = resolveTarget(ctx, deps.releases, out)
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
	if !pluginInstalled {
		return 1
	}
	return 0
}

// execRunner runs a child with its output captured rather than shown.
//
// Nothing a child prints reaches the terminal on its own. The download progress
// meters, the attestation dump, and the installer's own advice for a first-time
// install are all noise around a command whose answer is one line, and no flag
// turns them off: install.sh offers only --version, --bin-dir, and --service-dir,
// and gh's attestation verify offers none. So the update reports in its own words
// and replays a child's output only when that child failed, where it is the
// diagnosis rather than the noise.
//
// Stdin is deliberately empty. Inheriting the terminal is what would let a prompt
// be answered by hand, but with the output captured that prompt would be invisible
// and the run would hang on it — gh asking for a login is the case that happens.
// An empty stdin turns the same situation into a failure that can be read.
//
// An empty dir leaves the child in this process's working directory, which is
// what the plugin and restart steps want: they are the user's own commands and
// should see the user's own directory.
type execRunner struct{}

// Run runs one command, returning what it wrote and its failure as the error.
//
// The same buffer takes both streams, which exec notices and serves with a single
// pipe, so the output arrives in the order the child wrote it rather than in two
// blocks to be interleaved afterwards.
//
// Running before reading is not a style choice: a return statement evaluates its
// operands left to right, so `output.Bytes(), command.Run()` reads the buffer
// before the child has written to it and hands back nothing at all.
func (r execRunner) Run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	return output.Bytes(), err
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

// newUpgradeDeps is the production wiring, gathered in one place so the sequence
// reaches outside itself in exactly one way rather than at each step's call site.
//
// It takes no writer now that the runner captures: a child's output goes to the
// report only through the step that ran it, which is what keeps a failure's
// diagnosis from arriving before the step that failed.
func newUpgradeDeps() upgradeDeps {
	return upgradeDeps{
		runner:   execRunner{},
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
// A named tag must not be older than the running release; explicitTag is where
// that is refused and where the reasoning for it lives.
//
// It runs the same way whatever started it. Started by hand it replaces the
// binary under $HOME; started by the unit it fails, because ProtectHome=read-only
// is what makes the daemon unable to write the files it reads, and that is a
// property worth keeping.
func Update(ctx context.Context, cfg config.Config) int {
	return upgrade(ctx, cfg, newUpgradeDeps(), os.Stdout)
}
