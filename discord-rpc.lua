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
local VERSION = "1.8.0"

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
local function snapshot(event, forced_status)
  event = event or {}
  -- The daemon accepts only playing, paused and stopped, and every snapshot
  -- either replaces the last retained one or overwrites the last good document,
  -- so an unusable snapshot is destructive on its way to being discarded.
  -- Cliamp answers nil when it has nothing to report, which is not the same as
  -- "stopped", so withhold it: returning nil is how this function says so, and
  -- each transport decides what withholding means for it. The status enum is
  -- deliberately not duplicated here — a second copy would be one to drift, and
  -- the contract test cannot catch a drift it shares.
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

  local function flush()
    -- A beat is liveness for the last good snapshot, so there is nothing to
    -- keep alive until one has been written, and a document with no status is
    -- one the daemon refuses anyway.
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
  start = function()
    -- The directory is where Cliamp keeps its own state, so it can be missing
    -- on a first run. The first write completes the document, which is why the
    -- beat timer is armed after it rather than before.
    cliamp.fs.mkdir(STATE_DIR)
    change()
    -- A beat is liveness, not a change: it rewrites the document so the daemon
    -- can tell a Cliamp running quietly from one that stopped mid-track.
    cliamp.timer.every(HEARTBEAT_SECS, flush)
  end
end

p:on("app.start", function()
  start()
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
