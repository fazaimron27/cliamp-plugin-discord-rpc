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

Download the `v1.0.0` archive for your Linux architecture and verify it against
[published checksums](https://github.com/fazaimron27/cliamp-plugin-discord-rpc/releases/download/v1.0.0/checksums.txt):

```sh
version=v1.0.0
case "$(uname -m)" in
  x86_64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
archive="cliamp-plugin-discord-rpc_${version}_linux_${arch}.tar.gz"
base_url="https://github.com/fazaimron27/cliamp-plugin-discord-rpc/releases/download/${version}"
curl -fLO "${base_url}/${archive}"
curl -fLO "${base_url}/checksums.txt"
sha256sum --ignore-missing -c checksums.txt
tar -xzf "${archive}"
cd "cliamp-plugin-discord-rpc_${version}_linux_${arch}"
```

Install the daemon and its systemd user service, then continue with the
configuration below before starting it:

```sh
install -Dm755 cliamp-rpcd ~/.local/bin/cliamp-rpcd
install -Dm644 cliamp-rpcd.service ~/.config/systemd/user/cliamp-rpcd.service
systemctl --user daemon-reload
```

See the [v1.0.0 release](https://github.com/fazaimron27/cliamp-plugin-discord-rpc/releases/tag/v1.0.0)
for release notes and direct asset downloads.

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
