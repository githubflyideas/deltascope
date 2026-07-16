<div align="center">

<img src="../logo.svg" alt="deltascope" width="96">

# deltascope

**单机性能倒退示波台 —— 自带医生意见**

🌐 [English](../../README.md) · **中文** · [日本語](README.ja.md) · [한국어](README.ko.md) · [Deutsch](README.de.md) · [Français](README.fr.md) · [Español](README.es.md) · [Português](README.pt.md) · [Italiano](README.it.md) · [Русский](README.ru.md) · [ไทย](README.th.md) · [Bahasa Indonesia](README.id.md) · [Tiếng Việt](README.vi.md)


</div>

---

<div align="center">
<img src="../preview-diff.svg" width="100%" alt="A/B report">
<br><br>
<img src="../preview-trend.svg" width="100%" alt="Trends">
<br><sub>更多界面预览见英文主页</sub>
</div>

## 它做什么

选定两个时间窗口 —— **基线 A** 与 **对比 B** —— deltascope 基于本机历史归档计算各指标窗口均值,按指标极性逐项判定,输出三层报告:**诊断结论 → 依据 → 全量数据**。

## 特性

- **诊断规则引擎** —— 16 条内置跨指标经验规则(换页螺旋、磁盘饱和、accept 队列溢出、OOM、单核热点、SYN 压力、重启检测…),命中即给出人话结论 + 依据 + 下一步排查命令。永远不做合成健康分。
- **146 项内置指标 · 5 大分类** —— 含 PSI 压力、软中断丢包、每核热点(自动聚合折叠)、TCP 连接状态分布、直接回收、LVM/MD;高抖动指标有专属噪音阈值。
- **全量数据报告** —— 平稳行灰显不隐藏、行位置固定、行底色深浅 ∝ |Δ|、新出现 ⊕ / 消失 ⊖ 独立标记、Top-5 恶化锚点。
- **一切皆配置** —— 指标目录、诊断规则、阈值均为可导出 JSON,加载即校验;`profiles/` 提供 full/core 两档。
- **无头模式** —— `deltascope compare` 以文本(ANSI 彩色)或 JSON 输出同一份报告,发现恶化退出码 2,可直接进 cron 与告警管道。
- **为隔离环境而生** —— 单一静态二进制、前端与图表全部内嵌、本地认证、无 CDN、无遥测、零出网流量。

## 快速开始

预编译静态二进制见 [`dist/`](../../dist/):`linux-amd64`(内核 ≥3.2)、`linux-arm64`、`linux-amd64-el6`(内核 2.6.32)。

源码构建(联网开发机,一次):

```bash
make vendor && make test && make build
```

目标机部署(Rocky Linux 9 参考,可完全离线):

```bash
RETENTION_DAYS=7 LISTEN_ADDR=0.0.0.0:8080 \
DSCOPE_ADMIN_USER=admin DSCOPE_ADMIN_PASS='强密码' ./deploy.sh
```

deploy.sh 负责采集安装、分层采样(热 10s / 温 60s / 冷 5min)、环形保留、专用用户与加固 systemd 服务。

## 用法

```bash
deltascope serve   -listen :8080 -archive DIR -data DIR [-catalog F] [-rules F]
deltascope user    add|del|list <name>
deltascope catalog export > catalog.json
deltascope rules   export > rules.json
deltascope compare -a-start 2026-07-09T14:00 -a-end 2026-07-09T15:00 \
                   -b-start 2026-07-10T14:00 -b-end 2026-07-10T15:00 \
                   [-format text|json] [-all] [-no-color]
```

## 判定语义

- counter 指标按窗口换算**速率均值**(与 pmdiff 一致)
- Δ% = (B − A) / |A| × 100,`|Δ| ≥ 阈值` 才参与判定(全局默认 15%,高抖动指标有专属阈值)
- 极性:`worse_up`(CPU、重传)/ `better_up`(可用内存)/ `neutral`(吞吐量,仅提示关注)
- A=0 → B≠0 记 ∞;两侧均无数据的指标自动跳过 —— 目录可放心做加法
- 新出现 ⊕ / 消失 ⊖ 是独立事件,与幅度变化分开标记

## 安全

PBKDF2-HMAC-SHA256(600k 迭代,逐用户盐)· HMAC 签名无状态会话(HttpOnly,SameSite=Strict)· 按 IP 登录限速与时间侧信道拉平 · 指标名白名单 + 数组式 exec(永不过 shell)· 严格 CSP `default-src 'self'` 零内联 · 加固 systemd(非 root,ProtectSystem=strict)· 凭证永不出本机。

## 说明

时间窗按服务器本地时区解释 · 单窗口最长 32 天 · 趋势步长自适应(10s–15m,约 600 点)· 图表库随源码内嵌(Apache-2.0)· 首日部署需等归档积累后才能跨天对比。
