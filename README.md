# Cliamp Discord RPC Plugin

Discord Rich Presence for [Cliamp](https://www.cliamp.stream/). The Lua plugin
publishes retained playback snapshots through Cliamp's in-memory IPC pub/sub
broker, and the `cliamp-rpcd` daemon forwards them to the local Discord desktop
client. Playback state is not written to disk.

> [!NOTE]
> This project is currently developed and tested only on Linux. Prebuilt daemon
> releases are available for `x86_64`/`amd64` and `aarch64`/`arm64`.

## Prerequisites

Before installing, make sure you have:

- Cliamp with plugin event pub/sub support. Until that support is included in
  an official release, build and install a compatible Cliamp checkout first.
- The Discord desktop client. Discord in a web browser does not expose the local
  IPC socket used by Rich Presence.
- A Discord account signed in to the desktop client.
- `curl`, `gh`, `sha256sum`, `tar`, and `systemctl` when installing from a release.
- Git and Go 1.25 or newer only when self-deploying from source.

The daemon and Discord must run in the same desktop user session. The supplied
service is a systemd user service and does not require root access.

No Discord Developer Portal registration or Last.fm API key is required. The
daemon uses the community-maintained Cliamp Discord application by default and
displays its static artwork. Album artwork through Last.fm is an optional
enhancement.

## Install from release

Use this path for a normal installation on `amd64` or `arm64`. It installs the
plugin through Cliamp and downloads the published `v1.5.0` daemon; Go is not
required.

### Install the plugin

```sh
cliamp plugins install fazaimron27/cliamp-plugin-discord-rpc
cliamp plugins trust discord-rpc
```

Review the source, SHA-256 hash, declared permissions, and filesystem access
shown by Cliamp before approving it. Restart Cliamp after installation.

### Install the daemon

Install the daemon directly from this repository:

```sh
curl -fsSL https://raw.githubusercontent.com/fazaimron27/cliamp-plugin-discord-rpc/main/install.sh | sh
```

This command downloads code and executes it. To review the installer first:

```sh
curl -fsSL -o install.sh https://raw.githubusercontent.com/fazaimron27/cliamp-plugin-discord-rpc/main/install.sh
less install.sh
sh install.sh
rm install.sh
```

The installer:

- Detects `amd64` or `arm64`.
- Downloads the matching archive from the
  [v1.5.0 release](https://github.com/fazaimron27/cliamp-plugin-discord-rpc/releases/tag/v1.5.0).
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
curl -fsSL https://raw.githubusercontent.com/fazaimron27/cliamp-plugin-discord-rpc/main/uninstall.sh | sh
```

To review the uninstaller first, download it with `curl -fsSL -o uninstall.sh`,
inspect it, then run `sh uninstall.sh`.

The uninstaller stops and disables the user service if it is active, removes the
daemon and unit file, and preserves the Cliamp plugin, configuration, and
playback state. Use `--bin-dir` and `--service-dir` if you installed to custom
locations. Continue at [Start and verify](#start-and-verify).

## Self-deploy from source

Use this path for development, an architecture without a prebuilt archive, or
when you want to audit and build every installed file yourself. This path does
not use the release installer.

### Build the daemon

```sh
git clone https://github.com/fazaimron27/cliamp-plugin-discord-rpc.git
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
restart Cliamp. If the daemon reports that Cliamp rejected the subscription,
your Cliamp build does not yet provide plugin event pub/sub.

## Start and verify

1. Start the Discord desktop client and sign in.
2. Start or restart Cliamp and play a track.
3. Run the daemon manually:

```sh
~/.local/bin/cliamp-rpcd
```

A playing track should appear on your Discord profile. Keep this terminal open
while using the daemon and press `Ctrl+C` to stop it. Pausing or stopping
playback clears the activity, and the daemon reconnects automatically if
Discord is started or restarted later.

Run `~/.local/bin/cliamp-rpcd --help` for all daemon options. The daemon
subscribes to `plugin.discord-rpc.playback` on Cliamp's owner-only local IPC
socket and reconnects automatically when Cliamp restarts.

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

## Troubleshooting

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

### The service fails immediately

Run the daemon in the foreground to see the configuration error directly:

```sh
systemctl --user stop cliamp-rpcd
~/.local/bin/cliamp-rpcd
```

The built-in Application ID is used unless a custom value is supplied. The
Last.fm API key is optional and artwork lookup is disabled when it is empty.

## How it works

The plugin publishes a complete playback snapshot to the retained
`plugin.discord-rpc.playback` topic whenever Cliamp starts, changes track,
changes playback state, seeks, or quits. Cliamp keeps only the latest snapshot
in memory and immediately replays it to a newly connected daemon. The daemon
resolves optional album artwork through Last.fm and updates Discord through its
local IPC socket.

The subscription connection is also the liveness signal. Pausing or stopping
clears activity; an unclean Cliamp exit closes the stream and clears activity
immediately. The daemon reconnects with bounded backoff and receives the latest
retained snapshot after Cliamp returns. No playback state file, filesystem
watcher, heartbeat, or polling loop is used.

See [Architecture](docs/architecture.md) for the state contract, package
responsibilities, artwork flow, and failure behavior.
