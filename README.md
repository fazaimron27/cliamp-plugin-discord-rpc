# Cliamp Discord RPC Plugin

Discord Rich Presence bridge for [Cliamp](https://www.cliamp.stream/). A sandboxed Lua hook publishes playback state to a JSON file, and `cliamp-rpcd` sends that state to the local Discord desktop client over Discord IPC.

See [Architecture](docs/architecture.md) for the state contract, package
responsibilities, artwork flow, and failure behavior.

## Discord setup

1. Create an application in the [Discord Developer Portal](https://discord.com/developers/applications).
2. Copy its Application ID.
3. Under **Rich Presence > Art Assets**, upload a square Cliamp image named `cliamp`. Asset changes can take several minutes to propagate.

Discord only renders assets registered for the application. Local cover files cannot be passed through RPC. This bridge uses the `cliamp` application asset and shows the album as its hover text.

## Install plugin

This repository follows Cliamp's install-source convention:

- Repository: `cliamp-plugin-discord-rpc`
- Entrypoint: `discord-rpc.lua`
- Installed plugin name: `discord-rpc`

Install it through Cliamp's plugin manager:

```sh
cliamp plugins install fazaimron27/cliamp-plugin-discord-rpc
cliamp plugins trust discord-rpc
```

The install command displays the source, SHA-256 hash, declared permissions,
and implicit filesystem access before asking for approval. Use `--yes` only
after independently reviewing the same source.

For local development, copy the entrypoint manually instead:

```sh
mkdir -p ~/.config/cliamp/plugins
cp ./discord-rpc.lua ~/.config/cliamp/plugins/discord-rpc.lua
chmod 644 ~/.config/cliamp/plugins/discord-rpc.lua
cliamp plugins trust discord-rpc
```

Restart Cliamp after installing or updating the plugin.

## Install daemon

Review the installer, then run it from a repository checkout:

```sh
less ./install.sh
./install.sh
```

The installer detects Linux `amd64` or `arm64`, downloads the `v1.0.0` release,
verifies the archive against the published SHA-256 checksum, and installs the
daemon and systemd user service. It does not start the service before you add
your Discord credentials below.

To install another release or use custom destinations:

```sh
./install.sh --version v1.0.0 \
  --bin-dir "$HOME/.local/bin" \
  --service-dir "${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
```

The same script is included in future release archives and installs their
packaged binary without downloading it again. Run `./install.sh --help` for all
options.

For a manual installation, download the archive and `checksums.txt` from the
[v1.0.0 release](https://github.com/fazaimron27/cliamp-plugin-discord-rpc/releases/tag/v1.0.0),
verify it with `sha256sum -c checksums.txt`, extract it, then install the files:

```sh
install -Dm755 cliamp-rpcd ~/.local/bin/cliamp-rpcd
install -Dm644 cliamp-rpcd.service ~/.config/systemd/user/cliamp-rpcd.service
systemctl --user daemon-reload
```

## Build daemon from source

```sh
go build -o cliamp-rpcd ./daemon/cmd/cliamp-rpcd
chmod 755 ./cliamp-rpcd
```

## Run

When not using the systemd service, start the Discord desktop client and run:

```sh
./cliamp-rpcd
```

The default state path is `~/.local/share/cliamp/rpc-state.json`. Run `./cliamp-rpcd --help` for all options.

The daemon reads its dedicated credentials from `[plugins.discord-rpc]` in
`~/.config/cliamp/config.toml`:

```toml
[plugins.discord-rpc]
app_id = "YOUR_DISCORD_APPLICATION_ID"
lastfm_api_key = "YOUR_DEDICATED_LASTFM_API_KEY"
```

This is intentionally separate from `[plugins.cliamp-lastfm]`, which belongs to
the scrobbling plugin. The Discord bridge uses Last.fm's `track.getInfo` API to
display each track's album artwork. Artwork lookups are cached for the lifetime
of the daemon, and the configured Discord application asset remains the
fallback. `CLIAMP_DISCORD_APP_ID` and `CLIAMP_DISCORD_LASTFM_API_KEY` override
the values in this section.

## systemd user service

The release installation above puts the service in the expected user directory.
For a source build, install both files manually:

```sh
install -Dm755 ./cliamp-rpcd ~/.local/bin/cliamp-rpcd
install -Dm644 ./cliamp-rpcd.service ~/.config/systemd/user/cliamp-rpcd.service
```

The service reads `app_id` and `lastfm_api_key` from
`~/.config/cliamp/config.toml`; no systemd environment override is required.
Enable it with:

```sh
systemctl --user daemon-reload
systemctl --user enable --now cliamp-rpcd
journalctl --user -u cliamp-rpcd -f
```

After editing the Lua plugin, approve its new hash with `cliamp plugins trust discord-rpc` and restart Cliamp.

## Behavior

- Playing tracks use Discord's Listening activity type and show an elapsed/remaining timeline.
- Paused or stopped playback, Cliamp shutdown, or a stale heartbeat clears the activity.
- Resuming playback restores the activity and timeline from the saved position.
- The daemon reconnects automatically when Discord starts or restarts.
