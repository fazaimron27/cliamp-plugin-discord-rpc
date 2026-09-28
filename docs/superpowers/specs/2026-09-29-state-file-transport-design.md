# State-file transport for cliamp-rpcd

Date: 2026-09-29
Issue: #26

Revised after implementation: the mismatch check, the document's field set, and
the liveness story changed while building this. Each is marked **Changed** below
with what it replaced, because the reason matters more than the outcome.

## Problem

The daemon speaks only Cliamp IPC v2. The only thing that serves an older Cliamp
is the frozen `v1.4.0` artifact: a duplicate of the whole daemon from before the
version relation, `--check`, and the current activity card. Making the state file
a selectable transport collapses two products back into one and lets the legacy
line retire.

## What was verified against Cliamp, not assumed

Cliamp at `eb59f54`; `go test ./luaplugin/` passes there.

- `cliamp.fs.write` still exists (`luaplugin/api_fs.go:107`, `docs/plugins.md:375`).
  Cliamp ships `plugins/now-playing.lua` using it, and its docs recommend
  "write to an allowed file for a watcher to read" for local automation.
- The write allowlist is built in and needs no declared permission: `/tmp/`,
  `~/.config/cliamp/`, `~/.local/share/cliamp/`, `~/Music/cliamp/`.
  `~/.local/share/cliamp/rpc-state.json` is inside it.
- `cliamp.fs.write`, `app.start`, `app.quit`, `cliamp.timer.every`, and
  `p:config` all landed together with the initial plugin system (`296722a`,
  2026-03-29, first tag v1.28.0). `p:publish` landed 2026-08-18 (`f373776`),
  which **no release tag contains yet**; the last tag without it is v1.63.2.
  So every Cliamp that can run a plugin has the file API, and the file transport
  is available on current Cliamp too — its audience is "every release ≤ v1.63.2
  plus main builds before the pub/sub merge", not "legacy only".
- `p:config(key)` reads `[plugins.<name>]` from the same config.toml the daemon
  reads (`luaplugin/luaplugin.go:411`), values arriving as quote-trimmed strings
  (`config/config.go:681`), and `nil` for a missing key (`luaplugin_test.go:200`).
  One TOML key can therefore configure both halves.
- `p:publish` returns `nil, err` when it refuses (`docs/plugins.md:172`), which
  is the convention the plugin's error path already follows.

## Decisions

**One joint setting, and only joint ones are shared.** `[plugins.discord-rpc]`
carries `transport = "ipc" | "file"` and `state_path = "..."`. Both halves read
the same file, so they agree by construction. `state_path` has **no** flag or
environment variable on purpose: an override this side could only be made where
the plugin cannot see it, leaving the daemon watching a path nothing writes.

**Changed — the overrides are daemon-only, and the mismatch is derived.**
`--transport` and `CLIAMP_DISCORD_TRANSPORT` override the file for the daemon
alone, which is the only way to desynchronize the halves, and the plugin has no
flag or environment of its own. The design first had the plugin declare the
transport it used in a `transport` field beside `plugin_version`, and the daemon
compare the two. That was dropped: it costs a field in the payload for every
snapshot to describe something only reachable by a deliberate override, and it
cannot catch the case the override itself creates, because the plugin's own
choice is not observable from a flag.

What replaced it: `config` records where its transport value came from
(`flag`, `environment`, `config file`, `default`), so the daemon can compare the
value it is running with against the value the plugin will read. They can only
differ when one side came from an override. The comparison is against the
plugin's *effective* value — the file's, or `ipc` when the file is silent — so
an explicit `--transport ipc` beside a file that says nothing is not reported as
a disagreement. `--check` reports the transport and its provenance on every run,
warns when the halves disagree, and names both values. It does not probe the
other transport: a report that silently reads a second source would be reporting
on a configuration the user does not have.

**Two timestamps, never interchanged.** The document carries `updated_at`,
bumped only on a real playback change, and `heartbeat`, bumped every 15 s:

- `updated_at` → `State.ObservedAt`, so `timelineTracker` runs unchanged.
- `heartbeat` → liveness only, against `--max-age` (default 45 s, two missed
  beats' worth of slack).

Feeding the heartbeat, or the file's mtime, into `ObservedAt` is the degradation
to avoid: each rewrite would advance the observed time while the position stood
still, blowing past the tracker's ±2 s continuity tolerance and re-anchoring the
progress bar every 15 s. v1.4.0 already had this split —
`presence/activity.go:85` anchored from `UpdatedAt`, while `IsPlaying` gated on
`heartbeat + maxAge` and `PresenceKey` excluded heartbeat and sequence.

**Changed — liveness covers both exits, and the channel never closes.** The
design said the plugin removes the document on quit, so a clean exit clears
activity without waiting out `--max-age`. That is `now-playing.lua`'s shape, not
v1.4.0's: `git show v1.4.0:discord-rpc.lua` sets `status = "stopped"` and touches
`updated_at` instead, leaving the file in place. The daemon therefore treats
both as the same event — a removed document and a document whose heartbeat has
aged out each become a `stopped` snapshot — and the plugin writes the stopped
document, matching what the release it supersedes wrote.

The subscription's channel is what the run loop reads, and the run loop treats a
**closed** channel as a transport to reconnect. So the watch's channel stays open
for the daemon's lifetime and delivers retractions as states. Cliamp quitting is
an event on the subscription, not the end of it.

**Changed — the document keeps v1.4.0's field names, without its extra ones.**
`v`, the playback fields, `updated_at`, `heartbeat`. The design carried
`session`, `seq`, and `transport` forward and left `path` out, "as v1.4.0
shipped". Dropping the write-only bookkeeping follows from not needing it:
`session`/`seq` identified a writer for a consumer that no longer exists, and
`transport` is gone as above. `path` is **in**: it is the private track identity
the daemon already uses to keep a resumed track's timeline, and a document
without it would make every restart of a track look like a new one.

`plugin_version` stays, from the snapshot payload, so the existing version
relation works unchanged for both transports. A v1.4.0 document has none, which
is the case the version watch already treats as silence.

**A version the daemon does not know is refused, not guessed.** `v` declares the
schema. A document declaring another one is rejected rather than read loosely,
because a newer schema may mean something different by the same field names. The
run loop stays quiet about it — a read that fails is most likely a document
caught mid-write, and the next write heals it. The permanent case is what
`statewatch.Inspect` exists for: `--check` reads the document once and says why
it cannot be used, which is the question `Subscribe` cannot answer, since it
delivers what changes and the failure mode is that nothing does.

**The watcher presents the IPC subscription's interface.** A restored
`daemon/internal/statewatch` package exposes `Subscribe(ctx, path, maxAge)
(<-chan playback.State)` beside `cliampipc.Subscribe`, so the run loop does not
branch on transport beyond choosing which one to open. It watches the parent
directory: the plugin writes with a plain write that replaces the file, so a
watch on the file itself would be watching an inode the next write leaves
behind. Reads are debounced (20 ms) until the writes stop.

**The plugin's fallback matches the daemon's default.** `p:config("transport")
or "ipc"`, so an unconfigured plugin and an unconfigured daemon agree, and the
mismatch check stays quiet for users who never touch the setting.

**Cost accepted:** `github.com/fsnotify/fsnotify v1.9.0` returns to `go.mod`
(dropped by the v2 cutover) with `golang.org/x/sys` indirect.

## Slices, as built

1. Config: `Transport`, `StatePath`, `StateMaxAge`, precedence, and strict
   validation of the transport value.
2. Config: record where the transport value came from, and derive the
   mismatch against the plugin's effective value.
3. `statewatch`: the document, its decode and validation, and its liveness
   arithmetic as a pure function (`lapseIn`/`liveAt`).
4. `statewatch`: the directory watcher, `Subscribe`, and `Inspect` for `--check`.
5. Run loop: choose the transport, start the matching subscription, and log
   which source is in use.
6. `--check`: report the transport and its provenance, and read the document
   instead of subscribing when the transport is `file`.
7. `discord-rpc.lua`: `p:config("transport")` at load, one snapshot builder with
   two emitters, a 15 s heartbeat, a document that moves `updated_at` only on
   change, and a stopped document on quit.
8. Docs: scope the "no state file" claim, document the option in the README, and
   describe the contract in `docs/architecture.md`.

Two guard layers hold the halves together, because nothing else can: a text
agreement test (`daemon/tests/transport_plugin_test.go`) for the values both
sides compute — the schema number, the default transport, the path under `$HOME`,
the heartbeat against the window — which runs everywhere; and a runtime test
(`daemon/tests/transport_plugin_runtime_test.go`) that runs the real plugin under
a stub Cliamp and feeds what it wrote to the daemon's own decoder, which runs
wherever a Lua interpreter is installed.

## Out of scope

- Retiring the legacy line (#25) — this has to ship first.
- `--poll`, v1.4.0's fallback for filesystems without watch support. Unless a
  real user hits it, the watcher's error is reported and that is all.
- Luacheck in CI (#27), which would want to know that
  `daemon/tests/testdata/plugin_driver.lua` is a test double, not plugin code.
