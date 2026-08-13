# CLIamp Discord RPC Plugin

Discord Rich Presence bridge for [CLIamp](https://www.cliamp.stream/). A sandboxed Lua hook publishes playback state to a JSON file, and `cliamp-rpcd` sends that state to the local Discord desktop client over Discord IPC.

See [Architecture](docs/architecture.md) for the state contract, package
responsibilities, artwork flow, and failure behavior.

## Discord setup

1. Create an application in the [Discord Developer Portal](https://discord.com/developers/applications).
2. Copy its Application ID.
3. Under **Rich Presence > Art Assets**, upload a square CLIamp image named `cliamp`. Asset changes can take several minutes to propagate.

Discord only renders assets registered for the application. Local cover files cannot be passed through RPC. This bridge uses the `cliamp` application asset and shows the album as its hover text.

## Install plugin

This repository follows CLIamp's install-source convention:

- Repository: `cliamp-plugin-discord-rpc`
- Entrypoint: `discord-rpc.lua`
- Installed plugin name: `discord-rpc`

Install it through CLIamp's plugin manager:

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

Restart CLIamp after installing or updating the plugin.

## Build daemon

```sh
go build -o cliamp-rpcd ./daemon/cmd/cliamp-rpcd
chmod 755 ./cliamp-rpcd
```

## Run

Start the Discord desktop client, then run:

```sh
./cliamp-rpcd
```

The default state path is `~/.local/share/cliamp/rpc-state.json`. Run `./cliamp-rpcd -help` for all options.

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

```sh
mkdir -p ~/.local/bin ~/.config/systemd/user
cp ./cliamp-rpcd ~/.local/bin/cliamp-rpcd
chmod 755 ~/.local/bin/cliamp-rpcd
cp ./cliamp-rpcd.service ~/.config/systemd/user/cliamp-rpcd.service
chmod 644 ~/.config/systemd/user/cliamp-rpcd.service
```

The service reads `app_id` and `lastfm_api_key` from
`~/.config/cliamp/config.toml`; no systemd environment override is required.
Enable it with:

```sh
systemctl --user daemon-reload
systemctl --user enable --now cliamp-rpcd
journalctl --user -u cliamp-rpcd -f
```

After editing the Lua plugin, approve its new hash with `cliamp plugins trust discord-rpc` and restart CLIamp.

## Behavior

- Playing tracks use Discord's Listening activity type and show an elapsed/remaining timeline.
- Paused or stopped playback, CLIamp shutdown, or a stale heartbeat clears the activity.
- Resuming playback restores the activity and timeline from the saved position.
- The daemon reconnects automatically when Discord starts or restarts.
