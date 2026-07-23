# rec-deploy — Project Coding Instructions

A Go CLI that deploys GitHub repositories in place on a Linux server and administers
their GitHub side (deploy keys, webhooks). One static binary, `rec-deploy`, installed on
each target server. It is both the operator CLI and the webhook daemon (`rec-deploy serve`).

**The design is authoritative and already agreed:**
`docs/superpowers/specs/2026-07-14-rec-deploy-cli-design.md`. Read it before writing
code. If an implementation choice contradicts the spec, the spec wins — or you stop and
ask.

`docs/` is gitignored, so the specs and plans live in the working tree only and never
reach a clone. If the file is not there, say so and ask for it rather than guessing at
what it said.

In chat, always answer in **Romanian**. Every `CLAUDE.md`, `README.md` and all code /
doc comments **must be written in English** — this file included.

---

## The Build (authoritative stack)

The project compiles to a **single static binary** with **no runtime dependency**. That
property is the north star — every tooling choice below protects it.

| Concern | Choice | Notes |
| --- | --- | --- |
| Language | **Go** (latest stable) | `CGO_ENABLED=0` always — keeps the binary fully static |
| CLI framework | **`spf13/cobra`** | commands, auto help, auto shell completions |
| Config | **`spf13/viper`** | `/etc/rec-deploy/config.yaml` as root, `~/.config/rec-deploy/` otherwise |
| Interactive UI | **Charm**: `charmbracelet/huh` (forms), `bubbletea` + `lipgloss` + `bubbles` (full TUI views) | ported from `rec-cli`'s `internal/ui` |
| Persistence | **`modernc.org/sqlite`** | **pure-Go** SQLite, **no cgo** — keeps the binary static |
| Retry / backoff | **`cenkalti/backoff/v4`** | GitHub API transient failures |
| Build / release | **GoReleaser** | linux amd64+arm64, darwin arm64; `.deb`/`.rpm` via nfpm |
| Self-update | **`minio/selfupdate`** | SHA-256 verified against `checksums.txt`, **fails closed** |
| Testing | **stdlib `testing` only** | no testify, no mocking framework, no table library |

`rec-cli` (checked out beside this repository) is the reference implementation for all of the
above. When in doubt about layout, UI, config, store, self-update or release wiring —
**read how `rec-cli` does it and do the same.**

---

## The Vanilla Code Law (HIGHEST PRIORITY — overrides every other rule)

Write **standard, idiomatic, vanilla Go**. Don't reinvent what the standard library or a
well-maintained module already does. Don't invent "creative" abstractions to feel
original.

### The acceptance test

> *Could a competent senior Go developer, hired tomorrow, read this code and recognize
> what it does from the Go stdlib docs and pkg.go.dev alone — without spending days
> deciphering custom in-house solutions?*

- ✅ **Yes** → write it.
- ❌ **No** → refactor toward the idiomatic pattern, reach for a maintained module, or skip the feature.

### Decision rule — before introducing ANY new helper / abstraction / dependency

1. **Does the Go standard library do this?** (`os/exec`, `os/user`, `syscall`, `net/http`,
   `crypto/hmac`, `context`, `sync`, `encoding/json` …) → use the stdlib.
2. **Does a well-maintained module do this?** (Cobra, Viper, Charm, backoff,
   `modernc.org/sqlite`, `golang.org/x/crypto/ssh`) → use the module.
3. **Is it already a documented convention here or in `rec-cli`?** → follow it.
4. **None of the above?** → **STOP.** Don't invent. Tell the owner, explain what you'd
   build and why; let them decide scope.

If this Law conflicts with anything else in this file, **this Law wins**.

---

## Architecture — thin command, logic in packages

- A **Cobra command is thin**: parse/validate flags → call business logic → render
  output. No domain logic, no shelling out, no DB work inside `cmd/`.
- **Business logic lives in `internal/<domain>/`**, grouped by domain, **never** by type
  (no `internal/models`, no `internal/helpers`).
- A package exposes a small typed API (e.g. `deploy.Run(ctx, opts) (Result, error)`);
  the command wires flags into `opts` and prints the result.

```
main.go              ~17 lines: cli.Context() → cmd.Execute(ctx) → cli.Exit(err)
cmd/                 one file per command group; newXxxCmd() *cobra.Command
internal/
  ui/                port of rec-cli's ui (theme, render, spinner, picker, form, nav)
  cli/               Context(), SetupLogger(), Exit()
  config/            viper; FHS paths when root, XDG when not
  store/             SQLite + embedded numbered migrations
  github/            token resolution, deploy keys, webhooks, HMAC verification
  manifest/          .rec-deploy.yml parsing and validation
  discover/          filesystem walk with prune rules
  deploy/            engine: lock, git sync, pipeline, rollback, results
  privexec/          privilege drop, timeout, output capture
  sshkey/            ed25519 keygen, ephemeral in-process agent, known_hosts pinning
  notify/            telegram, smtp, journald
  server/            HTTP webhook receiver
  buildinfo/         version/commit/date via -ldflags
  selfupdate/        GitHub release download, SHA-256 verified, fails closed
  units/             the systemd units, embedded — so the binary can report drift
```

---

## Security rules (non-negotiable — this binary holds real secrets)

It stores GitHub tokens, webhook HMAC secrets and ed25519 private keys, and it executes
code from remote repositories as arbitrary UNIX users. These are not style preferences.

- **No credential is ever compiled into the binary.** `rec-cli` ships a live bearer token
  in `internal/proxy123/default.go`. Do not copy that pattern. Ever.
- **Never log or print a secret.** Human output redacts to `••••` + last 4 characters;
  `--json` renders `set` / `unset`. A private key is never printed at all.
- **HMAC comparison is `hmac.Equal`**, never `==`. It runs over the **raw** request body,
  before any parsing.
- **Deploy deliveries are deduplicated** on `X-GitHub-Delivery` via a unique index.
  Without it, replaying a captured signed request re-deploys.
- **Privilege drop is `syscall.Credential`**, never `sudo`. Set `Uid`, `Gid` **and**
  `Groups` (from `user.LookupGroupIds()`) — `sudo -u` gets supplementary groups from PAM,
  `SysProcAttr` does not, and dropping them silently breaks HestiaCP group memberships.
  `HOME` and `SHELL` come from the real passwd entry, never from a hardcoded
  `/home/{user}`.
- **The private key never touches a site user's disk.** Keys live root-only in
  `/var/lib/rec-deploy/keys/` (0400); deploys use an ephemeral in-process SSH agent whose
  socket is chowned to the site user and destroyed afterwards.
- **github.com host keys are pinned** from `https://api.github.com/meta`. Never
  `StrictHostKeyChecking=no`.
- **Root-owned deploy targets are allowed but flagged** (`⚠ root` in status and
  notifications, a warning in the log). Push access to such a repo is root on the server;
  that must be visible, not silent.
- **The manifest's `repository` field is verified** against `git remote get-url origin`
  before anything runs.

---

## The defects we are here to fix (do not reintroduce them)

The Laravel prototype fails in these specific ways. Every one of them must be impossible
by construction in this codebase — they are the reason it exists.

| Defect | The rule here |
| --- | --- |
| Manifest `timeout` parsed but never applied (60s default kills `composer install`) | Every command runs under a real `context.WithTimeout`. Default 10m, per-command override. |
| No overlap lock → concurrent `git reset --hard` on one tree | Every path deploys under an advisory `flock`. |
| `X-GitHub-Delivery` stored but never checked | Unique index; a repeat delivery is a no-op `200`. |
| Zero installations found → no notification at all | Zero installations is an **error**, reported and notified. |
| Missing/invalid manifest → empty pipeline → reports **success** | A missing or invalid manifest is a **hard failure**, never a silent success. |
| `StrictHostKeyChecking=no` | Pinned host keys. |
| Private key copied into every site user's `~/.ssh` | Ephemeral agent; the key stays root-only. |
| Push to any branch redeploys whatever branch is checked out | Deploy only when the pushed ref matches the checkout's branch. |
| Full raw GitHub payload persisted forever | Persist `ref`, `sha`, `message`, `author`. Nothing else. |

---

## Interactive by default

`rec-deploy` is **dual-mode**: every command is fully scriptable **and** offers a polished
interactive UI. A module is not "done" until it has both.

- A command run without the input it needs, **in a terminal**, opens an interactive
  environment instead of failing. In a non-TTY (piped, CI, systemd) it falls back to help
  or a status summary, or errors with a clear pointer to the flag. Branch on
  `isInteractive()`.
- **Command groups** give their bare form a `RunE` that opens a hub via `selectMenu(...)`.
  When you add a subcommand, wire it into the group's menu in the **same** change.
- **Navigation, identical everywhere:** `Esc` = back one level (`ui.ErrBack`); bare `←`
  also goes back on navigation screens; `Ctrl+C` anywhere or `q` outside text inputs =
  quit the session (`ui.ErrQuit` + `ui.Quitting()`); `h` = help. Option+arrows remain
  text-editing shortcuts and must never be intercepted for navigation.
  A command's **top** menu returns `ui.ErrBack`; a **nested** menu returns `nil`.
  Every menu loop owes `if ui.Quitting() { return ui.ErrQuit }` at the top of the `for`.
- **Show progress for blocking work — never a dead pause.** Wrap it with
  `ui.Spinner(title, func() error { … })`. *Exception:* work that streams its own output.
  **A deploy streams.** `rec-deploy deploy` shows command output live; it must not spin.
- **Destructive actions** confirm with `ui.Confirm` in a TTY and require `--yes`
  otherwise, erroring with the literal re-run hint.
- **`--json` is a global persistent flag.** Commands branch early:
  `if flagJSON { return ui.PrintJSON(v) }`.

---

## Core Philosophy

- **Think before coding.** State assumptions explicitly. If multiple interpretations
  exist, present them — don't pick silently. If something is unclear, stop, name what's
  confusing, and ask **before** implementing.
- **Simplify.** Minimum code that solves the problem, nothing speculative. No
  overengineered abstractions, no features beyond what was asked, no error handling for
  impossible scenarios. Don't extract a function for a few lines used once.
- **Flat code (early returns).** Guard and return early; avoid deep nesting.
- **Surgical changes.** Touch only what you must. Match existing style. Remove only the
  imports/vars **your** change made unused; mention pre-existing dead code instead of
  silently deleting it.
- **Best correct solution, not the first that compiles.** If you deliberately ship a
  simpler version, **say so and state what the better version would be** — never silently
  downgrade.
- **A change is "done" only when its consequences are done.** *Surgical* means *precise*,
  not *incomplete*: call sites updated, tests adjusted, doc comments and `--help` text
  corrected, error paths handled — all in the same change.

---

## Go Style & Documentation

- **`gofmt` (via `goimports`) is the canonical style — tool-enforced, non-negotiable.**
  PHP-specific Allman-brace and blank-line rules do not apply. Go has one
  machine-defined format.
- **`golangci-lint` is the gate.** Code must pass before it is "done".
- **Doc comments on every exported identifier**, starting with the identifier's name, in
  imperative mood. Comments explain *why*, not *what*.
- **Errors are values.** Wrap with `fmt.Errorf("...: %w", err)`; never panic for control
  flow. Messages are lowercase, no trailing period, and carry an actionable hint with the
  fix in backticks — e.g. ``github token is not configured — run `rec-deploy init` ``.
- **`context.Context` is threaded through every blocking call.** It is how `Ctrl+C`
  cancels a deploy and how per-command timeouts are enforced.

---

## Testing

Stdlib `testing`, tests in the same package, real SQLite in `t.TempDir()`, bubbletea
models tested by calling `Update()` with a synthetic `tea.KeyMsg`. `t.Fatal` for setup
failures, `t.Errorf` for assertions.

The parts that carry real risk and need real coverage:

- **`internal/manifest`** — string vs object entries, timeouts, defaults, and that a
  missing or malformed file is an error rather than an empty pipeline.
- **`internal/discover`** — the git-root stop rule, prune rules, a repo found in several
  locations, zero locations.
- **`internal/github`** — HMAC verification against known-good and tampered bodies;
  delivery deduplication.
- **`internal/deploy`** — the branch filter, the lock, rollback on failure, and that a
  step's timeout actually kills the process.
- **`internal/privexec`** — that the target UID, GID and supplementary groups are applied.
  Needs root, so it skips when unavailable.

---

## Installed software

`rec-deploy` runs on real servers with real repositories. Refactoring *inside* the binary
stays free — rename packages, restructure commands, update every call site in the same
change, no deprecation cycles. What **leaves** the binary is where compatibility starts,
because a server can be running any mix of the three artifacts this ships:

- **The SQLite schema is additive only** — no `DROP`, no `RENAME`, no `ADD COLUMN NOT NULL`
  without a `DEFAULT`. `internal/store`'s `TestMigrationsAreAdditive` enforces it. This is
  what lets self-update's rollback restore *service* and not merely a file: only the
  migrations a binary embeds are applied, so an older binary opens a newer database and
  works. A binary that refused would turn one bad release into an outage on every server
  that took it, unattended.
- **The systemd units are shipped, not assumed.** self-update replaces only the binary, so
  a v1.0.0 binary can be running units from any earlier install. A binary that *needs* a
  new unit directive breaks a box that auto-updates into it. `rec-deploy status` reports
  the drift; the operator re-runs the installer.
- **`install.sh` is served from `main` and installs the newest published release.** A change
  to it goes live the moment it merges, against a tarball built from an older tag. Merge
  and tag together.

---

## Explicitly out of scope

Laravel/web-specific, or deliberately dropped scope. Do not recreate:

- Eloquent, queues, `Bus::chain`, jobs, observers, notifications-as-a-framework.
- Laravel Scout / Algolia / Meilisearch. A `LIKE` over a few hundred rows replaces it.
- Cloudflare automation, HestiaCP template generation, the scratch `Tests` command group.
- Capistrano-style releases/symlink deploys — **impossible** for a WP theme inside
  `wp-content/themes/`, which is a first-class deploy target.
- A control plane, agentless orchestration, or SSH between servers. Each server is
  independent and registers its own webhook.
- GitLab, Gitea, or any `Forge` abstraction. **GitHub only**, written directly.
- PHP Allman braces & blank-line rules (replaced by `gofmt`).

---

## Checklist before any change

0. **Vanilla Go check** — would a senior Go dev recognize this from stdlib + pkg.go.dev
   alone? If no → don't write it. (Overrides everything below.)
1. **Read the spec** — `docs/superpowers/specs/2026-07-14-rec-deploy-cli-design.md`
   (working tree only — `docs/` is gitignored).
2. **Identify the domain** — deploy / discover / manifest / github / privexec / sshkey / …
3. **Decide the layer** — Cobra command (thin: flags + render) vs `internal/<domain>`.
4. **Reuse before adding** — search the package, then `rec-cli`, before writing a helper.
5. **Thread `context`** through anything blocking.
6. **Both modes** — interactive (TTY menu/prompt) *and* non-interactive (flags/args)?
7. **Security rules** — no secret logged, no secret in the binary, `hmac.Equal`, no `sudo`.
8. **`gofmt` + `golangci-lint` clean**, exported identifiers documented, before "done".
