# 指标目录 Profiles

- catalog-full.json — 内置全量 (与二进制内嵌一致)
- catalog-core.json — 精简档: 去掉每核/ICMP/IP 层/LVM/MD 等高基数与深水区指标, 适合低配机器或只关注主干

用法: `deltascope serve -catalog profiles/catalog-core.json`
自定义: 从任一份复制修改, 极性 polarity 取 worse_up | better_up | neutral, fold: true 表示前端聚合折叠。
诊断规则同理: `deltascope rules export > rules.json` 编辑后 `serve -rules rules.json`。
归档中不存在的指标自动跳过, 放心做加法。
