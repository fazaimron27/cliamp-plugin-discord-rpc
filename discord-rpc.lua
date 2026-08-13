-- discord-rpc: publishes Cliamp now-playing state for cliamp-rpcd.
--
-- This plugin holds no permissions. A separate daemon owns Discord IPC.

local p = plugin.register({
  name = "discord-rpc",
  version = "1.1.0",
  description = "Publish now-playing state for the cliamp-rpcd Discord bridge",
  type = "hook",
})

local SCHEMA_VERSION = 1
local HEARTBEAT_SECS = 15
local STATE_DIR = (os.getenv("HOME") or "") .. "/.local/share/cliamp"
local STATE_PATH = STATE_DIR .. "/rpc-state.json"

local boot_ticks = math.floor((os.clock() % 1) * 1000000)
local boot_mark = tostring({}):gsub("^%a+:%s*", "")
math.randomseed(os.time() + boot_ticks + (tonumber(boot_mark:sub(-6), 16) or 0))
local session = tostring(os.time()) .. "-" .. boot_mark .. "-" ..
  tostring(boot_ticks) .. "-" .. tostring(math.random(100000, 999999))
local seq = 0

local function toint(v, fallback)
  local n = tonumber(v)
  if n == nil or n ~= n or n == math.huge or n == -math.huge then
    return fallback
  end
  return math.floor(n)
end

local st = {
  status = "stopped",
  title = "", artist = "", album = "",
  year = 0, duration = 0, position = 0,
  path = "", stream = false,
  updated_at = os.time(),
}

local function flush()
  seq = seq + 1
  local payload = {
    v = SCHEMA_VERSION,
    session = session,
    seq = seq,
    status = st.status,
    title = st.title,
    artist = st.artist,
    album = st.album,
    year = st.year,
    duration = st.duration,
    position = st.position,
    path = st.path,
    stream = st.stream,
    heartbeat = os.time(),
    updated_at = st.updated_at,
  }
  local ok, err = pcall(function()
    cliamp.fs.write(STATE_PATH, cliamp.json.encode(payload))
  end)
  if not ok then
    cliamp.log.error("discord-rpc: state write failed: " .. tostring(err))
  end
end

local function touch()
  st.updated_at = os.time()
end

local function adopt(ev)
  st.title = ev.title or ""
  st.artist = ev.artist or ""
  st.album = ev.album or ""
  st.path = ev.path or ""
  st.duration = toint(ev.duration, 0)
  st.stream = ev.stream and true or false
end

local function merge(ev)
  if ev.title ~= nil then st.title = ev.title end
  if ev.artist ~= nil then st.artist = ev.artist end
  if ev.album ~= nil then st.album = ev.album end
  if ev.path ~= nil and ev.path ~= "" then st.path = ev.path end
  if ev.stream ~= nil then st.stream = ev.stream and true or false end
  local duration = toint(ev.duration, 0)
  if duration > 0 then st.duration = duration end
end

p:on("app.start", function()
  cliamp.fs.mkdir(STATE_DIR)
  cliamp.timer.every(HEARTBEAT_SECS, flush)
  flush()
end)

p:on("track.change", function(ev)
  adopt(ev)
  st.year = toint(ev.year, 0)
  st.position = 0
  touch()
  flush()
end)

p:on("playback.state", function(ev)
  local path = ev.path
  if path ~= nil and path ~= "" and path ~= st.path then
    adopt(ev)
    st.year = 0
  else
    merge(ev)
  end
  st.status = ev.status or st.status
  if ev.position ~= nil then st.position = toint(ev.position, st.position) end
  if st.status == "stopped" then st.position = 0 end
  touch()
  flush()
end)

p:on("player.seek", function(ev)
  if ev.position ~= nil then st.position = toint(ev.position, st.position) end
  local duration = toint(ev.duration, 0)
  if duration > 0 then st.duration = duration end
  touch()
  flush()
end)

p:on("app.quit", function()
  st.status = "stopped"
  st.position = 0
  touch()
  flush()
end)
