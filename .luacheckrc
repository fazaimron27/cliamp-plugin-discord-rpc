-- luacheck configuration for discord-rpc.lua.
--
-- Cliamp injects `plugin` and `cliamp` as globals before it loads a plugin, so
-- from the plugin's point of view they are defined even though nothing in this
-- repository declares them. Those two are the whole config on purpose: the
-- shipped plugin should produce no warnings at all, so nothing that luacheck
-- reports later is hidden by something suppressed here.
--
-- Which Lua Cliamp embeds is not recorded in this repository, so no `std` is
-- set and luacheck's default applies; the plugin uses no construct that differs
-- across the 5.x line.

globals = { "cliamp", "plugin" }
