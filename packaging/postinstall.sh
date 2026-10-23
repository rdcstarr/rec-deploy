#!/bin/sh
# Post-install for the .deb / .rpm.
#
# Replacing the binary does not move a running process onto it: a long-lived
# daemon holds its own image until it execs again. Without this script, `dpkg -i`
# — or an unattended `apt upgrade` — leaves rec-deploy.service serving the
# previous release, silently, until someone restarts it by hand.
# That is the one upgrade path where nothing else would: install.sh restarts the
# daemon itself, and `self-update --restart` supervises its own restart.
set -e

# systemctl exists inside containers whose PID 1 is not systemd, where every call
# below fails with a confusing message. This directory check is what sd_booted(3)
# itself does.
[ -d /run/systemd/system ] || exit 0

# The package ships unit files, so systemd has to re-read them before a restart
# can run the new ones.
systemctl daemon-reload || true

# try-restart, never restart: a unit the operator deliberately stopped stays
# stopped, and a first install — where nothing is running yet — is a clean no-op.
# A failure must not fail the package: the files are correctly installed either
# way, and dpkg leaving the package half-configured over a service that would not
# come back is worse than an operator restarting it themselves.
systemctl try-restart rec-deploy.service || true

# A release that shipped remote MCP leaves its units enabled. The package manager
# deletes their files on upgrade but cannot stop what is already running from
# them, so stop and disable them here; on a box that never had them this fails
# silently, which is why the output is discarded. One call per unit: systemctl
# can fail the whole call on a name it has no file for.
for unit in rec-deploy-mcp.service rec-deploy-mcp-tunnel.service rec-deploy-mcp-update.timer; do
	systemctl disable --now "$unit" >/dev/null 2>&1 || true
done
systemctl daemon-reload || true

exit 0
