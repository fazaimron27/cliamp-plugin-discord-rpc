<!--
Title: type(scope): what changed — fix, feat, docs, chore, ci, test. Lowercase,
imperative, no trailing period. A release PR is chore(release): vX.Y.Z.

Delete any section below that does not apply, and any check inside one. Strike
a check that does not apply rather than leaving it blank: an empty box reads as
work not done, and a ticked box that did not happen is worse. An empty heading
is worse than a missing one.
-->

<!--
Open with the behaviour, in prose. What it was, what it is now, and why it
mattered. This is the part a reviewer reads first and the only part they are
guaranteed to read.
-->

## Root cause

<!--
Required for a fix; delete it for anything else. What actually caused the
failure, and how you know — a trace, a measurement, a reproduced failure. "How
you know" is the half that gets skipped: a cause confirmed against a program
beats one inferred from reading.
-->

## Verification

<!-- Paste the output, not a claim that you ran it. -->

```sh
gofmt -l .
go vet ./...
CLIAMP_REQUIRE_LUA=1 go test -race -count=1 ./...
```

- [ ] Every new test was written first and watched fail for the right reason
- [ ] Any new guard was proved by mutation: broken deliberately, seen to fail
      naming the site, then reverted. A guard only ever observed passing is not
      yet known to guard anything.
- [ ] If this branch merges others, the suite ran on the **merged tree**. A
      clean merge is not a working merge.
- [ ] Anything not exercised locally is named here, with why

## Not in this PR

<!--
What a reviewer would reasonably expect to be here and is not, and why. This is
where scope is drawn; deleting it says you looked and found nothing.
-->

## Notes

- [ ] No version-shaped string added to README.md, docs/building.md or
      docs/comments.md — the release-pin guard scans those by name
- [ ] Added prose is within the 80-column bound in docs/comments.md
- [ ] If this touches a release pin, all five moved together
