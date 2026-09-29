# Cliamp Discord RPC Plugin

Discord Rich Presence for [Cliamp](https://www.cliamp.stream/). The Lua plugin
hands playback snapshots to the `cliamp-rpcd` daemon, which forwards them to the
local Discord desktop client.

![Cliamp Discord Rich Presence](https://github.com/user-attachments/assets/f16ed6b7-052d-4bd3-b5a3-cdfc8fb64ff5)

> [!NOTE]
> This project is currently developed and tested only on Linux. Prebuilt daemon
> releases are available for `x86_64`/`amd64` and `aarch64`/`arm64`.

This README covers using the plugin: install, run, configure, troubleshoot.
Building and internals live under [`docs/`](docs/) —
[Building from source](docs/building.md) and
[Architecture](docs/architecture.md).

## Compatibility

One thing decides the setup, and it is the Cliamp version you have:

- **Cliamp v2.x:** install both halves below and change nothing. This is the
  default `ipc` transport.
- **Cliamp v1.x:** set
  [`transport = "file"`](#choose-the-playback-transport). The plugin writes a
  state document instead of publishing one, so it asks nothing of Cliamp beyond
  `cliamp.fs`, and the daemon reads that document. Nothing else about the setup
  changes.

The plugin API the default transport needs — retained plugin events published
over IPC — first shipped in Cliamp v2. No v1 release carries it, so on a v1 build
the `ipc` transport cannot work at all, and no setting makes it. The split really
is the major version: every v1 build needs `file`, every v2 build takes `ipc`.

Either way, the plugin and the daemon must be on the same release line. What the
default transport requires of Cliamp, and what a v1 build answers instead, is in
[Architecture](docs/architecture.md#pubsub-contract).

## Prerequisites

- Cliamp v2.x, on your `PATH` as `cliamp`. A v1 build can use the `file` transport
  instead ([build recipe](docs/building.md#build-cliamp-from-main), which builds
  the official [`main`](https://github.com/bjarneo/cliamp/tree/main) branch).
- The Discord desktop client, signed in. Discord in a web browser does not expose
  the local IPC socket Rich Presence uses.
- `curl`, `gh`, `sha256sum`, `tar`, and `systemctl` when installing from a
  release.

The daemon and Discord must run in the same desktop user session. The supplied
service is a systemd *user* service and needs no root access.

No Discord Developer Portal registration and no Last.fm API key are required. The
community-maintained Cliamp Discord application is used by default, with its
static artwork; album artwork through Last.fm is optional.

## Install v1.9.0 from release

This path installs the plugin through Cliamp and downloads the published
`v1.9.0` daemon. Go is not required. To build from source instead, see
[Building from source](docs/building.md).

### Install the plugin

```sh
cliamp plugins install fazaimron27/cliamp-plugin-discord-rpc@v1.9.0
cliamp plugins trust discord-rpc
```

Review the source, SHA-256 hash, declared permissions, and filesystem access
shown by Cliamp before approving it. Restart Cliamp after installation.

### Install the daemon

```sh
curl -fsSL https://raw.githubusercontent.com/fazaimron27/cliamp-plugin-discord-rpc/v1.9.0/install.sh | sh
```

This command downloads code and executes it. To review the installer first:

```sh
curl -fsSL -o install.sh https://raw.githubusercontent.com/fazaimron27/cliamp-plugin-discord-rpc/v1.9.0/install.sh
less install.sh
sh install.sh
rm install.sh
```

The installer:

- Detects `amd64` or `arm64`.
- Downloads the matching archive from the
  [v1.9.0 release](https://github.com/fazaimron27/cliamp-plugin-discord-rpc/releases/tag/v1.9.0).
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
curl -fsSL https://raw.githubusercontent.com/fazaimron27/cliamp-plugin-discord-rpc/v1.9.0/uninstall.sh | sh
```

To review the uninstaller first, download it with `curl -fsSL -o uninstall.sh`,
inspect it, then run `sh uninstall.sh`.

The uninstaller stops and disables the user service if it is active, removes the
daemon and unit file, and preserves the Cliamp plugin and configuration. Use
`--bin-dir` and `--service-dir` if you installed to custom locations.

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
2026/08/14 15:29:59 starting cliamp-rpcd 1.9.0 (Cliamp IPC: /home/user/.config/cliamp/cliamp.sock)
2026/08/14 15:30:14 subscribed to Cliamp playback events
2026/08/14 15:30:18 connected to Discord at /run/user/1000/discord-ipc-0
```

The first line reports the daemon's release. The subscription line confirms the
plugin-to-daemon event stream, and the Discord line confirms the local Rich
Presence connection. A playing track should then appear on your Discord profile.
Keep this terminal open while using the daemon and press `Ctrl+C` to stop it.
Pausing or stopping playback clears the activity, and the daemon reconnects on
its own if Cliamp or Discord is restarted.

With [`transport = "file"`](#choose-the-playback-transport), the first line
names the state document instead of the socket and the second reads `watching
for Cliamp state in <path>`: the daemon is reading the document rather than
subscribing to it.

Run `~/.local/bin/cliamp-rpcd --help` for all daemon options, `--version` to
print the release and exit, or `--check` to probe the environment before
starting.

If the daemon logs a warning that the plugin and daemon versions do not match,
the two halves came from different release lines. The warning names the half that
is behind and prints the command that updates it, so follow that line. For
reference:

- **The plugin is behind.** Install it at the daemon's version, which the warning
  names:

  ```sh
  cliamp plugins install fazaimron27/cliamp-plugin-discord-rpc@v1.9.0
  ```

- **The daemon is behind.** This is the usual state when you build the daemon
  from source and run it behind an already-updated plugin. Rebuild it
  ([Building from source](docs/building.md)), or install the released daemon,
  then restart it:

  ```sh
  curl -fsSL https://raw.githubusercontent.com/fazaimron27/cliamp-plugin-discord-rpc/v1.9.0/install.sh | sh
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

## Configuration

Nothing has to be configured for normal use. When you do want to change
something, the keys live in Cliamp's existing `~/.config/cliamp/config.toml`,
in their own section, and every one of them is optional:

```toml
[plugins.discord-rpc]
transport = "ipc"      # ipc (default) or file
# lastfm_api_key = ""  # a Last.fm key enables album artwork
# app_id = ""          # your own Discord application ID
# state_path = "/home/user/.local/share/cliamp/rpc-state.json"   # file transport only
```

Restart Cliamp after changing any of them. The sections below cover each key.

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
chose.

Use `file` when the Cliamp build does not expose `p:publish()`, which is what
the default transport needs. Nothing else changes: artwork, version reporting,
and pause/stop behavior work the same way, and the daemon still clears the
activity when Cliamp quits — the plugin stops writing its heartbeat, so the
document stops counting as live.

`state_path` is where that document lives, and it is the one key with no
command-line or environment equivalent: a path that reached only the daemon
would leave the plugin writing somewhere else. The daemon watches the document's
directory, and treats a document whose heartbeat has stopped as a dead Cliamp
after `--max-age` — 45 seconds by default, which tolerates two missed beats.

## Troubleshooting

Start with the built-in diagnostic. It probes each transport for real and reports
what it found:

```sh
~/.local/bin/cliamp-rpcd --check
```

```text
cliamp-rpcd 1.9.0

transport ok    ipc, from the default
cliamp    ok    subscribed to plugin.discord-rpc.playback at /home/user/.config/cliamp/cliamp.sock
plugin    ok    plugin v1.9.0 matches daemon v1.9.0
discord   fail  Discord IPC unavailable: dial unix /run/user/1000/discord-ipc-0: connect: no such file or directory
last.fm   ok    the API key was accepted
config    ok    /home/user/.config/cliamp/config.toml
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

### Cliamp rejects the subscription or plugin publishing fails

Version 1.9.0 requires Cliamp's version 2 IPC envelope and retained plugin event
pub/sub API, both of which are on the official
[`main`](https://github.com/bjarneo/cliamp/tree/main) branch. The daemon logs the
exact rejection, so start with:

```sh
journalctl --user -u cliamp-rpcd -n 20
```

`invalid_version` means the running `cliamp` executable predates the version 2
IPC cutover. `unknown operation` or a missing `publish` means it predates the
retained plugin event pub/sub merge. In both cases rebuild `cliamp` from that
branch ([build recipe](docs/building.md#build-cliamp-from-main)), reinstall and
trust `discord-rpc.lua`, then restart Cliamp. If you want to stay on an older
tagged Cliamp release, switch to the
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
cliamp    fail  no state document at /home/user/.local/share/cliamp/rpc-state.json, so Cliamp is not running or the plugin is not writing one
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
`state_path` sets one, so a difference between the two lines is a setting the
two halves do not share: the plugin writes the default path unless the same key
reaches it in `[plugins.discord-rpc]`.

## How it works

The plugin hands the daemon a complete playback snapshot whenever Cliamp starts,
changes track, changes playback state, seeks, or quits. The daemon resolves
optional album artwork through Last.fm and updates Discord over its local IPC
socket.

By default those snapshots travel over Cliamp's IPC broker, and the subscription
doubles as the liveness signal: pausing or stopping clears activity, and an
unclean Cliamp exit closes the stream and clears activity immediately. With
`transport = "file"` they are written to a state document the daemon watches
instead, and a heartbeat in that document stands in for the connection. Nothing
is written to disk unless that transport is selected.

The contract behind all of it — the snapshot's fields, the document's schema,
package responsibilities, and failure behavior — is in
[Architecture](docs/architecture.md).
