# Cliamp Discord RPC Plugin

Discord Rich Presence for [Cliamp](https://www.cliamp.stream/). The Lua plugin
writes the current playback state to disk, and the `cliamp-rpcd` daemon sends it
to the local Discord desktop client.

## Prerequisites

Before installing, make sure you have:

- Linux on `x86_64`/`amd64` or `aarch64`/`arm64`.
- Cliamp installed and available as `cliamp`.
- The Discord desktop client. Discord in a web browser does not expose the local
  IPC socket used by Rich Presence.
- A Discord account and access to the
  [Discord Developer Portal](https://discord.com/developers/applications).
- A Last.fm account for creating an API key.
- Git for obtaining the repository.
- `curl`, `sha256sum`, `tar`, and `systemctl` when installing from a release.
- Go 1.25 or newer only when self-deploying from source.

The daemon and Discord must run in the same desktop user session. The supplied
service is a systemd user service and does not require root access.

## 1. Create a Discord application

1. Open the [Discord Developer Portal](https://discord.com/developers/applications).
2. Select **New Application**.
3. Enter a name such as `Cliamp`, accept Discord's terms, and create the application.
4. Open **General Information** and copy the **Application ID**. This is the
   value used as `app_id` later.
5. Open **Rich Presence**, then **Art Assets**.
6. Upload a square image with the asset name `cliamp`.

Discord can only display assets registered to your application. Newly uploaded
assets can take several minutes to become available.

## 2. Create a Last.fm API key

The Last.fm key is used to look up album artwork for the current track.

1. Sign in to Last.fm.
2. Open [Create an API account](https://www.last.fm/api/account/create).
3. Enter an application name, for example `Cliamp Discord RPC`.
4. Enter a short description, for example `Album artwork for Discord Rich Presence`.
5. Leave **Callback URL** empty. This integration does not use Last.fm login or
   account authorization.
6. Enter this repository URL as the application homepage:
   `https://github.com/fazaimron27/cliamp-plugin-discord-rpc`.
7. Submit the form.
8. Copy the generated **API key**. The **Shared secret** is not required and
   should not be added to the Cliamp configuration.

Album artwork is optional. You can leave `lastfm_api_key` empty; the Discord
application asset named `cliamp` will be used when artwork is unavailable.

## 3. Configure credentials

Create the configuration directory and file if they do not exist:

```sh
mkdir -p ~/.config/cliamp
touch ~/.config/cliamp/config.toml
chmod 600 ~/.config/cliamp/config.toml
```

Open `~/.config/cliamp/config.toml` in your editor and add:

```toml
[plugins.discord-rpc]
app_id = "YOUR_DISCORD_APPLICATION_ID"
lastfm_api_key = "YOUR_LASTFM_API_KEY"
```

Replace `YOUR_DISCORD_APPLICATION_ID` with the Application ID from step 1 and
`YOUR_LASTFM_API_KEY` with the API key from step 2. If you do not want album
artwork, use an empty value:

```toml
lastfm_api_key = ""
```

Protect the API credentials by keeping the configuration file readable only by
your user. The `chmod` command above sets the required permissions.

## 4A. Install from release

Use this path for a normal installation on `amd64` or `arm64`. It installs the
plugin through Cliamp and downloads the published `v1.1.0` daemon; Go is not
required.

### Install the plugin

```sh
cliamp plugins install fazaimron27/cliamp-plugin-discord-rpc
cliamp plugins trust discord-rpc
```

Review the source, SHA-256 hash, declared permissions, and filesystem access
shown by Cliamp before approving it. Restart Cliamp after installation.

### Install the daemon

Clone the repository so you can review and run the installer:

```sh
git clone https://github.com/fazaimron27/cliamp-plugin-discord-rpc.git
cd cliamp-plugin-discord-rpc
less install.sh
./install.sh --version v1.1.0
```

The installer:

- Detects `amd64` or `arm64`.
- Downloads the matching archive from the
  [v1.1.0 release](https://github.com/fazaimron27/cliamp-plugin-discord-rpc/releases/tag/v1.1.0).
- Verifies the archive against the published SHA-256 checksum.
- Installs `cliamp-rpcd` to `~/.local/bin`.
- Installs `cliamp-rpcd.service` as a systemd user service.
- Reloads the systemd user manager without starting the daemon.

Run `./install.sh --help` to see version and destination overrides. Continue at
[Start and verify](#5-start-and-verify).

## 4B. Self-deploy from source

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

Restart Cliamp after installing the Lua plugin. When you edit that file later,
run `cliamp plugins trust discord-rpc` again to approve its new hash, then
restart Cliamp.

## 5. Start and verify

1. Start the Discord desktop client and sign in.
2. Start or restart Cliamp and play a track.
3. Enable and start the daemon:

```sh
systemctl --user enable --now cliamp-rpcd
```

4. Check the service:

```sh
systemctl --user status cliamp-rpcd
journalctl --user -u cliamp-rpcd -f
```

A playing track should appear on your Discord profile. Pausing or stopping
playback clears the activity. The daemon reconnects automatically if Discord is
started or restarted later.

To run the daemon in the foreground instead of using systemd:

```sh
systemctl --user stop cliamp-rpcd
~/.local/bin/cliamp-rpcd
```

Run `~/.local/bin/cliamp-rpcd --help` for all daemon options.

## Troubleshooting

### Discord activity does not appear

- Confirm the Discord desktop client is running under the same Linux user.
- Confirm `app_id` exactly matches the Discord Application ID.
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

The Application ID is required. The Last.fm API key is optional.

## How it works

The plugin writes playback state to
`~/.local/share/cliamp/rpc-state.json`. The daemon reads that file, resolves
optional album artwork through Last.fm, and updates Discord through its local
IPC socket. Heartbeats clear stale activity after an unclean Cliamp exit.

See [Architecture](docs/architecture.md) for the state contract, package
responsibilities, artwork flow, and failure behavior.
