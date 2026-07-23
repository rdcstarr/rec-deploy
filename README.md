# rec-deploy

Auto-deploy GitHub repositories in place on a Linux server, and administer their GitHub
side — deploy keys and webhooks — from the same binary.

`rec-deploy` is a single static Go binary (`CGO_ENABLED=0`, no runtime dependency) installed
on each target server. It is both the operator CLI and the webhook daemon. Push to a
repository, and every checkout of it on the box updates itself and runs its own
post-deploy steps, each as the UNIX user that owns the directory.

It works on any layout — HestiaCP sites, `/var/www`, `/srv`, `/opt` — and a deploy target
does not have to be a site: a WordPress theme or plugin living inside `wp-content/` is a
first-class target.

## Why rec-deploy

The manifest lives in the repository and is read after the pull, one push fans out to
every checkout, and each target runs under its UNIX owner. The design also prevents
common deployment failure modes:

| Failure mode | rec-deploy |
| --- | --- |
| Manifest `timeout` parsed and never applied; a 60s default killed `composer install` | Every step runs under a real `context.WithTimeout` (default 10m, per-step override) |
| No lock → two pushes ran concurrent `git reset --hard` on one tree | Every path deploys under an advisory `flock` |
| `X-GitHub-Delivery` stored but never checked | Unique index; a replayed delivery is a no-op `200` |
| Zero installations found → no notification at all | Zero installations is an error, reported and notified |
| Missing/invalid manifest → empty pipeline → reported **success** | A missing or invalid manifest is a hard failure |
| `StrictHostKeyChecking=no` | github.com host keys pinned from `api.github.com/meta` |
| Private key copied into every site user's `~/.ssh` | Key stays root-only; deploys use an ephemeral in-process SSH agent |
| A push to any branch redeployed whatever branch was checked out | A path deploys only when the pushed ref matches its checkout's branch |
| The full raw payload persisted forever | Only `ref`, `sha`, `message`, `author` |

## Install

The quickest way — one command that detects your OS and architecture, verifies the
download against the release checksums, installs the systemd unit, and drops you straight
into the setup wizard:

```sh
curl -fsSL https://get.rec.tools/deploy | sudo bash
```

It touches only `/usr/bin/rec-deploy` and `/etc/systemd/system/rec-deploy.service`. It is plain
shell — read it first if you would rather not pipe an unread script into a root shell.

Prefer a package? Grab one from the
[releases](https://github.com/rdcstarr/rec-deploy/releases). The `.deb` / `.rpm` install
the binary to `/usr/bin/rec-deploy`, drop the systemd unit, and create `/etc/rec-deploy` and
`/var/lib/rec-deploy` (both `0700` — they hold the GitHub token, the HMAC secrets and the
deploy keys).

```sh
# Debian / Ubuntu
dpkg -i rec-deploy_<version>_linux_amd64.deb

# Fedora / RHEL
rpm -i rec-deploy_<version>_linux_amd64.rpm
```

Or the plain tarball (linux `amd64`/`arm64`, darwin `arm64`). It carries the binary and the
systemd units under `packaging/`, but installs nothing: copy the binary where you want it,
and the units to `/etc/systemd/system` if you want the daemon. No directories are created:

```sh
tar -xzf rec-deploy_<version>_linux_amd64.tar.gz
install -m 0755 rec-deploy /usr/bin/rec-deploy
```

Verify a download against `checksums.txt` from the same release. Afterwards `rec-deploy
self-update` upgrades in place: it checks the digest against that same `checksums.txt` and
**fails closed** — a missing or mismatched hash aborts the update rather than warning.

The binary never deploys itself. It is a deploy target for nothing; upgrades come only
from a signed release, never from a `git reset --hard` under a running job.

## Setup

Everything below runs as root on the target server.

**1. Configure.** `rec-deploy init` is an interactive wizard: it asks for a GitHub token,
validates it against `GET /user`, checks that the `repo` and `admin:repo_hook` scopes are
present (and names exactly what is missing), then asks for the listen address, the public
URL GitHub will post to, the discovery roots and the notification channels.

```sh
rec-deploy init
```

The token is also picked up from `GITHUB_TOKEN` / `GH_TOKEN`, or from `gh auth token` if
the `gh` CLI happens to be installed — `gh` is supported, never required. Config lands in
`/etc/rec-deploy/config.yaml`, mode `0600`.

Config edits apply live: the daemon re-reads its configuration for every
delivery, so a channel configured with `rec-deploy config` works on the very
next push — no restart. Only the listen address and the public URL, which bind
at startup, need `systemctl restart rec-deploy`. When in doubt,
`rec-deploy notify test` reports every channel's outcome on the spot.

### Setup walkthrough

What the wizard asks, in order:

**GitHub token** — a classic personal access token with the `repo` and
`admin:repo_hook` scopes, validated live against the API. Empty keeps the stored
one. Details below.

**Listen address** — the local bind, `0.0.0.0:9000` by default. This is not what
GitHub sees; details below.

**Public URL** — the address registered as the webhook destination. It must be
reachable from the internet; details below.

**Scan roots** — comma-separated globs walked to find checkouts of registered
repositories, e.g. `/home/*/web,/var/www`. Empty keeps the current roots.

**Notifications** — Space toggles `telegram` / `email`, Enter confirms; each
selected channel is configured on the spot. Confirming with none selected runs
without notifications.

**Test notification** — offered whenever a channel is configured; a wrong chat ID
or SMTP password is cheaper to find here than on a real deploy.

**Auto-update** — opt-in systemd timer that checks releases hourly and swaps the
binary after checksum verification, restarting the daemon. Disable any time with
`systemctl disable --now rec-deploy-update.timer`.

#### Getting a GitHub token

The wizard's first question. Create it once per operator account:

1. Open <https://github.com/settings/tokens> → **Generate new token (classic)** —
   on the account that owns the repositories you will deploy (the token manages
   their deploy keys and webhooks).
2. Tick exactly two scopes: **`repo`** (private repo access + deploy keys) and
   **`admin:repo_hook`** (webhooks). Nothing else.
3. Pick any expiration you are comfortable with. The token is stored root-only on
   the server; when it expires, re-run `rec-deploy init` and paste the new one —
   every other answer is kept.
4. Paste it at the prompt (input is masked).

Fine-grained tokens are rejected by design: the API does not report their scopes,
so the wizard cannot verify the token can actually do the job.

#### Listen address vs public URL

Two different questions that look alike:

- **Listen address** is local: which of the machine's interfaces the daemon binds.
  `0.0.0.0:9000` (the default) means "every interface" and is right for most
  servers. GitHub never sees this value.
- **Public URL** is what gets registered as the webhook destination — where GitHub
  actually delivers. It must be reachable from the internet: usually
  `http://<public-ip>:9000`, with that port open in the firewall.

```
GitHub ── POST to the public URL ──▶ public IP :9000 ──▶ daemon bound on 0.0.0.0:9000
```

Bind `127.0.0.1` only when a local reverse proxy (nginx with TLS) sits in front;
then the public URL is the proxy's HTTPS address.

**2. Start the daemon.**

```sh
systemctl daemon-reload
systemctl enable --now rec-deploy
journalctl -u rec-deploy -f
```

It listens on `0.0.0.0:9000` by default and serves `POST /hook/{token}` and
`GET /health`. Open the port on the firewall.

**3. Register a repository.**

```sh
rec-deploy repo add rdcstarr/tema-mea
```

That generates a passphrase-less ed25519 key pair, uploads the public half as a read-only
deploy key, and creates a `push` webhook on the repo pointing at this server's public URL
with a fresh HMAC secret. `rec-deploy repo remove` deletes both **on GitHub**, not just
locally; `rec-deploy repo rotate` rolls the secret and the key.

**4. Get the code onto the box.** Either clone it yourself, or:

```sh
rec-deploy repo install rdcstarr/tema-mea /home/andrei/web/site.ro/public_html/wp-content/themes/tema
```

which clones as the directory's owner. If the checkout contains a manifest, install
immediately runs its `post_deploy` pipeline with the same ownership, timeout, locking,
logging and rollback rules as a normal deploy. Discovery then finds the checkout on every
push.

## Uninstall

One command removes everything the installer and the wizard created:

```sh
rec-deploy uninstall
```

Interactively it shows what it found, then asks two questions: whether to also
delete the deploy keys and webhooks on GitHub for every registered repository
(recommended — otherwise they stay there, pushing into a dead endpoint), and
whether to delete the local data (token, HMAC secrets, deploy keys, state
database). Non-interactively it requires `--yes`, with `--keep-github` /
`--keep-data` for the same choices.

The GitHub cleanup runs first, while the token and the stored IDs still exist;
if any of it fails, uninstall stops before destroying the data they live in.
Installs from a `.deb`/`.rpm` keep their binary and unit files — finish those
with `dpkg -r rec-deploy` / `rpm -e rec-deploy`. The deployed checkouts on
disk are never touched.

## The manifest — `.rec-deploy.yml` or `.rec-deploy.yaml`

Committed at the root of every deployable repository, and read **after** the pull — so the
deploy steps version with the code. Both YAML filename extensions are supported; keep
only one of them in a checkout.

```yaml
repository: rdcstarr/tema-mea

post_deploy:
  - composer install --no-dev --optimize-autoloader
  - run: npm ci && npm run build
    timeout: 15m
  - run: wp cache flush
    continue_on_failure: true

rollback_on_failure: true
```

| Key | Default | Meaning |
| --- | --- | --- |
| `repository` | required | The `owner/repo` this checkout must belong to. Verified against `git remote get-url origin` before anything runs; a mismatch aborts that path. |
| `post_deploy` | `[]` | Steps run in order after a successful git sync. |
| `rollback_on_failure` | `false` | On a failed step, reset the tree to the pre-deploy SHA and re-run the *previous* manifest's `post_deploy`. |

A **step** is either form:

```yaml
post_deploy:
  - composer install          # string: the command, default timeout, fail-fast
  - run: npm ci && npm run build
    timeout: 15m              # Go duration; default 10m
    continue_on_failure: true # default false — otherwise a non-zero exit stops the pipeline
```

Parsing is strict: an unknown top-level key is an error, so a typo like `post_deploys:`
fails loudly instead of silently deploying nothing.

**An absent or empty `post_deploy` is legitimate**, and means exactly what it says: pull
the code, run nothing. The sync *is* the deploy — the common case for a static theme or a
plain content repo. What is *not* legitimate, and is a hard failure, is a **missing or
invalid manifest**: the prototype rescued that into an empty command list and reported
success having run nothing, which is the single defect this file format exists to prevent.

## What happens on a push

1. GitHub posts to `/hook/{token}`. The token is looked up in SQLite (unknown → `404`),
   `X-Hub-Signature-256` is verified as HMAC-SHA256 over the **raw** body with
   `hmac.Equal`, and `X-GitHub-Delivery` is deduplicated against a unique index — a
   replayed delivery is a no-op `200`.
2. The daemon answers `200` immediately and works on a goroutine. GitHub's 10-second
   budget is never at risk.
3. It walks the configured roots for a `.rec-deploy.yml` or `.rec-deploy.yaml`, keeping the checkouts whose
   origin remote is that repository. It does
   not descend into a git repository once it finds one, and prunes `node_modules`,
   `vendor`, `uploads`, `cache`. **Zero installations found is an error**, reported and
   notified — not silence.
4. Each path deploys under an advisory `flock`, and only if the pushed ref matches the
   branch that checkout is actually on. So staging on `develop` and production on `main`
   coexist on one server, from one repo, with no configuration.
5. Per path: record the current SHA (the rollback point), `git fetch` + `reset --hard` +
   `clean -fd` **as the directory's owner**, re-read the manifest from the fresh tree, then
   run `post_deploy` sequentially under real timeouts, keeping the last 8 KB of output.
6. One notification summarising every path: Telegram, email, journald.

Privilege drop is `syscall.Credential` — `sudo` is gone entirely, along with its
shell-quoting layer and the need for passwordless sudo to arbitrary users. The owner is
read from the directory's inode (`stat`), never parsed out of the path, so no layout is
assumed. Supplementary groups, `HOME` and `SHELL` come from the real passwd entry.

## Many servers

Each server registers **its own** webhook on the repository. GitHub allows several
webhooks per repo, so fan-out across machines is free:

```
GitHub push
   ├──► http://server-a:9000/hook/<token-a>   ─┐
   └──► http://server-b:9000/hook/<token-b>   ─┤ independent, unaware of each other
```

No control plane, no agent inventory, no SSH between servers, no single point of failure.
A server that is down misses its push and catches up with `rec-deploy deploy owner/repo`.

## Security posture

Be clear-eyed about the default: the webhook payload travels **in clear over HTTP** —
repository name, branch, commit message, author. Anyone on the path can read it. What
they cannot do is *forge* it: the HMAC-SHA256 signature is verified over the raw body with
a constant-time compare, and the secret itself never crosses the network. **No credential
is transmitted** — not the GitHub token, not the deploy key, not the HMAC secret; the
payload carries none of them, and the binary compiles none of them in. Secrets live
root-only on disk (`0600` config, `0400` keys), redact to `••••` + last 4 in output, and
the ed25519 private key never touches a site user's disk: deploys reach it through an
ephemeral in-process SSH agent that is destroyed when the deploy ends. github.com's host
keys are pinned, never blindly accepted. Root-owned deploy targets are **allowed** — some
services in `/opt` belong to no site user — but they are **flagged loudly** (`⚠ root` in
`rec-deploy status` and in every notification), because push access to such a repository is
root on the server and that should be visible rather than discovered. If leaking commit
metadata to a network observer is unacceptable, run with `--listen 127.0.0.1:9000` behind
an nginx with TLS; no extra code is needed.

## Commands

```sh
rec-deploy                              # interactive hub (TTY) / help (piped)
rec-deploy init                         # setup wizard: token, listen, public URL, roots, notifications
rec-deploy serve                        # webhook daemon (systemd)

rec-deploy repo add <owner/repo>        # keygen + upload deploy key + create webhook
rec-deploy repo list                    # registered repositories
rec-deploy repo show <owner/repo>       # key id, hook id, checkouts, last deploy
rec-deploy repo remove <owner/repo>     # deletes the key and the hook on GitHub too
rec-deploy repo rotate <owner/repo>     # roll the HMAC secret and the deploy key
rec-deploy repo install <owner/repo> <path>   # clone into path as its owner

rec-deploy deploy <owner/repo> [--path P]     # deploy now, streaming output live
rec-deploy rollback <owner/repo> [--path P]   # back to the previous SHA
rec-deploy scan                         # what discovery finds, and why
rec-deploy status                       # daemon health, repos, last deploy per path
rec-deploy logs [owner/repo]            # deploy history
rec-deploy notify test                  # send a test notification and report each channel's outcome

rec-deploy config get <key> | set <key> <value> | path
rec-deploy self-update                  # SHA-256 verified against checksums.txt; fails closed
rec-deploy uninstall [--keep-github] [--keep-data]   # removes services, local data and github deploy keys/webhooks
rec-deploy completion [bash|zsh|fish|powershell]
rec-deploy version
```

Every command is scriptable and interactive: run without the input it needs in a terminal
and it opens a menu or prompts; piped or under systemd it falls back to help, a status
summary, or an error naming the flag. Destructive actions confirm in a TTY and require
`--yes` otherwise.

The bare `rec-deploy` hub above is curated, not exhaustive: `deploy`, `repo`, `logs`,
`status`, `config`, `mcp`, `self-update` and `uninstall`, plus `init` until the setup
wizard has run to completion. `rollback` and `scan` are reached from the `repo` and `status` menus,
and `notify test` from the Telegram/Email sections of `config` — every command above stays
fully typable and listed in `--help` whether or not the hub shows it on its first screen.

A few commands changed shape, not name or flags. `logs` run in a terminal opens a
browser — pick a repository, then a deploy, then (when that deploy touched more than one
checkout) which checkout, then read what each command printed in a scrollable pane;
`--path`, `--limit`, `--json` and every non-TTY run are unchanged. `self-update` is one
checked, verified, fail-closed action, not a menu: it reports current → latest, confirms,
installs, and offers a supervised restart; `--check` and `--restart` are unchanged.
`status` now offers to run `scan` and to start, stop or restart the daemon underneath the
health report it already prints. `serve` is the process those actions — and systemd —
start; it refuses to run a second time beside a `rec-deploy.service` that is already
active, so start, stop and restart it from `rec-deploy status` or `systemctl`, never by
running `serve` by hand.

Global flags: `--config`, `--json`, `--no-color`, `-v/--verbose`, `--yes`.

## Paths

| | As root | Otherwise (development) |
| --- | --- | --- |
| Config | `/etc/rec-deploy/config.yaml` | `~/.config/rec-deploy/config.yaml` |
| State, keys, locks | `/var/lib/rec-deploy/` | `~/.config/rec-deploy/` |

State is SQLite (`modernc.org/sqlite` — pure Go, no cgo, so the binary stays static).

## Build from source

```sh
make build      # static binary into ./rec-deploy
make test
make lint
make snapshot   # local GoReleaser build, no publish
```

Requires Go 1.26+. The authoritative design lives in
`docs/superpowers/specs/2026-07-14-rec-deploy-cli-design.md`, which is kept outside
version control — ask a maintainer for it.

## MCP server

`rec-deploy mcp` exposes this server's deployment state to MCP clients over
stdio. The MCP surface is read-only: it can list repositories, installations
and deploys, inspect one deploy, validate a discovered checkout's manifest and
report service health. It cannot deploy, roll back or change configuration.

Configure a local MCP client to launch the installed binary:

```json
{
  "mcpServers": {
    "rec-deploy": {
      "command": "/usr/bin/rec-deploy",
      "args": ["mcp"]
    }
  }
}
```

The process must run as a user that can read rec-deploy's state database and
configured discovery roots. Repository webhook tokens and secrets, GitHub and
notification credentials, GitHub delivery IDs, and captured command output are
never returned by the MCP tools. Standard output is reserved for MCP protocol
messages; diagnostics go to standard error.

For an agent on another machine, enable the isolated Cloudflare Tunnel wizard:

```bash
sudo rec-deploy mcp enable
rec-deploy mcp status
```

`mcp status` reports access mode, endpoint and service health in at most four lines; run
in a terminal it is also where you act on them — rotate the token, restart the service, or
re-check — rather than a readout you go elsewhere to fix.

Choose a one-time Cloudflare Account API token (recommended) or browser login.
Create the token under **Manage Account → Account API Tokens** with two policies:
**Entire account → Cloudflare One Connector: cloudflared → Edit**, and
**Specific domain → DNS → Edit + Zone → Read**. The wizard asks for the
32-character Account ID from the account Overview, then the token. rec-deploy
creates the tunnel and proxied DNS record, installs separate hardened services,
selects a loopback-only origin port and verifies the public HTTPS endpoint. It
does not change Hestia, Nginx, Apache, the host firewall or their ports.

The API token or browser account certificate is discarded after provisioning.
Only the tunnel-specific runtime credential remains root-only under
`/var/lib/rec-deploy/mcp`. The MCP bearer token is written to a root-only
provisioning file and only its SHA-256 digest is stored in configuration. Copy
the token to the client, delete the provisioning file, and configure the public
HTTPS URL with `Authorization: Bearer TOKEN`. Rotate a lost token with
`rec-deploy mcp token rotate`; the MCP service must be restarted to apply it.

## License

MIT.
