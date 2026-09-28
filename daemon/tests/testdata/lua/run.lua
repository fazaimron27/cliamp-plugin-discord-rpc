-- Runs discord-rpc.lua against the stubbed Cliamp API and prints what it
-- published, as one JSON document on stdout.
--
--   luajit run.lua <path/to/discord-rpc.lua> <scenario>
--
-- The output shape is {"published":[<payload>,...],"logs":[<string>,...]} so the
-- Go side can unmarshal each payload into playback.State and assert on it.

local script_dir = arg[0]:match("^(.*)/[^/]*$") or "."
package.path = script_dir .. "/?.lua;" .. package.path

local stub = require("stub")

local plugin_path = arg[1]
local scenario_name = arg[2]

if not plugin_path or not scenario_name then
  io.stderr:write("usage: luajit run.lua <plugin.lua> <scenario>\n")
  os.exit(2)
end

-- Each scenario seeds the accessors, then names the handler to fire and the
-- event it receives. The nil cases matter most: they are what a plugin sees
-- when Cliamp has no answer, and they are what the contract test exercises.
local scenarios = {
  -- A complete, ordinary snapshot.
  playing = function()
    stub.player_values.state = "playing"
    stub.player_values.duration = 200
    stub.player_values.position = 42
    stub.track_values.title = "Track"
    stub.track_values.artist = "Artist"
    stub.track_values.album = "Album"
    stub.track_values.path = "/music/track.flac"
    stub.track_values.year = 2020
    stub.track_values.is_stream = false
    return "playback.state", { status = "playing", position = 42 }
  end,

  -- A track change whose event carries the track but no status, at a moment
  -- when the player accessor has no answer.
  ["state-nil"] = function()
    stub.player_values.state = nil
    stub.player_values.duration = 200
    stub.player_values.position = 0
    stub.track_values.title = "Track"
    stub.track_values.artist = "Artist"
    stub.track_values.album = "Album"
    stub.track_values.path = "/music/track.flac"
    stub.track_values.year = 2020
    stub.track_values.is_stream = false
    return "track.change", { title = "Track", artist = "Artist" }
  end,

  -- Every accessor unanswered, as at startup before the player is ready.
  ["all-nil"] = function()
    stub.player_values.state = nil
    stub.player_values.duration = nil
    stub.player_values.position = nil
    stub.track_values.title = nil
    stub.track_values.artist = nil
    stub.track_values.album = nil
    stub.track_values.path = nil
    stub.track_values.year = nil
    stub.track_values.is_stream = nil
    return "app.start", nil
  end,

  -- Shutdown forces a stopped status, which must keep working regardless.
  quit = function()
    stub.player_values.state = "playing"
    stub.player_values.duration = 200
    stub.player_values.position = 42
    stub.track_values.title = "Track"
    stub.track_values.artist = "Artist"
    stub.track_values.album = "Album"
    stub.track_values.path = "/music/track.flac"
    stub.track_values.year = 2020
    stub.track_values.is_stream = false
    return "app.quit", nil
  end,
}

local scenario = scenarios[scenario_name]
if not scenario then
  io.stderr:write("unknown scenario: " .. scenario_name .. "\n")
  os.exit(2)
end

local event_name, event = scenario()

stub.install()
dofile(plugin_path)

local handler = stub.handlers[event_name]
if not handler then
  io.stderr:write("plugin registered no handler for " .. event_name .. "\n")
  os.exit(3)
end
handler(event)

local function encode_string(value)
  local escaped = value:gsub('[%c"\\]', function(character)
    if character == '"' then return '\\"' end
    if character == "\\" then return "\\\\" end
    return string.format("\\u%04x", character:byte())
  end)
  return '"' .. escaped .. '"'
end

local function encode(value)
  local kind = type(value)
  if kind == "nil" then return "null" end
  if kind == "boolean" then return tostring(value) end
  if kind == "number" then
    if value ~= value or value == math.huge or value == -math.huge then return "null" end
    if value == math.floor(value) then return string.format("%d", value) end
    return string.format("%.17g", value)
  end
  if kind == "string" then return encode_string(value) end
  if kind == "table" then
    local parts = {}
    for key, item in pairs(value) do
      table.insert(parts, encode_string(tostring(key)) .. ":" .. encode(item))
    end
    return "{" .. table.concat(parts, ",") .. "}"
  end
  error("cannot encode a " .. kind)
end

local function encode_array(values)
  local parts = {}
  for _, item in ipairs(values) do
    table.insert(parts, encode(item))
  end
  return "[" .. table.concat(parts, ",") .. "]"
end

-- The topic and the retain flag are part of the contract too: the daemon
-- subscribes to one topic by name, and retention is what makes an invalid
-- snapshot destructive rather than merely ignored.
local published = {}
for _, entry in ipairs(stub.published) do
  table.insert(published, table.concat({
    '{"topic":', encode(entry.topic),
    ',"retain":', tostring(entry.options ~= nil and entry.options.retain == true),
    ',"payload":', encode(entry.payload),
    "}",
  }))
end

print('{"published":[' .. table.concat(published, ",") .. '],"logs":' .. encode_array(stub.logs) .. "}")
