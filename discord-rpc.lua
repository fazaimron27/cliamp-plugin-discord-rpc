-- discord-rpc: publishes Cliamp playback state for cliamp-rpcd.
--
-- The state leaves this plugin one of two ways. Over Cliamp's local IPC pub/sub
-- stream, which is what the daemon subscribes to, or in a state document
-- written with cliamp.fs, for Cliamp builds whose plugin events cannot carry a
-- retained snapshot. A separate daemon owns Discord IPC either way.
--
-- The transport is read from config.toml, the same file cliamp-rpcd reads, so
-- one setting moves both halves.

-- Single source of truth for this plugin's release. The manifest and every
-- published snapshot must agree, and cliamp-rpcd warns when its own release
-- line differs from the value published here.
local VERSION = "1.10.1"

-- The transport used when config.toml names none. cliamp-rpcd defaults to the
-- same value, which is what lets it tell a deliberate override apart from a
-- plugin that was simply never configured: they disagree only when one side has
-- been set.
local DEFAULT_TRANSPORT = "ipc"

-- The state document's shape and pace. Both are part of the contract with
-- cliamp-rpcd: it refuses a document whose schema it does not know, and it
-- stops believing one whose heartbeat has stopped.
local SCHEMA_VERSION = 1
local HEARTBEAT_SECS = 15

local STATE_DIR = (os.getenv("HOME") or "") .. "/.local/share/cliamp"
local STATE_PATH = STATE_DIR .. "/rpc-state.json"

-- The document cliamp-rpcd writes its Discord connection to, and the pace at
-- which this plugin reads it. The path is the daemon's own default composed the
-- same way, and the contract test beside the daemon holds the two together: a
-- plugin reading where the daemon does not write would say nothing about a
-- Discord that was working.
--
-- Three beats of staleness, the same tolerance the playback document's window
-- gives: one missed beat is a slow write, and only a beat that has stopped for
-- three is a daemon that stopped. The poll runs at the daemon's own beat, so a
-- transition is seen within a beat of being written and a stopped daemon within
-- the threshold after that; anything faster would only read the same document
-- twice, and the contract test refuses a poll slower than the threshold, since
-- that would leave a disconnection unsaid for an interval after the document
-- had already shown it.
local STATUS_SCHEMA_VERSION = 1
local STATUS_POLL_SECS = 5
local STATUS_STALE_SECS = 15
local STATUS_PATH = STATE_DIR .. "/rpc-status.json"

local p = plugin.register({
  name = "discord-rpc",
  version = VERSION,
  description = "Publish playback events for the cliamp-rpcd Discord bridge",
  type = "hook",
})

local TRANSPORT = p:config("transport") or DEFAULT_TRANSPORT

local function toint(value, fallback)
  local number = tonumber(value)
  if number == nil or number ~= number or
      number == math.huge or number == -math.huge then
    return fallback
  end
  return math.floor(number)
end

local function value(event, key, fallback)
  if event ~= nil and event[key] ~= nil then
    return event[key]
  end
  return fallback()
end

-- What the daemon is meant to believe about the player right now. An event
-- carries only the fields it changed, so what it leaves out is read from the
-- player rather than carried over: the snapshot stands on its own, whichever
-- transport takes it.
--
-- The daemon accepts only playing, paused and stopped, and every snapshot either
-- replaces the last retained one or overwrites the last good document, so an
-- unusable snapshot is destructive on its way to being discarded. Cliamp answers
-- nil when it has nothing to report, which is not the same as "stopped", so
-- withhold it: returning nil is how this function says so, and each transport
-- decides what withholding means for it. The status enum is deliberately not
-- duplicated here — a second copy would be one to drift, and the contract test
-- cannot catch a drift it shares.
local function snapshot(event, forced_status)
  event = event or {}
  local status = forced_status or value(event, "status", cliamp.player.state)
  if status == nil or status == "" then
    return nil
  end
  return {
    status = status,
    title = value(event, "title", cliamp.track.title) or "",
    artist = value(event, "artist", cliamp.track.artist) or "",
    album = value(event, "album", cliamp.track.album) or "",
    path = value(event, "path", cliamp.track.path) or "",
    year = toint(value(event, "year", cliamp.track.year), 0),
    duration = toint(value(event, "duration", cliamp.player.duration), 0),
    position = toint(value(event, "position", cliamp.player.position), 0),
    stream = value(event, "stream", cliamp.track.is_stream) and true or false,
    plugin_version = VERSION,
  }
end

-- IPC: one retained publish per snapshot. The daemon subscribes and each
-- snapshot replaces the last, so nothing has to be remembered here.
local function publish(event, forced_status)
  local payload = snapshot(event, forced_status)
  if payload == nil then
    cliamp.log.error("discord-rpc: no player state available, skipping publish")
    return
  end
  local ok, err = p:publish("playback", payload, { retain = true })
  if not ok then
    cliamp.log.error("discord-rpc: publish failed: " .. tostring(err))
  end
end

-- File: the daemon watches a document, and what it reads between writes has to
-- stand on its own, so the last snapshot is kept and rewritten. Only updated_at
-- moves with the playback and heartbeat moves with the clock, which is what
-- keeps the daemon's progress bar honest: it interpolates the playhead from
-- updated_at, and a beat that moved it would re-anchor the bar every 15 seconds
-- on a position that had not moved.
local emit = publish
local start = function()
  emit()
end

if TRANSPORT == "file" then
  local document = { v = SCHEMA_VERSION }

  -- Rewrites the document so the daemon can see the plugin is still alive. A
  -- beat is liveness for the last good snapshot, so there is nothing to keep
  -- alive until one has been written, and a document with no status is one the
  -- daemon refuses anyway.
  local function flush()
    if document.status == nil then
      return
    end
    document.heartbeat = os.time()
    local ok, err = pcall(function()
      cliamp.fs.write(STATE_PATH, cliamp.json.encode(document))
    end)
    if not ok then
      cliamp.log.error("discord-rpc: state write failed: " .. tostring(err))
    end
  end

  local function change(event, forced_status)
    local payload = snapshot(event, forced_status)
    if payload == nil then
      cliamp.log.error("discord-rpc: no player state available, skipping write")
      return
    end
    for key, item in pairs(payload) do
      document[key] = item
    end
    document.updated_at = os.time()
    flush()
  end

  emit = change
  -- The directory is where Cliamp keeps its own state, so it can be missing on a
  -- first run. The first write completes the document, which is why the beat
  -- timer is armed after it rather than before. That beat carries no change: it
  -- rewrites the document so the daemon can tell a Cliamp running quietly from
  -- one that stopped mid-track.
  start = function()
    cliamp.fs.mkdir(STATE_DIR)
    change()
    cliamp.timer.every(HEARTBEAT_SECS, flush)
  end
end

-- What cliamp-rpcd last knew about its Discord connection, read from the
-- document it writes. The daemon runs beside Cliamp rather than inside it, and
-- nothing carries a fact back across that boundary: Cliamp's pub/sub lets this
-- plugin publish, and offers a subscriber only to a native client. A file both
-- halves can name is therefore the channel, which is the one the file transport
-- already uses in the other direction.
--
-- It answers what the document says about the connection: true, false, or nil
-- for a document that does not speak to it. Nil is not a third kind of
-- connection, it is this plugin having nothing to report, which is why the
-- three cases are kept apart rather than collapsed into a boolean. A daemon
-- that has not yet reached Discord writes a beat with no connection at all, so
-- reading that as "disconnected" would put a Discord outage on screen every
-- time the daemon started with nothing playing.
--
-- Anything unreadable is nil for the same reason: a document that is missing,
-- refused by the decoder, or of a shape this plugin does not know says nothing
-- about Discord. A stale beat is the one case that does speak, and what it says
-- is that the daemon stopped writing — nothing is showing on Discord either
-- way, so believing the connection it still names would be the lie.
local function statusOf()
  -- The whole read is guarded because it crosses into Cliamp's API, where a
  -- build too old to have these functions answers by raising rather than by
  -- returning.
  local ok, document = pcall(function()
    local raw = cliamp.fs.read(STATUS_PATH)
    if raw == nil then
      return nil
    end
    return cliamp.json.decode(raw)
  end)
  if not ok or type(document) ~= "table" then
    return nil
  end
  if document.v ~= STATUS_SCHEMA_VERSION then
    return nil
  end
  local beat = tonumber(document.beat)
  if beat == nil then
    return nil
  end
  if os.time() - beat > STATUS_STALE_SECS then
    return false
  end
  -- Only a real boolean speaks: a document carrying anything else has a shape
  -- this plugin does not understand, and inventing a disconnection from it
  -- would be the one wrong answer that shows on screen.
  if document.connected ~= true and document.connected ~= false then
    return nil
  end
  return document.connected
end

-- The connection this plugin last said out loud, kept in cliamp.store rather
-- than in a local because a Cliamp restart would otherwise forget it: the state
-- most sessions begin in is a working connection, so announcing it again on
-- every start is the message a user would learn to ignore.
local function spoken()
  if cliamp.store == nil then
    return nil
  end
  return cliamp.store.get("connected")
end

-- Says the connection, and remembers having said it. A Cliamp without
-- cliamp.message cannot show anything, and losing presence over a status line
-- would be a far worse trade than losing the status line.
local function say(connected)
  if cliamp.store ~= nil then
    cliamp.store.set("connected", connected)
  end
  if cliamp.message ~= nil then
    cliamp.message(connected and "Discord connected" or "Discord disconnected")
  end
end

local function check()
  local connected = statusOf()
  if connected == nil or connected == spoken() then
    return
  end
  say(connected)
end

-- Arm the poll once. The daemon connects only when it has something to publish,
-- so this is what lets a quiet session still learn that presence works, and
-- what notices the daemon stopping while Cliamp stays open.
--
-- The immediate check is what carries the feature on a Cliamp with no timer at
-- all, where the poll below cannot be armed: that Cliamp learns the connection
-- once at startup and never notices it changing, which is a poorer indicator
-- but not a broken plugin. Everything this reads is guarded the same way, since
-- a plugin that raised here would take the playback publications down with it.
local watching = false
local function watchStatus()
  if watching then
    return
  end
  watching = true
  check()
  if cliamp.timer ~= nil then
    cliamp.timer.every(STATUS_POLL_SECS, check)
  end
end

p:on("app.start", function()
  start()
  watchStatus()
end)

p:on("track.change", function(event)
  emit(event)
end)

p:on("playback.state", function(event)
  emit(event)
end)

p:on("player.seek", function(event)
  emit(event)
end)

p:on("app.quit", function()
  emit(nil, "stopped")
end)
