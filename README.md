# Cliamp Discord RPC Plugin

Discord Rich Presence for [Cliamp](https://www.cliamp.stream/). The Lua plugin
hands playback snapshots to the `cliamp-rpcd` daemon, which forwards them to the
local Discord desktop client. Snapshots travel through Cliamp's in-memory IPC
pub/sub broker by default, and through a state file when
[the transport is configured](#choose-the-playback-transport) to `file`.

![Cliamp Discord Rich Presence](https://github.com/user-attachments/assets/f16ed6b7-052d-4bd3-b5a3-cdfc8fb64ff5)

> [!NOTE]
> This project is currently developed and tested only on Linux. Prebuilt daemon
> releases are available for `x86_64`/`amd64` and `aarch64`/`arm64`.

## Compatibility

The default `ipc` transport requires a Cliamp build that speaks IPC protocol
version 2 and exposes the plugin event pub/sub API. Cliamp made version 2
mandatory for its socket, so an older build answers the daemon's subscription
with a structured `invalid_version` error. Use one of these combinations:

- **v1.8.0 with a current Cliamp:** run any Cliamp build that includes the
  version 2 IPC cutover ([`c75cdec`](https://github.com/bjarneo/cliamp/commit/c75cdec)),
  then install this project's `v1.8.0` plugin and daemon.
- **An older Cliamp with the v1.8.0 daemon:** configure the
  [`file` transport](#choose-the-playback-transport). The plugin writes the
  state document instead of publishing it and the daemon reads it, so a Cliamp
  build without the pub/sub API runs the same plugin and daemon as any other.
  It requires no `p:publish()`.

The Lua plugin and daemon must use the same release line, unless the transport
is set to match them across the two as described below. Do not install the
v1.8.0 Lua plugin into a Cliamp build that lacks `p:publish()` while that plugin
is left on the default transport: its event handlers will fail when they try to
publish, and no playback events will reach the daemon. Set
[`transport = "file"`](#choose-the-playback-transport) for such a build instead.

## Prerequisites

Before installing v1.8.0, make sure you have:

- Git and Go 1.26.5 or newer to build Cliamp from its official `main` branch.
- Cliamp built from the official
  [`main`](https://github.com/bjarneo/cliamp/tree/main) branch and available as
  `cliamp`. The build must include the
  [version 2 IPC cutover](https://github.com/bjarneo/cliamp/commit/c75cdec) and
  the [retained plugin event pub/sub merge](https://github.com/bjarneo/cliamp/commit/f373776d).
- The Discord desktop client. Discord in a web browser does not expose the local
  IPC socket used by Rich Presence.
- A Discord account signed in to the desktop client.
- `curl`, `gh`, `sha256sum`, `tar`, and `systemctl` when installing from a release.
- Go 1.25 or newer when self-deploying the daemon from source.

The daemon and Discord must run in the same desktop user session. The supplied
service is a systemd user service and does not require root access.

No Discord Developer Portal registration or Last.fm API key is required. The
daemon uses the community-maintained Cliamp Discord application by default and
displays its static artwork. Album artwork through Last.fm is an optional
enhancement.

## Install v1.8.0 from release

Use this path for a normal v1.8.0 installation on `amd64` or `arm64` after
installing Cliamp from its official `main` branch. It installs the plugin
through Cliamp and downloads the published `v1.8.0` daemon; Go is not required
for the plugin or daemon.

### Build Cliamp main

Build and install the official branch containing the version 2 IPC envelope and
the merged plugin pub/sub API:

```sh
git clone --branch main --single-branch \
  https://github.com/bjarneo/cliamp.git cliamp-main
cd cliamp-main
go test ./ipc ./luaplugin
go build -o cliamp .
install -Dm755 ./cliamp ~/.local/bin/cliamp
cd ..
```

Close any running Cliamp process before replacing its executable, then start it
again after installing and trusting the plugin below. Confirm that your shell
resolves the new binary with `command -v cliamp`.

### Install the plugin

```sh
cliamp plugins install fazaimron27/cliamp-plugin-discord-rpc@v1.8.0
cliamp plugins trust discord-rpc
```

Review the source, SHA-256 hash, declared permissions, and filesystem access
shown by Cliamp before approving it. Restart Cliamp after installation.

### Install the daemon

Install the daemon directly from this repository:

```sh
curl -fsSL https://raw.githubusercontent.com/fazaimron27/cliamp-plugin-discord-rpc/v1.8.0/install.sh | sh
```

This command downloads code and executes it. To review the installer first:

```sh
curl -fsSL -o install.sh https://raw.githubusercontent.com/fazaimron27/cliamp-plugin-discord-rpc/v1.8.0/install.sh
less install.sh
sh install.sh
rm install.sh
```

The installer:

- Detects `amd64` or `arm64`.
- Downloads the matching archive from the
  [v1.8.0 release](https://github.com/fazaimron27/cliamp-plugin-discord-rpc/releases/tag/v1.8.0).
- Verifies the archive's GitHub Actions provenance attestation, bound to this repository's release workflow.
- Verifies the archive against the published SHA-256 checksum.
- Installs `cliamp-rpcd` to `~/.local/bin`.
- Installs `cliamp-rpcd.service` as a systemd user service.
- Reloads the systemd user manager without starting the daemon.

The user service is installed but deliberately left disabled and inactive. The
installer prints both installed paths and does not start the daemon.

To install a specific version or use custom destinations, download the script
first and pass options to it. Run `sh install.sh --help` for details.

To remove only the daemon and service later:

```sh
curl -fsSL https://raw.githubusercontent.com/fazaimron27/cliamp-plugin-discord-rpc/v1.8.0/uninstall.sh | sh
```

To review the uninstaller first, download it with `curl -fsSL -o uninstall.sh`,
inspect it, then run `sh uninstall.sh`.

The uninstaller stops and disables the user service if it is active, removes the
daemon and unit file, and preserves the Cliamp plugin and configuration. Use
`--bin-dir` and `--service-dir` if you installed to custom locations. Continue
at [Start and verify](#start-and-verify).

## Self-deploy from source

Use this path for development, an architecture without a prebuilt archive, or
when you want to audit and build every installed file yourself. This path does
not use the release installer.

### Build the daemon

```sh
git clone --branch v1.8.0 --single-branch \
  https://github.com/fazaimron27/cliamp-plugin-discord-rpc.git
cd cliamp-plugin-discord-rpc
go test ./...
go vet ./...
go build -o cliamp-rpcd ./daemon/cmd/cliamp-rpcd
```

### Install the source checkout

```sh
install -Dm644 ./discord-rpc.lua ~/.config/cliamp/plugins/discord-rpc.lua
cliamp plugins trust discord-rpc
install -Dm755 ./cliamp-rpcd ~/.local/bin/cliamp-rpcd
install -Dm644 ./cliamp-rpcd.service ~/.config/systemd/user/cliamp-rpcd.service
systemctl --user daemon-reload
```

To remove a source deployment, run the repository's uninstaller from the same
checkout or specify the matching custom directories:

```sh
./uninstall.sh
```

Restart Cliamp after installing the Lua plugin. When you edit that file later,
run `cliamp plugins trust discord-rpc` again to approve its new hash, then
restart Cliamp. If startup reports `attempt to call a non-function object` for
`publish`, or the daemon logs `subscribe to Cliamp events:` with
`invalid_version`, verify that you installed a Cliamp build from the official
`main` branch after the version 2 IPC cutover, or switch to the
[`file` transport](#choose-the-playback-transport), which needs no `p:publish()`.

## Start and verify

1. Start the Discord desktop client and sign in.
2. Start or restart Cliamp and play a track.
3. Run the daemon manually:

```sh
~/.local/bin/cliamp-rpcd
```

A successful startup looks like this:

```text
$ cliamp-rpcd
2026/08/14 15:29:59 starting cliamp-rpcd 1.8.0 (Cliamp IPC: /home/faza/.config/cliamp/cliamp.sock)
2026/08/14 15:30:14 subscribed to Cliamp playback events
2026/08/14 15:30:18 connected to Discord at /run/user/1000/discord-ipc-0
```

The first line reports the daemon's release. The subscription line confirms the
Lua plugin-to-daemon event stream, and the Discord line confirms the local Rich
Presence connection. A playing track should then appear on your Discord profile.
Keep this terminal open while using the daemon and press `Ctrl+C` to stop it.
Pausing or stopping playback clears the activity, and the daemon reconnects
automatically if Discord is started or restarted later.

With [`transport = "file"`](#choose-the-playback-transport), the first line
names the state document instead of the socket and the second reads `watching
for Cliamp state in <path>`: the daemon is reading the document rather than
subscribing to it.

Run `~/.local/bin/cliamp-rpcd --help` for all daemon options,
`~/.local/bin/cliamp-rpcd --version` to print the release and exit, or
`~/.local/bin/cliamp-rpcd --check` to probe the environment before starting.
With the default `ipc` transport, the daemon subscribes to
`plugin.discord-rpc.playback` on Cliamp's owner-only local IPC socket and
reconnects automatically when Cliamp restarts.

If the daemon logs a warning that the plugin and daemon versions do not match,
the two halves came from different release lines. The warning names the half that
is behind and prints the command that updates it, so follow that line. For
reference:

- **The plugin is behind.** Install it at the daemon's version, which the warning
  names:

  ```sh
  cliamp plugins install fazaimron27/cliamp-plugin-discord-rpc@v1.8.0
  ```

- **The daemon is behind.** This is the usual state when you build the daemon
  from source and run it behind an already-updated plugin. Rebuild it, or install
  the released daemon, then restart it:

  ```sh
  curl -fsSL https://raw.githubusercontent.com/fazaimron27/cliamp-plugin-discord-rpc/v1.8.0/install.sh | sh
  ```

Only the major and minor components are compared, so a patch difference stays
silent. A plugin old enough to omit its version is not warned about at all.

### Optional systemd user service

To run the daemon automatically in your desktop session instead of keeping it
in a terminal:

```sh
systemctl --user enable --now cliamp-rpcd.service
systemctl --user status cliamp-rpcd.service
journalctl --user -u cliamp-rpcd.service -f
```

Stop and disable automatic startup with:

```sh
systemctl --user disable --now cliamp-rpcd.service
```

## Optional customization

No `[plugins.discord-rpc]` configuration is needed for normal use. The options
below belong in Cliamp's existing `~/.config/cliamp/config.toml` file.

### Enable Last.fm album artwork

Create a Last.fm API key from the
[API account page](https://www.last.fm/api/account/create), then add it to the
dedicated plugin section:

```toml
[plugins.discord-rpc]
lastfm_api_key = "YOUR_LASTFM_API_KEY"
```

Only the API key is needed. Do not add the Last.fm shared secret. When the key
is absent or empty, artwork lookup is disabled and the community-maintained
static Discord asset is used.

### Use a custom Discord application

To replace the community-maintained default Discord application, create an
application in the
[Discord Developer Portal](https://discord.com/developers/applications), copy
its Application ID, and upload a square Rich Presence art asset named `cliamp`.
Then configure the override:

```toml
[plugins.discord-rpc]
app_id = "YOUR_DISCORD_APPLICATION_ID"
```

Newly uploaded Discord assets can take several minutes to become available.
Command-line and environment overrides are also supported; run
`cliamp-rpcd --help` for details.

### Choose the playback transport

Snapshots leave the plugin over Cliamp's IPC broker by default. To use a state
file instead, set the transport once:

```toml
[plugins.discord-rpc]
transport = "file"
```

The plugin reads this key and so does the daemon, from the same `config.toml`,
so one setting moves both halves and neither has to be told what the other
chose. Restart Cliamp after changing it.

Use `file` when the Cliamp build does not expose `p:publish()`, which is what
the default transport needs. Nothing else changes: artwork, version reporting,
and pause/stop behavior work the same way, and the daemon still clears the
activity when Cliamp quits — the plugin stops writing its heartbeat, and the
document stops counting as live.

Two further keys belong to this transport and are optional:

```toml
[plugins.discord-rpc]
state_path = "/home/you/.local/share/cliamp/rpc-state.json"  # where the document lives
```

`state_path` is the document's location, and the daemon watches the directory it
sits in. It has no command-line or environment equivalent, because a path that
reaches only the daemon would have the plugin writing elsewhere. The window
after which a silent document is treated as a dead Cliamp does have one:
`--max-age`, 45 seconds by default, which is three heartbeats.

A `--transport` flag and `CLIAMP_DISCORD_TRANSPORT` override the file's value
for the daemon alone. They exist for troubleshooting, and a daemon that uses one
warns at startup that the plugin will read `config.toml` instead. `cliamp-rpcd
--check` reports the transport in use and where its value came from, which is
the quickest way to see whether the two halves agree.

## Troubleshooting

Start with the built-in diagnostic. It probes each transport for real and reports
what it found:

```sh
~/.local/bin/cliamp-rpcd --check
```

```text
cliamp-rpcd 1.8.0

transport ok    ipc, from the default
cliamp    ok    subscribed to plugin.discord-rpc.playback at /home/faza/.config/cliamp/cliamp.sock
plugin    ok    plugin v1.8.0 matches daemon v1.8.0
discord   fail  Discord IPC unavailable: dial unix /run/user/1000/discord-ipc-0: connect: no such file or directory
last.fm   ok    the API key was accepted
config    ok    /home/faza/.config/cliamp/config.toml
```

The line above is the Discord-absent case, which the first entry below covers.
`--check` exits with status 1 when Cliamp or Discord is unreachable, so it can
gate a start:

```sh
cliamp-rpcd --check && systemctl --user start cliamp-rpcd.service
```

Artwork and plugin-version lines report `warn` without failing the command,
because the daemon runs without artwork and treats version skew as a warning
rather than an error. It makes exactly one connection to each half and never
publishes an activity, so running it does not disturb your Discord presence.

The `transport` line is the one to read first when nothing appears: it names the
transport in use and where that value came from. A `warn` there means the daemon
and the plugin are reading different settings, which from Discord's side looks
exactly like a broken Cliamp.

### Discord activity does not appear

- Confirm the Discord desktop client is running under the same Linux user.
- If using a custom application, confirm `app_id` exactly matches its Discord
  Application ID.
- Check the daemon log with `journalctl --user -u cliamp-rpcd -f`.
- Restart Discord if it was opened after the daemon; the daemon will reconnect.

### Album artwork does not appear

- Confirm `lastfm_api_key` contains the Last.fm **API key**, not the shared secret.
- Confirm the track has both artist and title metadata.
- Confirm Last.fm has artwork for that artist and track.
- Wait for the Discord asset named `cliamp` to finish processing; it is the
  fallback when Last.fm has no image.
- A lookup that fails is retried while the track plays, so a network blip
  resolves itself without restarting anything.

### Cliamp rejects the subscription or plugin publishing fails

Version 1.8.0 requires Cliamp's version 2 IPC envelope and retained plugin event
pub/sub API, both of which are on the official
[`main`](https://github.com/bjarneo/cliamp/tree/main) branch. The daemon logs the
exact rejection, so start with:

```sh
journalctl --user -u cliamp-rpcd -n 20
```

`invalid_version` means the running `cliamp` executable predates the
[version 2 IPC cutover](https://github.com/bjarneo/cliamp/commit/c75cdec).
`unknown operation` or a missing `publish` means it predates the
[retained plugin event pub/sub merge](https://github.com/bjarneo/cliamp/commit/f373776d).
In both cases rebuild `cliamp` from that branch, reinstall and trust
`discord-rpc.lua`, then restart Cliamp. If you want to stay on an older tagged
Cliamp release, switch to the
[`file` transport](#choose-the-playback-transport) instead, which needs no
pub/sub API.

### The service fails immediately

Run the daemon in the foreground to see the configuration error directly:

```sh
systemctl --user stop cliamp-rpcd
~/.local/bin/cliamp-rpcd
```

The built-in Application ID is used unless a custom value is supplied. The
Last.fm API key is optional and artwork lookup is disabled when it is empty.

### Nothing appears with the file transport

The `cliamp` line of `--check` reads the state document, so it says what the run
loop cannot: why a document that exists is not being used.

```text
transport ok    file, from the config file
cliamp    fail  no state document at /home/you/.local/share/cliamp/rpc-state.json, so Cliamp is not running or the plugin is not writing one
```

- **No document.** Cliamp has not run since `transport = "file"` was added, or
  the plugin was not restarted after it. Restart Cliamp, then play something.
- **A document that cannot be read.** The report names the reason. A schema this
  daemon does not know means the two halves came from different releases, and
  the `plugin` line usually says which half is behind.
- **A document last written a while ago.** The plugin has stopped writing its
  heartbeat, so Cliamp has quit or crashed; the daemon has already cleared the
  activity and is waiting for the next document. If Cliamp is still running, the
  plugin's write is failing, which it logs to Cliamp's own log.

`--check` also reports a state path that differs from the default when
`state_path` sets one, so a driver between the two lines is a setting the two
halves do not share: the plugin writes the default path unless the same key
reaches it in `[plugins.discord-rpc]`.

## How it works

The plugin publishes a complete playback snapshot to the retained
`plugin.discord-rpc.playback` topic whenever Cliamp starts, changes track,
changes playback state, seeks, or quits. Cliamp keeps only the latest snapshot
in memory and immediately replays it to a newly connected daemon. The daemon
resolves optional album artwork through Last.fm and updates Discord through its
local IPC socket.

With that default transport the subscription is also the liveness signal:
pausing or stopping clears activity, and an unclean Cliamp exit closes the
stream and clears activity immediately. The daemon reconnects with bounded
backoff and receives the latest retained snapshot after Cliamp returns.

With `transport = "file"` the same snapshots are written to
`~/.local/share/cliamp/rpc-state.json` instead, and the daemon watches that file
rather than subscribing. Two fields get their own job in that document: the
change time, which moves only when the playback does and from which the progress
bar is interpolated, and a heartbeat, refreshed every 15 seconds whether or not
anything changed. The heartbeat stands in for the connection: a document that
stops being refreshed, or is removed on quit, clears the activity. Nothing is
written to disk unless this transport is selected.

Behaviors that hold for both: pausing or stopping clears activity, and the
daemon's half of the contract is described in
[Architecture](docs/architecture.md) — the state document's schema, package
responsibilities, artwork flow, and failure behavior.
