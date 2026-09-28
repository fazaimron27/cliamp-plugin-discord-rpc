# Comment convention

This repository writes comments in one voice, and this document is that voice
written down. It exists because nothing else could: `gofmt` guarantees layout
and never rewrites a comment, while every other step in CI — `shellcheck`,
`luacheck`, `go vet`, `go test` — reads code. With no written rule the last
author decides, and the result is a file that reads one way at the top and
another further down.

The convention is enforced by `daemon/internal/style`, a guard test that parses
every Go file under `daemon/` and fails naming the file and line that broke a
rule. Two parts are deliberately left to review, because no syntax tree can
judge them; they are stated below so a reviewer has something to check against.

## The rules

**Every package opens with a doc comment.** A library package starts with
`Package <name>`; a `main` package starts with `Command <binary>`, because it is
named for the binary it builds and "Package main" tells a reader nothing they
cannot already see.

**Every exported declaration carries a doc comment** — functions, methods,
types, constants, and variables — and it opens with the name of the thing it
documents:

```go
// Connected reports whether a connection is currently open.
func (c *Client) Connected() bool { … }
```

The rule follows the identifier rather than the receiver, so an exported method
on an unexported type is documented like any other. Methods declared inside an
interface are not: the interface's own comment introduces them, and Discord's
payload schema is the case this repository hits.

**A grouped `const` or `var` block is documented once for the block, or once per
spec.** Both forms are in the tree and both are accepted, which is the point:
`config.go` documents a transport block with one comment above it, and
`statewatch.go` comments each spec inside its block and leaves the block itself
bare. A block comment speaks for every name under it, so it is not read as any
one name's opening words.

**Comment prose fits 80 columns**, counted from the marker rather than from the
line's start, so an indented comment inside a function is held to the same width
as a top-level one.

**A single-line comment is a sentence.** It opens in sentence case and ends in a
period. A comment whose last word is a URL or a code fragment is the standing
exception, since a period appended to either is wrong.

**A trailing comment annotates the value on its line.** A map entry, struct
field, or literal gets a short note beside it (`transport = "ipc"   # ipc
(default) or file`). A trailing comment is never the place to explain what the
code does: that belongs in the doc comment or in the line above, where it has
room to say why.

**Paragraphs in a doc block are separated by a bare `//`.** The blank marker
keeps two thoughts apart without a blank line inside the block, which `gofmt`
would reformat.

**A test's comment describes the scenario, and is optional.** Where one is
written it is a prose sentence — what situation the test sets up and what it
therefore proves. The identifier rule does not apply here: a test named
`TestReleasePinsAgreeWithVersionConstant` is not helped by a comment that
repeats its name, and a comment that says why the case matters is worth more
than one that restates it. Tests are also outside the doc-coverage rule, since a
test file's exports are its own business.

## Exemptions

Two exemptions come from the tree rather than from taste, and both are visible
in it:

- **A grouped `const`/`var` block may document at either level**, as above.
  Requiring both would flag a dozen legitimate lines.
- **A comment on a struct field is not a declaration comment.** Fields are
  documented in the shape that suits the struct, and the guard does not read
  them.

## What the guard does not check

These are stated so a reviewer can hold the line the guard cannot:

- **Whether a trailing comment annotates a value or explains logic.** The
  distinction is semantic, and a rule that guessed at it would flag correct
  code.
- **The period and capitalisation rules.** A comment ending in a URL or code
  fragment legitimately has no period, and telling those apart from an
  unfinished sentence means reading the comment.
- **Comment density.** Density varies legitimately and should. A file that owns
  a subtlety — the liveness rules for the state document, the timeline anchor —
  carries more explanation than one that translates a payload, and flattening
  that difference would cost exactly the reasoning worth keeping.

## Scope

The guard covers Go. The Lua plugin and the shell scripts follow the same voice
— the same width, the same sentence shape, the same preference for saying why —
but nothing enforces it: there is no Lua parser in the toolchain, and `luacheck`
and `shellcheck` lint the code rather than the prose. A change to either is held
to the convention in review.

Run the guard on its own with:

```sh
go test ./daemon/internal/style/
```

A failure names the file and the line, so the fix is always local. The guard
runs in CI as part of the ordinary test step, so a breach cannot land unnoticed.
