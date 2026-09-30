# Architecture

Cliamp Discord RPC is split into a sandboxed Lua plugin and a native Go daemon.
The plugin observes playback events and hands playback snapshots to the daemon,
which owns Discord IPC and optional Last.fm requests. Two transports carry those
snapshots, and the daemon treats them alike: its subscription and its state-file
watch both deliver the same playback snapshots, so nothing downstream knows
which one is in use.

## Data Flow

```text
Cliamp events
  -> discord-rpc.lua                  transport: ipc (default) or file
       ipc:   p:publish("playback", snapshot, {retain = true})
              -> Cliamp in-memory event broker
              -> ~/.config/cliamp/cliamp.sock subscription
       file:  cliamp.fs.write(the snapshot as a document)
              -> ~/.local/share/cliamp/rpc-state.json
              -> fsnotify watch on the state directory
  -> cliamp-rpcd
       -> Last.fm track.getInfo
       -> Discord SET_ACTIVITY
```

The root-level `discord-rpc.lua` file is also the repository entrypoint required
by Cliamp's `cliamp-plugin-<name>` install-source convention. The `ipc`
transport requires the retained plugin event pub/sub API merged into Cliamp's
official `main` branch in
[`f373776d`](https://github.com/bjarneo/cliamp/commit/f373776d).

The `file` transport asks nothing of Cliamp beyond `cliamp.fs`, so this release
also serves builds that predate the pub/sub API. It is not a legacy-only path:
it is a second transport for the same plugin, selected by the same
`plugins.discord-rpc.transport` key that the daemon reads, so one setting moves
both halves. Neither half needs to be told what the other chose.

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
is not warned about. The sentence it prints comes from `version.Explain`, which
the `--check` diagnostic prints too, so the two describe one pairing one way; only
the running daemon appends the remedy, because only it knows it is running. The
daemon's own release identity lives in
`daemon/internal/version`, which the `--version` flag, the startup log, and the
Last.fm `User-Agent` all read.

The subscription request must carry Cliamp's version 2 IPC envelope. Cliamp
rejects an unversioned frame with a structured `invalid_version` error instead
of interpreting it, so the daemon sends
`{"version":2,"id":...,"method":"subscribe","topics":[...]}` and validates the
acknowledged version and error object. See Cliamp's `docs/upgrading-ipc-v2.md`.

Version 2 is mandatory rather than negotiated: an older build refuses the
subscription instead of falling back to version 1, so the two failures a user can
hit here name which Cliamp they are running. `invalid_version` means the
executable predates the
[version 2 IPC cutover](https://github.com/bjarneo/cliamp/commit/c75cdec);
`unknown operation`, or a missing `publish`, means it predates the
[retained plugin event pub/sub merge](https://github.com/bjarneo/cliamp/commit/f373776d).
The `file` transport is what serves a build older than both, which is why it asks
nothing of Cliamp beyond `cliamp.fs`.

The event is retained in Cliamp memory. A daemon that starts after playback has
begun receives the newest snapshot immediately. Retention is process-local and
is never written to disk.

Cliamp assigns a process-local event sequence and timestamp. The timestamp is
**Unix seconds**: Cliamp publishes `time.Now().Unix()`, its own plugin docs show a
ten-digit `time` in the sample frame, and nothing in Cliamp reads the field back —
so the daemon is its only interpreter, and the unit is this side's assumption to
keep rather than one it inherits. It matters because the daemon derives Discord's
timeline from the event timestamp and the playback position: a frame that arrived
in milliseconds would put the observation some 56,000 years ahead of the clock and
re-anchor the progress bar on every snapshot, which is indistinguishable from the
re-anchoring a real track change causes. Snapshots whose positions match natural
progression preserve the timeline; a track change, resume, or position jump
creates a new anchor.

## State File Contract

The `file` transport writes one JSON document at
`~/.local/share/cliamp/rpc-state.json` (`plugins.discord-rpc.state_path`
overrides the path). It carries the same playback snapshot the pub/sub payload
does, with two additions and one rule:

- `updated_at` is the snapshot's change time, and it moves only when the
  playback fields do. It becomes the snapshot's observation time, which is what
  the timeline is derived from, so a document whose change time moved while the
  position stood still would re-anchor Discord's progress bar on a track that
  had not moved. It is Unix seconds, the same unit as the pub/sub event
  timestamp, because both land in the same observation time: the two transports
  are interchangeable only while their clocks agree on that.
- `heartbeat` is refreshed on a 15-second timer whether or not anything changed.
  The daemon never reads it as playback state. It is the liveness signal the
  subscription connection gives the `ipc` transport, standing in for a signal a
  file cannot carry: a document whose heartbeat is older than
  `--max-age` (45 s by default, two missed beats) is not evidence that Cliamp is
  running.
- `v` declares the document's schema, currently 1. A document declaring another
  schema is refused rather than read loosely, because a version this daemon has
  not been taught may mean something different by the same field names. The
  refusal is silent in the run loop and visible in `--check`, which reads the
  document and reports why it could not be used.

The daemon watches the document's directory rather than the document, because
the plugin writes with a plain write that replaces the file: a watch on the file
would be watching an inode the next write leaves behind. Reads are debounced
until the writes stop, and a read that fails or lands mid-write is ignored
rather than treated as evidence about the track, since the next write heals it.

Unlike the subscription, the watch has no close event to play the part of
Cliamp quitting, so a state change is delivered instead: a document that is
removed, or one whose heartbeat ages out, becomes a `stopped` snapshot. The
channel stays open for the daemon's lifetime, because a closed channel means
"reconnect" to the run loop and there is nothing to reconnect. This is the same
route a `stopped` payload takes over IPC: the activity is cleared.

The `file` transport therefore also reads the documents release v1.4.0 wrote:
same field names, same schema, same default path, so an install that still has
one keeps working across the upgrade. Documents from that line carry no
`plugin_version`, so the version warning stays silent for them, exactly as it
does for an `ipc` snapshot from a plugin that predates the report.

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
│       ├── presence/
│       ├── statewatch/
│       └── tracklink/
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
  `[plugins.discord-rpc]` from Cliamp's TOML config. The transport is the one
  setting that belongs to both halves at once, so this package records where its
  value came from and can say when a flag or environment override has the plugin
  reading a different one. `--transport` and `CLIAMP_DISCORD_TRANSPORT` are the
  two overrides that can do it; they exist for troubleshooting rather than
  configuration, and a daemon running under one warns at startup.
- `daemon/internal/cliamp` subscribes to retained and live plugin events over
  Cliamp's owner-only Unix socket.
- `daemon/internal/statewatch` reads and watches the state document. It presents
  the same interface as the subscription — a channel of playback snapshots — so
  the run loop does not branch on the transport, and it owns the document's
  liveness rules.
- `daemon/internal/playback` validates snapshots and derives private identity
  and public presence keys.
- `daemon/internal/presence` builds typed Discord Listening activities.
- `daemon/internal/artwork` resolves and caches album images from Last.fm.
- `daemon/internal/tracklink` maps a playback path to the public page it belongs
  to. It holds no network and no dependency beyond the standard library, because
  every value it is handed comes from a provider or a player: it is a refusal
  machine first, and a path it does not recognise is published nowhere.
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

The state file has no connection to close, so its equivalent is a retraction on
the same channel: a removed document, or one whose heartbeat has aged out,
arrives as a `stopped` snapshot and clears activity the same way. A Cliamp that
crashes mid-track leaves its last document behind, and the heartbeat window is
what stops that document from holding the card indefinitely.

## Artwork

The daemon calls Last.fm `track.getInfo` with artist and title and selects the
largest valid HTTPS image. The same response carries the track's own page and
its artist's page, which the resolver returns alongside the image: reading them
costs no request the artwork did not already make. What a lookup found is
remembered with an expiry: an entry naming an image is reused for an hour, while
an answer carrying no image is retried after 30 seconds. A track's artwork does
not change while it plays, but whether Last.fm could supply it can, so a
transient miss outlives nothing and the resolver does not accumulate a lookup
for every track the daemon has ever seen. A missing API key, failed lookup, or
absent image falls back to the Discord application asset configured by
`--large-image`.

Lookups run on their own goroutine, and the loop publishes a Listening activity
as soon as it knows the track — with artwork when the answer is already in hand,
and again when a lookup that was still open returns one. The loop is the only
thing that talks to Discord, so a request it waited on would queue every pause,
stop, and track change behind Last.fm for up to the client's four-second
timeout.

The community-maintained default Discord application ID is used unless a custom
ID is supplied through `--app-id`, `CLIAMP_DISCORD_APP_ID`, or
`plugins.discord-rpc.app_id`.

Last.fm is consulted only when a `lastfm_api_key` is supplied, and the resolver
returns before any request when it is absent, so a keyless daemon makes no
lookup at all. That withholds both the artwork and the exact track and artist
pages, since one response carries all three. The pages are never constructed
from the artist and title instead: Last.fm's own slug rules fold case and
rewrite punctuation, so a built URL is correct for most tracks and quietly wrong
for the rest, which is the failure `internal/tracklink` exists to refuse. The
card falls back to a Last.fm search, which needs no key.

## Card Links

The title, the artist, and the album art are links when the daemon knows where
they should point, and the first button is labelled for its destination so the
card never promises Last.fm and then opens Spotify. None of this costs a
request: each URL is either already in the payload or a string parse of it.

The track link is the provider's own page when the playback path identifies one,
otherwise the Last.fm page from the same lookup that supplied the artwork,
otherwise nothing — the title is not linked to a search page. The button falls
back one step further, to the Last.fm search that works with no key configured,
and is dropped entirely for a stream that resolves to nothing.

`internal/tracklink` owns the path-to-URL mapping, and both of its mechanisms
are allowlists. A `spotify:track:` URI is translated into a public page after
its id is charset-checked. A YouTube watch URL is checked against a host
allowlist and then **rebuilt from the video id**, so `list=`, `index=` and `t=`
cannot ride along and the host that is published is one this daemon
constructed. A path matching neither is published nowhere, which is what keeps a
local filename and the credential-bearing stream URLs of the five self-hosted
providers off the card.

The artist follows a different precedence, because no artist id exists anywhere
in the pipeline: providers do not hand one to a plugin, and `ProviderMeta` never
crosses that boundary. So the artist links to the provider's own artist search
for that name when the path identified one, otherwise to the exact artist page
when Last.fm supplies one, otherwise to Last.fm's search — which makes the
artist linkable in more cases than the track is, since the last two tiers need
neither a key nor an id.

The provider search outranks the exact page deliberately. A provider link means
the listener is playing from that service, and the artist should not point
somewhere other than the title and the button beside it. Ordering these the
other way round would leave the provider tier unreachable for anyone with a
Last.fm key, since a successful lookup returns an artist page almost every time.

## Failure Handling

Discord connection failures leave the daemon running. Presence refresh retries
reconnect even when playback state does not change. Failed activity updates
close the current Discord connection so the next attempt starts cleanly.

Cliamp subscription failures also leave the daemon running. It reconnects with
bounded backoff, clears stale Discord activity when the stream closes, and gets
the retained snapshot after reconnecting. On SIGINT or SIGTERM, the daemon
clears activity before closing Discord IPC.

A frame the daemon cannot decode, and a snapshot it rejects as invalid, are each
logged and dropped. A rejected snapshot is never republished, so Discord keeps
showing the last good activity, and the log line is the only account of why
presence stopped moving. Each discard names its own reason, because the two have
different causes: an undecodable frame means the transport is broken, while a
rejected snapshot means a complete frame carried values the daemon will not
accept.

## Testing

Every test lives beside the package it tests, in that package's directory. There
is no separate test tree, so the directory a test is found in is the package it
covers. Which of the two test packages it is written as says what it is allowed
to see, and that choice is the whole rule:

- the **external test package** (`playback_test`) can reach only the exported
  surface, so a rename or a removed symbol inside the package cannot silently
  keep it passing — this is where a contract test belongs;
- the **package itself** (`playback`) is for a test whose subject has no public
  surface at all, such as a value the API deliberately does not report.

The plugin is half the shipped surface and none of it is Go, so the payload
contract above is enforced by running the real `discord-rpc.lua` against a stub
of Cliamp's plugin API and holding what it publishes to `playback.State`. The
harness is `daemon/internal/playback/testdata/lua`, driven by
`daemon/internal/playback/lua_contract_test.go`, which asserts that the published
keys are exactly the ones the daemon accepts, that nothing the daemon would
reject is published, and that a scenario publishing nothing is caught rather
than passing vacuously. A field renamed on either side of the Lua/Go boundary
fails there.

Those tests need `luajit` and skip without it, so a local `go test ./...` can
pass with them unrun. CI installs `luajit` and sets `CLIAMP_REQUIRE_LUA`, which
turns that skip into a failure, so the suite cannot pass by skipping there.
`luacheck` lints the shipped plugin in the same job, configured by
`.luacheckrc`.
