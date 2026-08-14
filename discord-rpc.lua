-- discord-rpc: publishes retained Cliamp playback state for cliamp-rpcd.
--
-- Playback state stays in memory and is delivered over Cliamp's local IPC
-- pub/sub stream. A separate daemon owns Discord IPC.

local p = plugin.register({
  name = "discord-rpc",
  version = "1.5.0",
  description = "Publish playback events for the cliamp-rpcd Discord bridge",
  type = "hook",
})

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

local function publish(event, forced_status)
  event = event or {}
  local ok, err = p:publish("playback", {
    status = forced_status or value(event, "status", cliamp.player.state),
    title = value(event, "title", cliamp.track.title) or "",
    artist = value(event, "artist", cliamp.track.artist) or "",
    album = value(event, "album", cliamp.track.album) or "",
    path = value(event, "path", cliamp.track.path) or "",
    year = toint(value(event, "year", cliamp.track.year), 0),
    duration = toint(value(event, "duration", cliamp.player.duration), 0),
    position = toint(value(event, "position", cliamp.player.position), 0),
    stream = value(event, "stream", cliamp.track.is_stream) and true or false,
  }, { retain = true })
  if not ok then
    cliamp.log.error("discord-rpc: publish failed: " .. tostring(err))
  end
end

p:on("app.start", function()
  publish()
end)

p:on("track.change", function(event)
  publish(event)
end)

p:on("playback.state", function(event)
  publish(event)
end)

p:on("player.seek", function(event)
  publish(event)
end)

p:on("app.quit", function()
  publish({}, "stopped")
end)
