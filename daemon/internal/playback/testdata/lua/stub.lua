-- Minimal stand-in for Cliamp's plugin API, so discord-rpc.lua can be loaded
-- and exercised without Cliamp present.
--
-- Every accessor is a function, not a field: the real API exposes
-- cliamp.track.title() as a call, and the plugin relies on that by passing the
-- accessor itself as a fallback for value() to invoke.

local M = {}

M.published = {}
M.handlers = {}
M.logs = {}

-- Values the accessors report. Scenarios mutate these before firing an event.
M.player_values = {}
M.track_values = {}

local function accessor(store, name)
  return function()
    return store[name]
  end
end

local function accessors(store, names)
  local built = {}
  for _, name in ipairs(names) do
    built[name] = accessor(store, name)
  end
  return built
end

M.player = accessors(M.player_values, { "state", "duration", "position" })
M.track = accessors(M.track_values, { "title", "artist", "album", "path", "year", "is_stream" })

-- cliamp.log is consulted by the plugin when a publish fails, and by the
-- status guard. Records rather than prints, so a test can assert on it.
M.log = {
  error = function(message) table.insert(M.logs, message) end,
  warn = function(message) table.insert(M.logs, message) end,
}

local handle = {}

function handle:publish(topic, payload, options)
  table.insert(M.published, {
    topic = topic,
    payload = payload,
    options = options,
  })
  return true, nil
end

function handle:on(event, callback)
  M.handlers[event] = callback
end

-- The plugin reads its transport at load. The stub is unconfigured, so it reads
-- what an unconfigured Cliamp gives it: the ipc transport, which is the path
-- this contract test asserts on.
function handle:config() end

function handle:register() end

M.registered = nil

local plugin_api = {
  register = function(spec)
    M.registered = spec
    return handle
  end,
}

-- install puts the stubs in place as globals the plugin expects.
function M.install()
  _G.plugin = plugin_api
  _G.cliamp = {
    player = M.player,
    track = M.track,
    log = M.log,
  }
end

return M
