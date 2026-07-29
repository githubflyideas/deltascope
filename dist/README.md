# deltascope prebuilt binaries

Committed directly in this directory -- download the file for your
architecture and run it.

All statically linked, no runtime dependencies (neither glibc nor musl
needed) -- download and run. The host still needs PCP installed
(pcp + pcp-system-tools); see deploy.sh at the repo root.

| File | Arch | Min kernel | Targets |
|---|---|---|---|
| deltascope-linux-amd64 | x86_64 | 3.2 | Ubuntu 20.04/22.04/24.04 · Rocky 8/9/10 · RHEL/CentOS 7+ · Debian 10+ · Amazon Linux 2 / 2023 · SUSE SLES 12+ · openEuler / Anolis / Kylin, etc. |
| deltascope-linux-arm64 | aarch64 | 4.x | AWS Graviton (AL2/AL2023/Ubuntu arm64) · Huawei Kunpeng · Phytium and other ARM servers |

CentOS/RHEL 6 (kernel 2.6.32) is no longer supported: PCP itself needs a
kernel new enough to expose the counters deltascope depends on, and
EL6's PCP package is too old to run pmrep or hand an archive directory
to `-a` -- the binary would start but most of the product wouldn't work.
If you're still on EL6, upgrade the host or point deltascope at a PCP
archive collected elsewhere.

## Quick start

```bash
chmod +x deltascope-linux-amd64
sudo cp deltascope-linux-amd64 /usr/local/bin/deltascope
sudo DSCOPE_PASSWORD='a-strong-password' deltascope user add admin -data /var/lib/deltascope
deltascope serve -listen 0.0.0.0:8080 -data /var/lib/deltascope
```

## Verify

```bash
sha256sum -c SHA256SUMS
```

## Notes

- The binary's SQLite driver is the statically linked mattn/go-sqlite3
  (cgo, static musl/glibc); the default source build uses pure-Go
  modernc.org/sqlite. Both produce the exact same database file format
  and are interchangeable.
- el6 caveat: PCP in CentOS 6's EPEL is old and pmrep may be missing
  (trends won't work), and older PCP can't hand an entire archive
  directory to `-a`, limiting cross-day comparisons. The binary runs,
  but full functionality needs EL7+ / PCP 5.3+.
- Amazon Linux 2 reached EOL on 2026-06-30; use AL2023 for new deployments.
