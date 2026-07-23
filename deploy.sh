#!/usr/bin/env bash
# usage: DSCOPE_ADMIN_USER=admin DSCOPE_ADMIN_PASS='...' ./deploy.sh
# offline: put pre-downloaded pcp rpms into ./rpms/
set -euo pipefail

RETENTION_DAYS="${RETENTION_DAYS:-7}"        # archive retention days (ring cleanup)
LISTEN_ADDR="${LISTEN_ADDR:-0.0.0.0:8080}"   # web listen address
INSTALL_BIN="/usr/local/bin/deltascope"
DATA_DIR="/var/lib/deltascope"
SVC_USER="deltascope"

[[ $EUID -eq 0 ]] || { echo "must be run as root"; exit 1; }
cd "$(dirname "$0")"
[[ -x ./deltascope ]] || { echo "deltascope binary missing in this directory (run make build first)"; exit 1; }

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

echo "==> [2/6] enabling pmcd / pmlogger and setting ${RETENTION_DAYS}-day ring cleanup"
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

echo "==> [2.5/6] writing tiered sampling config (hot 10s / warm 60s / cold 5min)"
cat > /etc/pcp/pmlogger/deltascope.config <<'PMCFG'
log mandatory on every 10 seconds {
    kernel.all
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
    kernel.percpu.cpu
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

echo "==> [4/6] creating admin account"
if [[ -n "${DSCOPE_ADMIN_USER:-}" && -n "${DSCOPE_ADMIN_PASS:-}" ]]; then
    DSCOPE_PASSWORD="$DSCOPE_ADMIN_PASS" \
        sudo -u "$SVC_USER" --preserve-env=DSCOPE_PASSWORD \
        "$INSTALL_BIN" user add "$DSCOPE_ADMIN_USER" -data "$DATA_DIR"
else
    echo "    DSCOPE_ADMIN_USER/DSCOPE_ADMIN_PASS not set, run manually later:"
    echo "    sudo -u $SVC_USER $INSTALL_BIN user add <name> -data $DATA_DIR"
fi

echo "==> [5/6] writing systemd service"
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

echo "==> [6/6] firewall (optional)"
PORT="${LISTEN_ADDR##*:}"
if systemctl is-active --quiet firewalld; then
    firewall-cmd --permanent --add-port="${PORT}/tcp" >/dev/null
    firewall-cmd --reload >/dev/null
    echo "    firewalld now allows ${PORT}/tcp"
else
    echo "    firewalld not running, skipping"
fi

echo
echo "deploy complete ✔  http://<this-host-ip>:${PORT}/"
echo "  service status:  systemctl status deltascope"
echo "  user management: sudo -u $SVC_USER $INSTALL_BIN user add|del|list <name> -data $DATA_DIR"
echo "  archive retention: ${RETENTION_DAYS} days (edit $TIMERS then restart pmlogger_daily.timer)"
echo "  note: comparisons need pmlogger to accumulate at least two full periods of data before they'll work"
