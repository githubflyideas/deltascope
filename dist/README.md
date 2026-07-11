# deltascope 预编译二进制

全部为静态链接,无任何运行时依赖(glibc/musl 均不需要),下载即用。
主机侧仍需安装 PCP(pcp + pcp-system-tools),见仓库根目录 deploy.sh。

| 文件 | 架构 | 最低内核 | 适用系统 |
|---|---|---|---|
| deltascope-linux-amd64 | x86_64 | 3.2 | Ubuntu 20.04/22.04/24.04 · Rocky 8/9/10 · RHEL/CentOS 7+ · Debian 10+ · Amazon Linux 2 / 2023 · SUSE SLES 12+ · openEuler / Anolis / 麒麟 等 |
| deltascope-linux-arm64 | aarch64 | 4.x | AWS Graviton (AL2/AL2023/Ubuntu arm64) · 华为鲲鹏 · 飞腾 等 ARM 服务器 |
| deltascope-linux-amd64-el6 | x86_64 | 2.6.32 | CentOS 6.x / RHEL 6.x (Go 1.23 构建) |

## 快速使用

```bash
chmod +x deltascope-linux-amd64
sudo cp deltascope-linux-amd64 /usr/local/bin/deltascope
sudo DSCOPE_PASSWORD='一个强密码' deltascope user add admin -data /var/lib/deltascope
deltascope serve -listen 0.0.0.0:8080 -data /var/lib/deltascope
```

## 校验

```bash
sha256sum -c SHA256SUMS
```

## 说明

- 二进制内 SQLite 驱动为静态链接的 mattn/go-sqlite3(cgo, musl/glibc 静态);
  源码默认构建使用纯 Go 的 modernc.org/sqlite。两者数据库文件格式完全一致,可互换。
- el6 注意:CentOS 6 的 EPEL 中 PCP 版本较老,pmrep 可能缺失(趋势页不可用),
  且旧版 PCP 不支持把归档目录整体传给 -a,跨天对比受限。二进制能跑,
  但完整功能建议 EL7+ / PCP 5.3+。
- Amazon Linux 2 已于 2026-06-30 EOL,新部署建议 AL2023。
