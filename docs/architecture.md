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
requires the retained plugin event pub/sub API merged into Cliamp's official
`main` branch in
[`f373776d`](https://github.com/bjarneo/cliamp/commit/f373776d). Plugin release
v1.4.0 remains compatible with older Cliamp releases by using the former
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
- `plugin_version`, the release that published the snapshot, used only to report
  a mismatched plugin/daemon pairing

The daemon logs a warning once per distinct `plugin_version` whose major.minor
line differs from its own, then keeps running. The warning names the half that is
behind and prints the command that updates it, because the two halves are
installed independently: the plugin from Cliamp's plugin installer, the daemon
from a release archive or a source build, so either one can be the stale side. A
patch difference is silent: it cannot change this payload, so there is nothing for
the user to act on. A plugin old enough to omit the field predates the report and
is not warned about. The daemon's own release identity lives in
`daemon/internal/version`, which the `--version` flag, the startup log, and the
Last.fm `User-Agent` all read.

The subscription request must carry Cliamp's version 2 IPC envelope. Cliamp
rejects an unversioned frame with a structured `invalid_version` error instead
of interpreting it, so the daemon sends
`{"version":2,"id":...,"method":"subscribe","topics":[...]}` and validates the
acknowledged version and error object. See Cliamp's `docs/upgrading-ipc-v2.md`.

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
  reconnects, refreshes, and activity clearing. It also owns the `--check`
  diagnostic, which probes the same transports in isolation and reports them
  without starting the run loop.

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

The daemon calls Last.fm `track.getInfo` with artist and title and selects the
largest valid HTTPS image. What a lookup found is remembered with an expiry: a
resolved URL is reused for an hour, while an answer carrying no image is retried
after 30 seconds. A track's artwork does not change while it plays, but whether
Last.fm could supply it can, so a transient miss outlives nothing and the
resolver does not accumulate a lookup for every track the daemon has ever seen.
A missing API key, failed lookup, or absent image falls back to the Discord
application asset configured by `--large-image`.

Lookups run on their own goroutine, and the loop publishes a Listening activity
as soon as it knows the track — with artwork when the answer is already in hand,
and again when a lookup that was still open returns one. The loop is the only
thing that talks to Discord, so a request it waited on would queue every pause,
stop, and track change behind Last.fm for up to the client's four-second
timeout.

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
