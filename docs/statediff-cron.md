# 变更对账 · 每日巡检

deltascope 每天为整机状态拍一张快照,把"今天 vs 昨天变了什么"对账出来。
纯本地、无外部依赖、只读采集。

## 采集内容

系统身份 · 内核参数(sysctl)· 软件包 · 内核模块 · 网络配置 · 监听端口 ·
防火墙 · 存储挂载 · LVM/RAID · 服务状态 · 定时任务 · 配置文件指纹 · 安全态。
单机一次采集约 1700+ 项事实。权限不足或工具缺失的项优雅跳过,不影响整体。

## 安装

每小时拍一张快照,保留 7 天:

    0 * * * * root /usr/local/bin/deltascope snapshot -data /var/lib/deltascope -quiet

每天早 8 点把过去 24 小时的变更写入日志:

    0 8 * * * root /usr/local/bin/deltascope statediff -data /var/lib/deltascope -since 24h -no-color >> /var/log/deltascope-changes.log 2>&1

接入告警(有变更时退出码为 3):

    */30 * * * * root /usr/local/bin/deltascope statediff -data /var/lib/deltascope -since 30m -summary || /usr/local/bin/notify "$(hostname) 检出状态变更"

## 手动查看

    deltascope snapshot                      # 立即拍一张
    deltascope statediff -since 24h          # 对比 24 小时前
    deltascope statediff -a 2026-07-20T14:00 -b 2026-07-21T14:00

退出码 3 表示检出变更,0 表示状态一致,便于脚本判读。
