---
description: Cut a rec-deploy release — verify the gate, tag, push, and confirm the published artifacts
argument-hint: "[version, e.g. v0.13.1 — omit to be proposed one]"
allowed-tools: Bash(git:*), Bash(gh:*), Bash(go:*), Bash(make:*), Bash(gofmt:*), Bash(golangci-lint:*), Read, Edit
---

## Context

- Branch: !`git branch --show-current`
- Working tree: !`git status --short`
- Last tag: !`git tag --sort=-v:refname | head -1`
- Unreleased commits: !`git log --oneline $(git describe --tags --abbrev=0)..HEAD`
- Files changed since the last tag: !`git diff --name-only $(git describe --tags --abbrev=0)..HEAD`
- Unpushed: !`git log --oneline @{u}..HEAD 2>/dev/null || echo "(no upstream)"`

## Your task

Cut a release of rec-deploy. `$ARGUMENTS` is the version, if the operator named one.

**Stop and report instead of proceeding** if the working tree is dirty or the branch
is not `main`. A release tags a commit; uncommitted work would be published as a
version that contains none of it. Point at `/commit` and stop — do not commit on
the operator's behalf.

If there are no unreleased commits, say so and stop. Re-tagging the same tree
produces a release identical to the last one.

### 1. Run the gate

All four must be clean before anything is pushed. Report the actual output of any
that fails and stop — never tag over a failing gate.

```sh
gofmt -l ./cmd ./internal    # must print nothing
go vet ./...
golangci-lint run ./...
go test ./...
make build                   # proves the ldflags build the operator gets
```

### 2. Choose the version

Tags are `vMAJOR.MINOR.PATCH`, annotated (`git tag -a`), and always carry the
leading `v` — `internal/buildinfo.Resolved()` depends on it.

If `$ARGUMENTS` names one, use it. Otherwise read the unreleased commits and
propose: `fix:`/`refactor:` only → patch; any `feat:` → minor. State the choice
and what drove it; do not ask unless the commits are genuinely ambiguous.

### 3. Check what leaves the binary

Refactoring inside the binary is free. These three ship, and a server can be
running any mix of them — check each against the changed-files list above and
say plainly what you found:

- **`install.sh`** — served from `main`, installs the newest *published* release.
  It goes live the moment it is pushed, against a tarball built from the previous
  tag. If it changed, main and the tag must go out together: push, then tag
  immediately, without pausing between them.
- **`internal/units/files/`** — self-update replaces only the binary, so a new
  binary can run under units from any earlier install. If a unit changed, note in
  the release summary that operators must re-run the installer; `rec-deploy status`
  reports the drift.
- **`internal/store/migrations/`** — additive only. `TestMigrationsAreAdditive`
  enforces it and the gate already ran it; just confirm it passed rather than
  re-reasoning about the schema.

### 4. Tag and push

Push the branch first: a tag must point at a commit that exists on the remote.

```sh
git push origin main
git tag -a <version> -m "<version>"
git push origin <version>
```

### 5. Confirm it published

The `Release` workflow (`.github/workflows/release.yml`) fires on `v*` and runs
GoReleaser. Wait for it rather than assuming — a green tag with no artifacts is
the failure this step exists to catch.

```sh
RUN=$(gh run list --workflow=release.yml --limit 1 --json databaseId -q '.[0].databaseId')
gh run watch $RUN --exit-status --interval 20
gh release view <version> --json assets -q '.assets[].name'
```

A complete release has **8 assets**: `checksums.txt`, tarballs for linux
amd64/arm64 and darwin arm64, and `.deb` + `.rpm` for linux amd64/arm64. Anything
less means GoReleaser partially failed — report it as a failure, not a release.

### 6. Report

State the version, the workflow conclusion, the asset count, and any consequence
from step 3 the operator has to act on (re-run the installer, `install.sh` now
live). If the workflow failed, say so with its output — do not describe a release
that did not publish.
