#!/usr/bin/env bash
# usage: LISTEN_ADDR=0.0.0.0:8080 ./deploy.sh
# offline: put pre-downloaded pcp rpms into ./rpms/
#
# Creating the admin account happens in the browser on first visit, not
# here -- see the final message this script prints.
set -euo pipefail

RETENTION_DAYS="${RETENTION_DAYS:-7}"        # PCP archive retention (ring cleanup)
LISTEN_ADDR="${LISTEN_ADDR:-0.0.0.0:8080}"   # web listen address
INSTALL_BIN="/usr/local/bin/deltascope"
DATA_DIR="/var/lib/deltascope"
SVC_USER="deltascope"

[[ $EUID -eq 0 ]] || { echo "must be run as root"; exit 1; }
cd "$(dirname "$0")"
[[ -x ./deltascope ]] || {
    echo "deltascope binary missing in this directory."
    echo "Download one from dist/ in the repository, or build with:"
    echo "  CGO_ENABLED=1 CC=musl-gcc go build -tags cgosqlite \\"
    echo "    -ldflags '-linkmode external -extldflags -static' -o deltascope ."
    exit 1
}

echo "==> [1/6] installing PCP"
if compgen -G "rpms/*.rpm" >/dev/null; then
    echo "    using local offline RPMs (rpms/)"
    dnf install -y ./rpms/*.rpm || rpm -Uvh --replacepkgs rpms/*.rpm
elif ! command -v pmlogsummary >/dev/null; then
    dnf install -y pcp pcp-system-tools
else
    echo "    already installed, skipping"
fi
command -v pmrep >/dev/null || { echo "pmrep missing (pcp-system-tools), aborting"; exit 1; }

echo "==> [2/6] enabling pmcd / pmlogger, ${RETENTION_DAYS}-day ring cleanup, tiered sampling"
systemctl enable --now pmcd pmlogger
TIMERS=/etc/sysconfig/pmlogger_timers
touch "$TIMERS"
if grep -q '^PMLOGGER_DAILY_PARAMS=' "$TIMERS"; then
    sed -i "s|^PMLOGGER_DAILY_PARAMS=.*|PMLOGGER_DAILY_PARAMS=\"-k ${RETENTION_DAYS}\"|" "$TIMERS"
else
    echo "PMLOGGER_DAILY_PARAMS=\"-k ${RETENTION_DAYS}\"" >> "$TIMERS"
fi
systemctl enable --now pmlogger_daily.timer 2>/dev/null || true
systemctl enable --now pmlogger_check.timer 2>/dev/null || true

# Sampling groups mirror deltascope's built-in catalog (internal/pcp/catalog.go):
# hot = metrics with meaningful second-to-second movement; warm = per-device/
# per-NIC detail; cold = things that only change slowly. If you add
# metrics via `deltascope catalog export`, extend this config to match --
# a metric pmlogger never records can't appear in any report.
#
# kernel.percpu.cpu is in the hot tier, not warm: the reasoning chain's
# per-core saturation and peak states need enough per-core samples in a
# short window to clear their MinSamples guard. At 60s a 30-minute window
# holds only ~30 samples, right at the guard's edge, so a brief per-core
# spike could be missed; at 10s it holds ~180 and is caught reliably.
cat > /etc/pcp/pmlogger/deltascope.config <<'PMCFG'
log mandatory on every 10 seconds {
    kernel.all
    kernel.percpu.cpu
    mem.util
    mem.vmstat
    swap
    network.tcp
    network.softnet
    network.sockstat
}
log mandatory on every 60 seconds {
    disk.all
    disk.dev
    disk.dm
    disk.md
    network.interface
    network.tcpconn
    network.udp
}
log mandatory on every 5 minutes {
    filesys
    vfs
    network.icmp
    network.ip
    hinv
}
[access]
disallow .* : all;
disallow :* : all;
allow local:* : enquire;
PMCFG
CTRL=/etc/pcp/pmlogger/control.d/local
if [[ -f "$CTRL" ]] && grep -q 'config.default' "$CTRL"; then
    sed -i 's|-c config.default|-c /etc/pcp/pmlogger/deltascope.config|' "$CTRL"
    systemctl restart pmlogger
    echo "    switched pmlogger to the tiered sampling config"
else
    echo "    default control line not found; point pmlogger -c at /etc/pcp/pmlogger/deltascope.config manually"
fi

echo "==> [3/6] installing binary and data directory"
install -m 0755 ./deltascope "$INSTALL_BIN"
id "$SVC_USER" &>/dev/null || useradd --system --home-dir "$DATA_DIR" --shell /sbin/nologin "$SVC_USER"
usermod -aG pcp "$SVC_USER"       # read /var/log/pcp/pmlogger archives
mkdir -p "$DATA_DIR"
chown "$SVC_USER:$SVC_USER" "$DATA_DIR"
chmod 750 "$DATA_DIR"

echo "==> [4/6] writing systemd service"
cat > /etc/systemd/system/deltascope.service <<EOF
[Unit]
Description=deltascope change & performance diagnostics web service
After=network.target pmlogger.service
Wants=pmlogger.service

[Service]
User=${SVC_USER}
Group=${SVC_USER}
SupplementaryGroups=pcp
ExecStart=${INSTALL_BIN} serve -listen ${LISTEN_ADDR} -data ${DATA_DIR}
Restart=on-failure
RestartSec=3

NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=${DATA_DIR}
ReadOnlyPaths=/var/log/pcp
PrivateTmp=true
ProtectKernelTunables=true
ProtectControlGroups=true
RestrictSUIDSGID=true

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now deltascope

echo "==> [5/6] firewall (optional)"
PORT="${LISTEN_ADDR##*:}"
if systemctl is-active --quiet firewalld; then
    firewall-cmd --permanent --add-port="${PORT}/tcp" >/dev/null
    firewall-cmd --reload >/dev/null
    echo "    firewalld now allows ${PORT}/tcp"
else
    echo "    firewalld not running, skipping"
fi

echo "==> [6/6] waiting for the service to come up"
for _ in $(seq 1 10); do
    curl -sf "http://127.0.0.1:${PORT}/api/setup-status" >/dev/null 2>&1 && break
    sleep 1
done

echo
echo "deploy complete ✔"
echo
echo "  Open http://<this-host-ip>:${PORT}/ in a browser to create the"
echo "  admin account -- the login page detects there's no account yet"
echo "  and offers to create one directly, no CLI step required."
echo
echo "  service status:     systemctl status deltascope"
echo "  archive retention:  ${RETENTION_DAYS} days (edit $TIMERS, restart pmlogger_daily.timer)"
echo "  snapshot retention: 7 days, captured automatically every 10 minutes by the service"
echo "  note: comparisons need at least two full sampling periods of history before they'll show anything"
