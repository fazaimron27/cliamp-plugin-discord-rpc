# Building from source

This is the developer path: it builds Cliamp and the daemon from checkouts
instead of downloading a release archive. For a normal installation, the
[README](../README.md) is shorter and needs no Go toolchain.

## Requirements

- Git and Go 1.26.5 or newer to build Cliamp from its official `main` branch.
- Go 1.25 or newer to build the daemon.

## Build Cliamp from main

The default `ipc` transport needs a Cliamp build from the official `main`
branch: the one carrying the version 2 IPC envelope and the merged plugin
pub/sub API.

```sh
git clone --branch main --single-branch \
  https://github.com/bjarneo/cliamp.git cliamp-main
cd cliamp-main
go test ./ipc ./luaplugin
go build -o cliamp .
install -Dm755 ./cliamp ~/.local/bin/cliamp
cd ..
```

Close any running Cliamp process before replacing its executable, then start it
again after installing and trusting the plugin.

Confirm that your shell resolves the new binary with `command -v cliamp`. If it
still finds an older `cliamp`, that binary is what the daemon will talk to.

A Cliamp build without the pub/sub API can still run this project, over the
`file` transport; the README's
[Choose the playback transport](../README.md#choose-the-playback-transport)
covers that setting.

## Build the daemon

```sh
git clone --branch v1.12.0 --single-branch \
  https://github.com/fazaimron27/cliamp-plugin-discord-rpc.git
cd cliamp-plugin-discord-rpc
go test ./...
go vet ./...
go build -o cliamp-rpcd ./daemon/cmd/cliamp-rpcd
```

## Install the source checkout

```sh
install -Dm644 ./discord-rpc.lua ~/.config/cliamp/plugins/discord-rpc.lua
cliamp plugins trust discord-rpc
install -Dm755 ./cliamp-rpcd ~/.local/bin/cliamp-rpcd
install -Dm644 ./cliamp-rpcd.service ~/.config/systemd/user/cliamp-rpcd.service
systemctl --user daemon-reload
```

Restart Cliamp after installing the Lua plugin. When you edit that file later,
run `cliamp plugins trust discord-rpc` again to approve its new hash, then
restart Cliamp.

To remove a source deployment, run the repository's uninstaller from the same
checkout or specify the matching custom directories:

```sh
./uninstall.sh
```

## Tests

`go test ./...` runs the suite. Two things about it are worth knowing before you
trust a green run:

- The Lua contract tests need `luajit` and **skip without it**, so a local
  `go test ./...` can pass with the plugin half unrun. `CLIAMP_REQUIRE_LUA=1`
  turns that skip into a failure:

  ```sh
  CLIAMP_REQUIRE_LUA=1 go test -race ./...
  ```

- The tests are laid out by one rule, and which package a test is written as is
  the whole of it: every test sits beside the package it tests, and the external
  test package reaches only the exported surface. [Architecture](architecture.md#testing)
  states the rule and what the Lua harness enforces.

CI runs the same things you would: `test -z "$(gofmt -l .)"`,
`shellcheck -s sh install.sh uninstall.sh`, `luacheck discord-rpc.lua`,
`go vet ./...`, `go build ./daemon/cmd/cliamp-rpcd`, and `go test -race ./...`
with `CLIAMP_REQUIRE_LUA=1` set. `luajit` and `luacheck` are not on every
machine, so install them before a local run means as much as CI's.

## Release pins

The release version is written into more than one file — the plugin's `VERSION`,
`install.sh` twice, the README, the release workflow, and this document.
`daemon/internal/version/release_test.go` reads them and fails when one disagrees
with `version.Number`, because a stale pin in `install.sh` ships an archive whose
bundled installer downloads the previous daemon. It scans the two documentation
files by name, so a version pinned in a *new* file needs adding to that list, or
it is a pin nothing checks.

## Comment convention

Comments are written in one voice across the repository, and that voice is
written down in the [comment convention](comments.md): every file opens with a
comment saying what it is for, nothing is commented inside a function body, and
the reasoning those bodies carried lives either in the doc comment of the
declaration it explains or in the header of the file it governs. The document
also lists the exemptions the tree itself proves are needed, and the parts
deliberately left to review because no syntax tree can judge them. A guard test
in `daemon/internal/style` enforces the mechanical ones, so it runs with the
rest of the suite and a breach cannot land unnoticed.

Both documents exist for the same reason as the release pins above: a rule
nothing reads is a rule that drifts, and a comment convention with no guard is
a preference.
