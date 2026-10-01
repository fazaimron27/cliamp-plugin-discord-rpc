-- Runs discord-rpc.lua against a stub Cliamp and prints what the plugin did, so
-- the Go test beside it can read the documents and snapshots that were really
-- produced. It is a test double for the Cliamp API, not part of the plugin.
--
--     lua plugin_driver.lua <plugin path> <transport> [scenario]
--
-- <transport> is what config.toml would hold: a string, or "nil" for a plugin
-- that was never configured.
--
-- <scenario> adds events the default fixture does not have, so the tests that
-- need them do not share the expectations of the tests that do not. The default
-- fixture stands on its own and every existing test reads it: a start, a track
-- change carrying one field, one heartbeat, and a quit.
--
--     seek           two player.seek fires, both re-sending a track that has
--                    not changed, which is what a dragged progress bar produces
--     no-artist      a track change with the artist cleared, as a local file or
--                    a stream reports it
--     stopped-start  Cliamp starts with nothing playing, so the snapshot at
--                    app.start is a cleared card rather than a track and the
--                    track change that follows is the first thing worth naming
--
-- Each line of output is "<kind>\t<body>", in the order the plugin produced it:
--
--     write\t<document>              one cliamp.fs.write
--     publish\t<retain>\t<payload>   one p:publish
--     message\t<text>\t<seconds>     one cliamp.message
--     error\t<message>               one cliamp.log.error

local plugin_path, transport, scenario = arg[1], arg[2], arg[3]
if transport == "nil" then
  transport = nil
end

-- The plugin reads the clock in whole seconds, as os.time gives it, and only a
-- controlled clock can tell a heartbeat from a change.
local clock = 1000
os.time = function()
  return clock
end

local function emit(...)
  io.write(table.concat({ ... }, "\t"), "\n")
end

-- Enough of cliamp.json.encode for a flat payload. The real one belongs to
-- Cliamp; the Go side decodes whatever this produces, so it is the judge of
-- whether the plugin built something readable.
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

local beats = {}
local handlers = {}

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
  message = function(text, seconds) emit("message", text, tostring(seconds)) end,
  json = { encode = encode },
  fs = {
    write = function(_, content) emit("write", content) end,
    mkdir = function() end,
  },
  timer = {
    every = function(_, callback)
      beats[#beats + 1] = callback
    end,
  },
}

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
function p:publish(_, payload, options)
  emit("publish", tostring(options ~= nil and options.retain == true), encode(payload))
  return true
end

plugin = { register = function() return p end }

local function fire(event, payload)
  for _, callback in ipairs(handlers[event] or {}) do
    callback(payload)
  end
end

dofile(plugin_path)

-- The status of the very first snapshot, which the stopped-start scenario needs
-- to be a cleared card rather than a track.
if scenario == "stopped-start" then
  playing.status = "stopped"
end

fire("app.start")

-- A track change that carries one field: the rest has to come from the player.
clock = 1010
if scenario == "stopped-start" then
  playing.status = "playing"
end
playing.title = "Second Track"
playing.position = 61
fire("track.change", { title = "Second Track" })

-- The scenario events, if any. They sit after the track change and before the
-- heartbeat so the default fixture's own order is unchanged for every test
-- that does not name one.
if scenario == "seek" then
  clock = 1015
  fire("player.seek", {})
  clock = 1016
  fire("player.seek", {})
elseif scenario == "no-artist" then
  clock = 1015
  playing.artist = ""
  fire("track.change", {})
end

-- A heartbeat, with nothing about the playback having moved. Only the file
-- transport registers one, so over IPC there is nothing to fire here and the
-- Go side expects one snapshot fewer.
clock = 1025
for _, beat in ipairs(beats) do
  beat()
end

fire("app.quit")
