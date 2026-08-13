# Architecture

Cliamp Discord RPC is split into a sandboxed Lua plugin and a native Go daemon.
The split is required by Cliamp's security model: the plugin can observe player
events and write allowlisted files, but only the daemon can keep a connection to
Discord's local IPC socket and call Last.fm.

## Data Flow

```text
Cliamp events
  -> discord-rpc.lua
  -> ~/.local/share/cliamp/rpc-state.json
  -> cliamp-rpcd
       -> Last.fm track.getInfo
       -> Discord SET_ACTIVITY
```

The root-level `discord-rpc.lua` file is also the repository entrypoint required
by Cliamp's `cliamp-plugin-<name>` install-source convention.

## State Contract

The Lua plugin maintains a complete in-memory playback snapshot and writes JSON
schema version 1 after relevant events. Important fields are:

- `session` identifies one plugin load.
- `seq` increases for every write within a session.
- `status` is `playing`, `paused`, or `stopped`.
- `position` and `duration` are whole seconds.
- `updated_at` changes for playback changes and anchors Discord timestamps.
- `heartbeat` changes every 15 seconds and only proves plugin liveness.

The daemon combines `session` and `seq` to identify writes. Presence identity
excludes `heartbeat`, so heartbeat-only writes do not reset Discord's timeline.
The local `path` is retained for the state contract but is never included in a
Discord payload or Last.fm request.

## Repository Layout

The native application is grouped under `daemon/`, while the Cliamp plugin
entrypoint remains at the repository root:

```text
cliamp-plugin-discord-rpc/
├── daemon/
│   ├── cmd/cliamp-rpcd/
│   └── internal/
│       ├── artwork/
│       ├── config/
│       ├── daemon/
│       ├── discord/
│       ├── playback/
│       └── presence/
├── docs/
├── discord-rpc.lua
├── cliamp-rpcd.service
└── go.mod
```

All tests live in `daemon/tests/` and exercise package APIs from outside their
implementation directories. This keeps production and test files physically
separate while retaining Go's `internal` import protection: code beneath
`daemon/` is allowed to import `daemon/internal/...` packages.

## Go Packages

- `daemon/cmd/cliamp-rpcd` handles process startup and operating-system signals.
- `daemon/internal/config` loads flags, environment overrides, and
  `[plugins.discord-rpc]` from Cliamp's TOML config.
- `daemon/internal/playback` validates state documents and defines visibility rules.
- `daemon/internal/presence` builds typed Discord Listening activity payloads.
- `daemon/internal/artwork` resolves and caches album images from Last.fm.
- `daemon/internal/discord` implements socket discovery, framing, handshake, and
  `SET_ACTIVITY` over Discord IPC.
- `daemon/internal/daemon` polls state and coordinates the other packages.

## Playback Behavior

Playing tracks publish a Listening activity with title, artist, artwork, and a
client-rendered progress timeline. Paused and stopped playback clear the card.
Discord cannot freeze an activity timer, so clearing on pause is the only
reliable Spotify-like behavior. Resuming publishes a new activity anchored to
the saved playback position.

If the heartbeat exceeds the configured maximum age, the daemon treats the
state as stale and clears the activity. This handles an unclean Cliamp exit.

## Artwork

The daemon calls Last.fm `track.getInfo` with artist and title, selects the
largest valid HTTPS image, and caches both hits and misses for its lifetime. A
missing API key, failed lookup, or absent image falls back to the Discord
application asset configured by `-large-image`.

The bridge has dedicated credentials under `[plugins.discord-rpc]`; it never
reuses the separate `cliamp-lastfm` scrobbling plugin's key or session.

## Failure Handling

Discord connection failures leave the daemon running. Each later poll retries
socket discovery and handshake. A failed activity update closes the current
connection so the next poll reconnects cleanly. On SIGINT or SIGTERM, the
daemon clears the activity before closing the IPC socket.
