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

## The shape of a file

**Every file opens with a purpose comment: what this file is for.** It sits
directly below the `package` clause:

```go
package config

// This file is the configuration surface: it names the settings the daemon runs
// on, reads them from flags, environment variables, and Cliamp's config.toml,
// and records which of those a transport value came from.
```

The comment goes below the clause rather than above it, and the placement is
load-bearing. A comment above the clause is a *package doc*, and `go/doc`
concatenates the package docs of every file in a package, so per-file headers
written there would turn the package documentation into a jumble of file
descriptions. Below the clause the comment is invisible to `go doc` and is still
the first thing a reader meets.

**Nothing is commented inside a function body.** No explanation between two
statements, no note above a branch, no trailing comment at the end of a line.
The rule is absolute, which is what makes it checkable, and it is the half of
this convention a reader notices first.

## Where the reasoning goes

Those two rules relocate reasoning; they do not delete it. Nothing is meant to
be lost, and a change that drops a reason has broken this convention rather than
applied it. A comment lifted out of a body has two places to go.

**Into the doc comment of the declaration it explains**, when the reasoning
belongs to one function. This is where most of it goes, and where it reads best:
the doc comment is what `go doc` prints and what an editor shows on hover, so a
reader asking what `Subscribe` does gets the two things that make it subtle
along with the signature.

```go
// Subscribe connects to Cliamp and returns retained and live playback states.
// The channel closes when Cliamp exits or the connection fails.
//
// The channel holds one snapshot and drops the oldest to make room, because
// presence needs the newest complete snapshot rather than every intermediate
// transition from a burst of Cliamp events.
func Subscribe(ctx context.Context, socketPath string) (<-chan playback.State, error) {
```

**Into the file's purpose comment**, when the reasoning is an invariant the whole
file depends on — an ordering two functions must not break, a state machine they
share, the reason a setting is absent. A file-wide invariant is not about any
single line, and the header is the only place left that is not inside a body.

The tradeoff is worth naming: an invariant in the header sits further from the
code it governs than it used to, so someone changing one function may not look
up. What pays for it is having every invariant in one place, in the order the
code reads, rather than scattered at whichever points happened to need them —
and the guard, which will not let a new one appear where it does not belong.

## The rest of the voice

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
line's start, so an indented comment is held to the same width as a top-level
one.

**A single-line comment is a sentence.** It opens in sentence case and ends in a
period. A comment whose last word is a URL or a code fragment is the standing
exception, since a period appended to either is wrong.

**A trailing comment annotates the value on its line, and only outside a body.**
A struct field or a spec in a `const` or `var` block may carry a short note
beside it. Since a function body takes no comment at all, a value inside one
that seems to need annotating is better served by a name that says the thing, or
by a sentence in the doc comment of the function that owns it.

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

The scenario rule is what carries a test's body comments, which the guard
removes like any others. A step-by-step test loses the sentence above each step
and gains one comment on the function describing the situation all those steps
set up; where a step needs more than that, the step is usually a test of its own
or a helper whose name can say it.

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

- **Whether reasoning was actually relocated.** The guard sees that a body is
  bare and that a header exists; it cannot see whether the header says anything.
  A body emptied by deleting its comments passes, and is the failure mode this
  document exists to name.
- **The period and capitalisation rules.** A comment ending in a URL or code
  fragment legitimately has no period, and telling those apart from an
  unfinished sentence means reading the comment.
- **Comment density.** Density varies legitimately and should. A file that owns
  a subtlety — the liveness rules for the state document, the timeline anchor —
  carries more explanation than one that translates a payload, and flattening
  that difference would cost exactly the reasoning worth keeping.

## Scope

The guard covers the Go files under `daemon/`. Everything else follows the same
convention by review, and nothing enforces it: there is no Lua parser in the
toolchain, and `luacheck` and `shellcheck` lint code rather than prose.

- **Lua** — the shipped plugin and the fixtures under `testdata/` — carries its
  purpose comment at the top of the file and takes no comment inside a function
  body, exactly as Go does.
- **Shell** puts the purpose comment on the line below the shebang and takes no
  comment inside a function body. A `# shellcheck` directive is not a comment in
  this sense — it is an instruction to the linter — so it stays on the line it
  governs.
- **Workflows and the systemd unit** open with a purpose comment too. Their
  step-level comments stay where they are, because a comment above a step
  documents that step the way a doc comment documents a declaration, and there is
  no function body for one to sit inside.

Run the guard on its own with:

```sh
go test ./daemon/internal/style/
```

A failure names the file and the line, so the fix is always local. The guard
runs in CI as part of the ordinary test step, so a breach cannot land unnoticed.
