-- Runs discord-rpc.lua against a stub Cliamp that controls the status document
-- the daemon writes, and prints what the plugin said about the Discord
-- connection, so the Go test beside it can read the messages a real Cliamp
-- would have shown. It is a test double for the Cliamp API, not part of the
-- plugin.
--
--     lua status_driver.lua <plugin path> <transport> <api>
--
-- <transport> is what config.toml would hold: a string, or "nil" for a plugin
-- that was never configured.
--
-- <api> is "full", or "bare" for a Cliamp too old to have the status API at
-- all: bare omits cliamp.message, cliamp.store and cliamp.fs.read, and the
-- plugin has to go on publishing playback without them.
--
-- Each line of output is "<kind>\t<body>", in the order the plugin produced it:
--
--     message\t<text>                one cliamp.message
--     store\t<key>\t<value>          one cliamp.store.set
--     write\t<document>              one cliamp.fs.write
--     publish\t<payload>             one p:publish
--     error\t<message>               one cliamp.log.error
--     restart\t                      the plugin was loaded again, as Cliamp
--                                    loads it once per start
--
-- The script it drives is the fixture: a document is put where the daemon
-- writes one, the plugin's poll is run, and the next document replaces it. The
-- documents are built by the plugin's own daemon, which is what its status
-- tests prove; what is under test here is what the plugin does with them.

local plugin_path, transport, api = arg[1], arg[2], arg[3]
if transport == "nil" then
  transport = nil
end

-- The plugin reads the clock in whole seconds, as os.time gives it, and only a
-- controlled clock can tell a beat that moved from one that stopped.
local clock = 1000
os.time = function()
  return clock
end

local function emit(...)
  io.write(table.concat({ ... }, "\t"), "\n")
end

-- Enough of cliamp.json.encode for a flat payload. The real one belongs to
-- Cliamp; the Go side reads what this produces, so it is the judge of whether
-- the plugin built something readable.
local escapes = { ['"'] = '\\"', ["\\"] = "\\\\", ["\n"] = "\\n", ["\r"] = "\\r", ["\t"] = "\\t" }

local function quote(text)
  return '"' .. text:gsub('[%c"\\]', function(character)
    return escapes[character] or string.format("\\u%04x", character:byte())
  end) .. '"'
end

local function encode(value)
  local kind = type(value)
  if kind == "string" then
    return quote(value)
  elseif kind == "number" then
    return string.format("%d", value)
  elseif kind == "boolean" then
    return tostring(value)
  elseif kind == "table" then
    local parts = {}
    for key, item in pairs(value) do
      parts[#parts + 1] = quote(tostring(key)) .. ":" .. encode(item)
    end
    return "{" .. table.concat(parts, ",") .. "}"
  end
  return "null"
end

-- What cliamp.json.decode answers for each document the daemon is made to have
-- written. Decoding belongs to Cliamp rather than to this plugin, so the stub
-- answers with the table the plugin would have been handed: what is under test
-- is what the plugin does with that table. A document never put in place, or
-- one put in place as raw text, is the decoder failing, which is nil and a
-- message — the same thing Cliamp's own decoder returns for malformed input.
local decoded = {}
local document = nil

local function put(value)
  if value == nil then
    document = nil
    return
  end
  document = encode(value)
  decoded[document] = value
end

local function putRaw(text)
  document = text
end

-- The player the plugin reads when an event leaves a field out.
local playing = {
  status = "playing",
  title = "Track",
  artist = "Artist",
  album = "Album",
  path = "/music/track.flac",
  year = 1999,
  duration = 240,
  position = 30,
  stream = false,
}

-- What the plugin has said out loud. It outlives the reload below because
-- cliamp.store is what survives a Cliamp restart, and this stub stands in for
-- it: persisting the announced connection is exactly what stops a restart from
-- announcing one that never changed.
local stored = {}

local handlers = {}
local timers = {}

cliamp = {
  player = {
    state = function() return playing.status end,
    duration = function() return playing.duration end,
    position = function() return playing.position end,
  },
  track = {
    title = function() return playing.title end,
    artist = function() return playing.artist end,
    album = function() return playing.album end,
    path = function() return playing.path end,
    year = function() return playing.year end,
    is_stream = function() return playing.stream end,
  },
  log = {
    error = function(message) emit("error", message) end,
  },
  json = {
    encode = encode,
    decode = function(text)
      local value = decoded[text]
      if value == nil then
        return nil, "invalid JSON"
      end
      return value
    end,
  },
  fs = {
    write = function(_, content) emit("write", content) end,
    mkdir = function() end,
  },
  timer = {
    every = function(_, callback)
      timers[#timers + 1] = callback
    end,
  },
}

if api ~= "bare" then
  cliamp.message = function(text) emit("message", text) end
  cliamp.fs.read = function() return document end
  cliamp.store = {
    get = function(key) return stored[key] end,
    set = function(key, value)
      stored[key] = value
      emit("store", key, tostring(value))
    end,
  }
end

local p = {}
function p:on(event, callback)
  handlers[event] = handlers[event] or {}
  table.insert(handlers[event], callback)
end
function p:config(key)
  if key == "transport" then
    return transport
  end
  return nil
end
function p:publish(_, payload)
  emit("publish", encode(payload))
  return true
end

plugin = { register = function() return p end }

-- Runs every timer the plugin armed, which is what an elapsed tick does. The
-- plugin arms only the ones it needs, so the file transport's heartbeat is
-- among them and the others are not.
local function poll()
  for _, callback in ipairs(timers) do
    callback()
  end
end

-- Loads the plugin the way Cliamp does: once per start, into a fresh set of
-- handlers but the same store.
local function boot()
  handlers = {}
  timers = {}
  dofile(plugin_path)
  for _, callback in ipairs(handlers["app.start"] or {}) do
    callback()
  end
end

-- A daemon that has written nothing: never installed, or not started yet.
boot()

-- A daemon that has started but has not reached Discord. It has a beat and no
-- opinion about a connection, and saying "disconnected" here would put a
-- Discord outage on screen every time it started with nothing playing.
clock = 1000
put({ v = 1, beat = 1000 })
poll()

-- The handshake landed.
clock = 1005
put({ v = 1, beat = 1005, connected = true })
poll()

-- The same connection, one beat later: it did not change, so nothing is said.
clock = 1010
put({ v = 1, beat = 1010, connected = true })
poll()

-- Something the decoder refuses, and then a shape this plugin does not know.
-- Neither is a fact about Discord, so neither is a reason to change what the
-- plugin believes.
clock = 1015
putRaw("not json")
poll()

clock = 1020
put({ v = 2, beat = 1020, connected = false })
poll()

-- Still connected, which is what the two unreadable documents left standing: a
-- plugin that had taken either of them as a disconnection would announce this.
clock = 1025
put({ v = 1, beat = 1025, connected = true })
poll()

-- The daemon stopped. The document still says connected, but its beat has not
-- moved for longer than the plugin is willing to believe it.
clock = 1060
put({ v = 1, beat = 1025, connected = true })
poll()

-- The daemon came back and reached Discord again.
clock = 1065
put({ v = 1, beat = 1065, connected = true })
poll()

-- Cliamp restarted with the daemon still connected. A plugin announcing from a
-- blank memory would say "Discord connected" again, which is the message a user
-- would learn to ignore; the stored connection is what stops it.
emit("restart")
clock = 1070
boot()
