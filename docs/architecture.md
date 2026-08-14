# Architecture

Cliamp Discord RPC is split into a sandboxed Lua plugin and a native Go daemon.
The plugin observes playback events and publishes local messages; the daemon
owns Discord IPC and optional Last.fm requests.

## Data Flow

```text
Cliamp events
  -> discord-rpc.lua
  -> p:publish("playback", snapshot, {retain = true})
  -> Cliamp in-memory event broker
  -> ~/.config/cliamp/cliamp.sock subscription
  -> cliamp-rpcd
       -> Last.fm track.getInfo
       -> Discord SET_ACTIVITY
```

The root-level `discord-rpc.lua` file is also the repository entrypoint required
by Cliamp's `cliamp-plugin-<name>` install-source convention. This transport
requires the temporary
[`feat/plugin-event-pubsub`](https://github.com/fazaimron27/cliamp/tree/feat/plugin-event-pubsub)
Cliamp branch until the API is available in an official release. Plugin release
v1.4.0 remains compatible with official Cliamp releases by using the former
state-file transport.

## Pub/Sub Contract

The plugin publishes the exact topic `plugin.discord-rpc.playback`. Cliamp
constructs that namespace from the installed plugin filename, so Lua code cannot
impersonate another plugin. Each payload is a complete playback snapshot:

- `status`: `playing`, `paused`, or `stopped`
- `title`, `artist`, and `album`
- `year`
- `duration` and `position` in whole seconds
- `stream`
- `path`, used only as private local track identity

The event is retained in Cliamp memory. A daemon that starts after playback has
begun receives the newest snapshot immediately. Retention is process-local and
is never written to disk.

Cliamp assigns a process-local event sequence and timestamp. The daemon uses the
event timestamp and playback position to derive Discord's timeline. Snapshots
whose positions match natural progression preserve the timeline; a track
change, resume, or position jump creates a new anchor.

## Repository Layout

```text
cliamp-plugin-discord-rpc/
├── daemon/
│   ├── cmd/cliamp-rpcd/
│   └── internal/
│       ├── artwork/
│       ├── cliamp/
│       ├── config/
│       ├── daemon/
│       ├── discord/
│       ├── playback/
│       └── presence/
├── docs/
├── discord-rpc.lua
├── install.sh
├── uninstall.sh
├── cliamp-rpcd.service
└── go.mod
```

## Go Packages

- `daemon/cmd/cliamp-rpcd` handles startup and operating-system signals.
- `daemon/internal/config` loads flags, environment overrides, and
  `[plugins.discord-rpc]` from Cliamp's TOML config.
- `daemon/internal/cliamp` subscribes to retained and live plugin events over
  Cliamp's owner-only Unix socket.
- `daemon/internal/playback` validates snapshots and derives private identity
  and public presence keys.
- `daemon/internal/presence` builds typed Discord Listening activities.
- `daemon/internal/artwork` resolves and caches album images from Last.fm.
- `daemon/internal/discord` implements socket discovery, framing, handshake,
  and `SET_ACTIVITY` over Discord IPC.
- `daemon/internal/daemon` coordinates subscriptions, artwork, timelines,
  reconnects, refreshes, and activity clearing.

## Playback Behavior

Playing tracks publish a Listening activity with title, artist, artwork, and a
client-rendered progress timeline. Paused and stopped playback clear the card.
Discord cannot freeze an activity timer, so clearing on pause is the reliable
Spotify-like behavior. Resuming publishes a new timeline anchored to the saved
position.

A subscription disconnect clears activity immediately. This handles clean and
unclean Cliamp exits without heartbeat expiry. The daemon reconnects with
bounded backoff and receives retained state when Cliamp is available again.

## Artwork

The daemon calls Last.fm `track.getInfo` with artist and title, selects the
largest valid HTTPS image, and caches both hits and misses for its lifetime. A
missing API key, failed lookup, or absent image falls back to the Discord
application asset configured by `--large-image`.

The community-maintained default Discord application ID is used unless a custom
ID is supplied through `--app-id`, `CLIAMP_DISCORD_APP_ID`, or
`plugins.discord-rpc.app_id`. Last.fm artwork is enabled only when a
`lastfm_api_key` is supplied.

## Failure Handling

Discord connection failures leave the daemon running. Presence refresh retries
reconnect even when playback state does not change. Failed activity updates
close the current Discord connection so the next attempt starts cleanly.

Cliamp subscription failures also leave the daemon running. It reconnects with
bounded backoff, clears stale Discord activity when the stream closes, and gets
the retained snapshot after reconnecting. On SIGINT or SIGTERM, the daemon
clears activity before closing Discord IPC.
