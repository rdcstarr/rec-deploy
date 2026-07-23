#!/usr/bin/env bash
#
# rec-deploy installer — downloads the matching static binary from GitHub Releases,
# installs the systemd units, and drops you straight into the setup wizard.
# No runtime (Go/PHP/Node) required; the binary is fully static.
#
# One-liner (the repo is public):
#
#   curl -fsSL https://get.rec.tools/deploy | sudo bash
#
# It is plain shell — read it first if you like before piping it into a root
# shell. It touches only the rec-deploy binary in /usr/bin (/usr/local/bin on
# macOS) and the systemd units in /etc/systemd/system/rec-deploy*.
#
# RELEASING: this file is served from main and installs the newest published
# release, so a change here goes live the moment it merges — against a tarball
# built from an older tag. Anything that expects new release contents (the units
# under packaging/, say) breaks every install until the next tag is pushed. Merge
# and tag together.
#
set -euo pipefail

REPO="rdcstarr/rec-deploy"
BINARY="rec-deploy"
BINDIR="/usr/bin"

# Captured at top scope, before any function runs: BASH_SOURCE[0] here is the
# real script path when executed from a file, and empty when the script arrived
# on stdin (curl | bash). It must be captured here, not reread inside need_root
# — once execution enters a function, bash pads BASH_SOURCE with $0 as a
# fallback, so a re-check inside the function would see "bash" instead of empty
# and defeat the whole point of this check.
SCRIPT_SOURCE="${BASH_SOURCE[0]:-}"

# UNITS are every systemd unit the installer drops in. The updater's two are
# installed but left disabled — `rec-deploy init` is where the operator opts in.
UNITS="rec-deploy.service rec-deploy-mcp.service rec-deploy-mcp-tunnel.service rec-deploy-mcp-update.service rec-deploy-mcp-update.timer rec-deploy-update.service rec-deploy-update.timer"

# Progress goes to stderr, never stdout: `platform` returns its value by printing
# it, so anything a helper writes to stdout lands inside `$(platform)`'s result.
info() { printf '\033[36m›\033[0m %s\n' "$1" >&2; }
ok()   { printf '\033[32m✓\033[0m %s\n' "$1" >&2; }
warn() { printf '\033[33m!\033[0m %s\n' "$1" >&2; }
err()  { printf '\033[31merror:\033[0m %s\n' "$1" >&2; exit 1; }

# need_root re-execs the script under sudo when it is not already root, so the
# daemon install (a system path and a systemd unit) can proceed. Piped into a
# shell there are no positional args to forward.
need_root() {
	[ "$(id -u)" -eq 0 ] && return 0
	command -v sudo >/dev/null 2>&1 || err "run me as root: this installs a system binary and a systemd unit"
	# Piped into a shell the script only ever existed on stdin — SCRIPT_SOURCE is
	# empty, there is no file to re-exec under sudo, and trusting $0 would run
	# whatever ./bash happens to be. Ask for a sudo re-run instead.
	[ -n "$SCRIPT_SOURCE" ] \
		|| err "not running as root — re-run with sudo:  curl -fsSL https://get.rec.tools/deploy | sudo bash"
	warn "re-running under sudo…"
	exec sudo -E bash "$SCRIPT_SOURCE" "$@"
}

# platform prints "<os> <arch>" using the release naming, or exits on anything
# unsupported.
platform() {
	local os arch
	os="$(uname -s | tr '[:upper:]' '[:lower:]')"
	case "$os" in
		linux | darwin) ;;
		*) err "unsupported OS: $os" ;;
	esac

	arch="$(uname -m)"
	case "$arch" in
		x86_64 | amd64) arch="amd64" ;;
		aarch64 | arm64) arch="arm64" ;;
		*) err "unsupported architecture: $arch — build from source instead" ;;
	esac

	printf '%s %s' "$os" "$arch"
}

# latest_tag resolves the newest published release tag from the GitHub API.
#
# The body is read in full before it is parsed. Piping curl straight into
# `grep -m1` makes grep exit on the first match and close the pipe while curl is
# still writing the tail of the ~17 KB response; curl then dies with EPIPE
# (exit 23) and `pipefail` fails the whole function.
latest_tag() {
	local json
	json="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest")" || return 1
	grep -m1 '"tag_name"' <<<"$json" | cut -d '"' -f4
}

# download_binary fetches and verifies the release tarball for os/arch at tag and
# extracts it into $tmp, leaving the binary at ${tmp}/${BINARY}.
download_binary() {
	local os="$1" arch="$2" tag="$3"
	local asset url sumurl

	asset="${BINARY}_${tag#v}_${os}_${arch}.tar.gz"
	url="https://github.com/${REPO}/releases/download/${tag}/${asset}"
	sumurl="https://github.com/${REPO}/releases/download/${tag}/checksums.txt"

	info "downloading ${asset}…"
	curl -fsSL "$url" -o "${tmp}/${asset}" || err "download failed: $url"

	# Verify SHA-256 against the release checksums — fail closed, like the binary's
	# own self-update does. An unreachable checksums.txt is a refusal, not a warning:
	# whoever can serve a tampered tarball can also fail this request, so treating it
	# as "skip verification" hands the check to the one attacker it exists to stop.
	curl -fsSL "$sumurl" -o "${tmp}/checksums.txt" \
		|| err "could not fetch checksums.txt for ${tag} — refusing to install unverified"

	# awk on an exact field, not `grep " ${asset}$"`: the dots in the asset name are
	# regex wildcards, so grep can return a different entry's hash. awk also exits 0
	# when nothing matches, which keeps the empty-want check below reachable — under
	# `pipefail` a non-matching grep aborts the script with no message at all.
	local want got
	want="$(awk -v a="$asset" '$2 == a {print $1}' "${tmp}/checksums.txt")"

	# macOS has no GNU coreutils sha256sum; shasum (perl, always present there)
	# prints the same "<hash>  <file>" format. No hash tool at all is a refusal —
	# verification is fail-closed, never skipped.
	if command -v sha256sum >/dev/null 2>&1; then
		got="$(sha256sum "${tmp}/${asset}" | awk '{print $1}')"
	elif command -v shasum >/dev/null 2>&1; then
		got="$(shasum -a 256 "${tmp}/${asset}" | awk '{print $1}')"
	else
		err "neither sha256sum nor shasum found — refusing to install unverified"
	fi
	[ -n "$want" ] || err "no checksum listed for ${asset} in checksums.txt"
	[ "$want" = "$got" ] || err "checksum mismatch for ${asset} — refusing to install"
	ok "checksum verified"

	tar -C "$tmp" -xzf "${tmp}/${asset}"
}

# install_unit writes the systemd units and reloads the manager. It is skipped on
# a host without systemd (a Mac, a container) — there the binary alone is useful.
#
# The units come out of the tarball download_binary already verified, so they are
# pinned to the same tag as the binary and covered by the same checksum. They used
# to be curled from main: the release, not the push, is the unit of shipping, and
# main moving must do nothing until a tag is pushed — yet main moving silently
# changed what `ExecStart` ran on every fresh install. They were also the one part
# of a root install that no checksum covered.
install_unit() {
	command -v systemctl >/dev/null 2>&1 || { warn "no systemd — skipping the systemd units"; return 1; }

	local tag="$1" unit
	for unit in $UNITS; do
		# A release older than the units in its tarball is not a reason to reach for
		# the network: fetching them unverified would put the hole back on the one
		# path where it matters. So say what actually happened and where the units
		# are — this script is served from main and installs the newest published
		# release, so it goes live the moment it merges, and every release tagged
		# before it carries no packaging/ at all.
		[ -f "${tmp}/packaging/${unit}" ] \
			|| err "release ${tag} predates the units shipping in the tarball, so there is nothing verified to install. Take them from https://github.com/${REPO}/tree/${tag}/packaging and copy them into /etc/systemd/system yourself, or wait for the next release"

		install -m 0644 "${tmp}/packaging/${unit}" "/etc/systemd/system/${unit}" \
			|| err "could not install the systemd unit ${unit}"
	done

	systemctl daemon-reload
	ok "installed the systemd units in /etc/systemd/system"

	return 0
}

main() {
	need_root "$@"

	local os arch tag plat state have_systemd=0 restarted=0
	# platform reports failure by exiting, but inside "$( )" that exit only kills
	# the subshell; the failed assignment stops the script under set -e, and the
	# || exit makes that stop explicit rather than implicit.
	plat="$(platform)" || exit 1
	read -r os arch <<<"$plat"

	# /usr/bin is SIP-protected on macOS — not even root may write to it. The
	# stock admin-writable location is /usr/local/bin.
	if [ "$os" = "darwin" ]; then
		BINDIR="/usr/local/bin"
	fi

	# `|| err` is load-bearing, not decoration: without it `set -e` aborts on the
	# failed assignment and the install dies with no message at all.
	tag="$(latest_tag)" || err "could not reach the GitHub API — check connectivity and the API rate limit for this IP"
	[ -n "$tag" ] || err "could not determine the latest release — is ${REPO} published?"

	# tmp is a global, not a local: the EXIT trap that removes it fires after main
	# has returned, when a local would already be out of scope — `set -u` would then
	# abort the trap and leak the directory on every run. It also has to be created
	# here rather than at file scope, after need_root: `exec sudo` replaces the
	# process image, and an EXIT trap set before that never fires.
	tmp="$(mktemp -d)"
	trap 'rm -rf "$tmp"' EXIT

	download_binary "$os" "$arch" "$tag"
	# /usr/local/bin may not exist on a fresh macOS; -d is a no-op where it does.
	install -d "${BINDIR}"
	install -m 0755 "${tmp}/${BINARY}" "${BINDIR}/${BINARY}"
	ok "installed ${BINARY} ${tag} to ${BINDIR}/${BINARY}"

	if [ "$os" = "linux" ] && install_unit "$tag"; then
		have_systemd=1
	fi

	# An upgrade replaces the binary on disk, but a running daemon keeps the old
	# image in memory until it restarts. Restart it now so the version reported
	# above is the one actually serving webhooks.
	if [ "$have_systemd" -eq 1 ] && systemctl is-active --quiet rec-deploy; then
		if systemctl restart rec-deploy; then
			ok "restarted the running daemon on the new binary"
			restarted=1
		else
			warn "could not restart the running daemon — the old binary keeps serving until:  systemctl restart rec-deploy"
		fi
	fi

	# The payoff: drop straight into the wizard. Piped installs have the script on
	# stdin, so the wizard reads from the controlling terminal instead. Opening
	# /dev/tty is the gate: the node exists even without a controlling terminal
	# (cloud-init, CI), so `[ -e /dev/tty ]` lies — only open() tells the truth.
	if [ "$have_systemd" -eq 1 ] && { : </dev/tty; } 2>/dev/null; then
		info "starting the setup wizard…"
		echo
		if "${BINDIR}/${BINARY}" init </dev/tty; then
			# enable + restart, not `enable --now`: --now is a no-op on an already-
			# active unit, which would leave a running daemon on the stale config the
			# wizard just rewrote. restart also starts a stopped unit.
			#
			# --quiet drops systemctl's "Created symlink ..." line, which reports
			# the mechanism rather than the outcome.
			systemctl enable --quiet rec-deploy
			systemctl restart rec-deploy || true
			# A started daemon says nothing: the wizard's summary is the last thing
			# on screen and it already ends with the step that follows. Only a
			# daemon that did not come up is worth interrupting for.
			state="$(systemctl is-active rec-deploy || true)"
			if [ "$state" != "active" ]; then
				warn "rec-deploy is not running (state: ${state:-unknown}) — inspect it with:  journalctl -u rec-deploy -n 50"
			fi
		else
			warn "setup wizard was cancelled — configure later with:  rec-deploy init"
			printf '    then start the daemon with:  systemctl enable --now rec-deploy\n'
		fi
	else
		echo
		if [ "$restarted" -eq 1 ]; then
			info "upgrade complete — the daemon is already running the new binary"
		else
			info "installed. configure and start it with:"
			printf '    rec-deploy init\n'
			if [ "$have_systemd" -eq 1 ]; then
				printf '    systemctl enable --now rec-deploy\n'
			fi
		fi
	fi

	# `main "$@"` is the last command, so main's status is the installer's exit code
	# — and the documented one-liner pipes that into a root shell. Return explicitly
	# so no future trailing test can leak a false into it and fail a good install.
	return 0
}

main "$@"
