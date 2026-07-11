#!/usr/bin/env bash
# usage: DSCOPE_ADMIN_USER=admin DSCOPE_ADMIN_PASS='...' ./deploy.sh
# offline: put pre-downloaded pcp rpms into ./rpms/
set -euo pipefail

RETENTION_DAYS="${RETENTION_DAYS:-7}"        # 归档保留天数(环形清理)
LISTEN_ADDR="${LISTEN_ADDR:-0.0.0.0:8080}"   # Web 监听地址
INSTALL_BIN="/usr/local/bin/deltascope"
DATA_DIR="/var/lib/deltascope"
SVC_USER="deltascope"

[[ $EUID -eq 0 ]] || { echo "请以 root 运行"; exit 1; }
cd "$(dirname "$0")"
[[ -x ./deltascope ]] || { echo "当前目录缺少 deltascope 二进制(先 make build)"; exit 1; }

echo "==> [1/6] 安装 PCP"
if compgen -G "rpms/*.rpm" >/dev/null; then
    echo "    使用本地离线 RPM (rpms/)"
    dnf install -y ./rpms/*.rpm || rpm -Uvh --replacepkgs rpms/*.rpm
elif ! command -v pmlogsummary >/dev/null; then
    dnf install -y pcp pcp-system-tools
else
    echo "    已安装, 跳过"
fi
command -v pmrep >/dev/null || { echo "缺少 pmrep(pcp-system-tools), 终止"; exit 1; }

echo "==> [2/6] 启用 pmcd / pmlogger 并配置 ${RETENTION_DAYS} 天环形清理"
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

echo "==> [2.5/6] 写入分层采样配置 (热 10s / 温 60s / 冷 5min)"
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
    echo "    已切换 pmlogger 至分层采样配置"
else
    echo "    未找到默认 control 行, 请手动将 pmlogger -c 指向 /etc/pcp/pmlogger/deltascope.config"
fi

echo "==> [3/6] 安装二进制与数据目录"
install -m 0755 ./deltascope "$INSTALL_BIN"
id "$SVC_USER" &>/dev/null || useradd --system --home-dir "$DATA_DIR" --shell /sbin/nologin "$SVC_USER"
usermod -aG pcp "$SVC_USER"       # 读取 /var/log/pcp/pmlogger 归档
mkdir -p "$DATA_DIR"
chown "$SVC_USER:$SVC_USER" "$DATA_DIR"
chmod 750 "$DATA_DIR"

echo "==> [4/6] 创建管理员账号"
if [[ -n "${DSCOPE_ADMIN_USER:-}" && -n "${DSCOPE_ADMIN_PASS:-}" ]]; then
    DSCOPE_PASSWORD="$DSCOPE_ADMIN_PASS" \
        sudo -u "$SVC_USER" --preserve-env=DSCOPE_PASSWORD \
        "$INSTALL_BIN" user add "$DSCOPE_ADMIN_USER" -data "$DATA_DIR"
else
    echo "    未设置 DSCOPE_ADMIN_USER/DSCOPE_ADMIN_PASS, 稍后手动执行:"
    echo "    sudo -u $SVC_USER $INSTALL_BIN user add <name> -data $DATA_DIR"
fi

echo "==> [5/6] 写入 systemd 服务"
cat > /etc/systemd/system/deltascope.service <<EOF
[Unit]
Description=deltascope 性能倒退对比 Web 服务
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

echo "==> [6/6] 防火墙(可选)"
PORT="${LISTEN_ADDR##*:}"
if systemctl is-active --quiet firewalld; then
    firewall-cmd --permanent --add-port="${PORT}/tcp" >/dev/null
    firewall-cmd --reload >/dev/null
    echo "    firewalld 已放行 ${PORT}/tcp"
else
    echo "    firewalld 未运行, 跳过"
fi

echo
echo "部署完成 ✔  http://<本机IP>:${PORT}/"
echo "  服务状态:   systemctl status deltascope"
echo "  用户管理:   sudo -u $SVC_USER $INSTALL_BIN user add|del|list <name> -data $DATA_DIR"
echo "  归档保留:   ${RETENTION_DAYS} 天 (改 $TIMERS 后重启 pmlogger_daily.timer)"
echo "  注意: 首日归档不足两个完整时段时, 对比功能需等 pmlogger 积累数据"
