<div align="center">

<img src="docs/logo.svg" alt="deltascope" width="96">

# deltascope

**Performance regression scope for a single Linux box — with a doctor's notes**

[![Go](https://img.shields.io/badge/Go-1.22%2B-4cc9f0?logo=go&logoColor=white)](https://go.dev)
[![PCP](https://img.shields.io/badge/backend-Performance%20Co--Pilot-e8a33d)](https://pcp.io)
[![Static Binary](https://img.shields.io/badge/deploy-single%20static%20binary-3ddc97)]()
[![Air-gapped](https://img.shields.io/badge/network-air--gapped%20ready-8391ad)]()
[![License](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

🌐 **English** · [中文](docs/i18n/README.zh-CN.md) · [日本語](docs/i18n/README.ja.md) · [한국어](docs/i18n/README.ko.md) · [Deutsch](docs/i18n/README.de.md) · [Français](docs/i18n/README.fr.md) · [Español](docs/i18n/README.es.md) · [Português](docs/i18n/README.pt.md) · [Italiano](docs/i18n/README.it.md) · [Русский](docs/i18n/README.ru.md) · [ไทย](docs/i18n/README.th.md) · [Bahasa Indonesia](docs/i18n/README.id.md) · [Tiếng Việt](docs/i18n/README.vi.md)


</div>

---

```
Browser / CLI ── deltascope (one static Go binary)
                   ├─ A/B window regression diff over local history archives
                   ├─ 16-rule diagnosis engine → conclusion · evidence · next steps
                   ├─ zoomable trend charts (10 metric groups)
                   └─ everything embedded: UI, charts, credentials — zero external services
Collection: Performance Co-Pilot archives, ring retention (default 7 days)
```

<div align="center">

<img src="docs/preview-diff.svg" width="100%" alt="A/B regression report with diagnosis">
<sub>A/B regression report — diagnosis first, full data below</sub>

<br><br>

<img src="docs/preview-trend.svg" width="100%" alt="History trends">
<sub>History trends</sub>

</div>

<details>
<summary><b>More views (8)</b></summary>
<br>
<table>
<tr>
<td width="50%"><img src="docs/preview-login.svg" width="100%"><br><sub>Login</sub></td>
<td width="50%"><img src="docs/preview-diff-empty.svg" width="100%"><br><sub>Window presets</sub></td>
</tr>
<tr>
<td><img src="docs/preview-diff-network.svg" width="100%"><br><sub>Per-NIC rows</sub></td>
<td><img src="docs/preview-diff-disk-fs.svg" width="100%"><br><sub>Per-device & filesystem</sub></td>
</tr>
<tr>
<td><img src="docs/preview-diff-all.svg" width="100%"><br><sub>Full view & warnings</sub></td>
<td><img src="docs/preview-trend-fs.svg" width="100%"><br><sub>Filesystem trend</sub></td>
</tr>
<tr>
<td><img src="docs/preview-trend-mem-7d.svg" width="100%"><br><sub>7-day window & gaps</sub></td>
<td><img src="docs/preview-mobile.svg" width="60%"><br><sub>Mobile</sub></td>
</tr>
</table>
<sub><i>Faithful UI previews — replace with real screenshots after deployment.</i></sub>
</details>

## What it does

Pick two time windows — **baseline A** vs **suspect B** — and deltascope compares
per-metric averages from local history archives, judges every change against each
metric's polarity, and renders a three-layer report: **diagnosis → evidence → full data**.

## Features

- **Diagnosis rule engine** — 16 built-in cross-metric rules (swap spiral, disk
  saturation, accept-queue overflow, OOM, single-core hotspot, SYN pressure, reboot
  detection…). Each hit yields a plain-language conclusion, its evidence, and the
  next commands to run. No synthetic health scores — ever.
- **146 built-in metrics, 5 categories** — incl. PSI pressure, softnet drops,
  per-core hotspots (auto-folded), TCP state distribution, direct reclaim, LVM/MD.
  Per-metric noise thresholds override the global one.
- **Full-data report** — flat rows stay visible but dimmed, row order is stable,
  row tint scales with |Δ|, appeared ⊕ / vanished ⊖ marked distinctly, Top-5 anchors.
- **Everything is a config file** — catalog, rules, and thresholds are exportable
  JSON (`catalog export`, `rules export`), validated on load, swappable per run.
  `profiles/` ships full/core tiers.
- **Headless mode** — `deltascope compare` prints the same report as text (ANSI) or
  JSON and exits 2 on regressions: cron it, pipe it, alert on it.
- **Air-gapped by design** — one static binary, embedded UI and charts, local-only
  auth, no CDN, no telemetry, no outbound traffic of any kind.

## Quick start

Prebuilt static binaries (see [`dist/`](dist/)): `linux-amd64` (kernel ≥ 3.2),
`linux-arm64`, `linux-amd64-el6` (kernel 2.6.32).

Build from source (internet-connected dev box, once):

```bash
make vendor && make test && make build   # CGO_ENABLED=0 → ./deltascope
```

Deploy on the target (Rocky Linux 9 reference, fully offline OK):

```bash
# offline hosts: pre-download PCP RPMs on a matching online box
#   dnf download --resolve --alldeps pcp pcp-system-tools   → put into ./rpms/
RETENTION_DAYS=7 LISTEN_ADDR=0.0.0.0:8080 \
DSCOPE_ADMIN_USER=admin DSCOPE_ADMIN_PASS='a-strong-one' ./deploy.sh
```

deploy.sh installs collection, enables tiered sampling (hot 10s / warm 60s /
cold 5min), configures ring retention, creates the service user and a hardened
systemd unit.

## Usage

```bash
deltascope serve   -listen :8080 -archive DIR -data DIR [-catalog F] [-rules F] [-tls-cert F -tls-key F]
deltascope user    add|del|list <name>          # password via DSCOPE_PASSWORD or prompt
deltascope catalog export > catalog.json        # edit → serve -catalog catalog.json
deltascope rules   export > rules.json          # edit → serve -rules rules.json
deltascope compare -a-start 2026-07-09T14:00 -a-end 2026-07-09T15:00 \
                   -b-start 2026-07-10T14:00 -b-end 2026-07-10T15:00 \
                   [-format text|json] [-all] [-no-color]   # exit 2 on regression
```

## Diff semantics

- Counters are averaged **as rates** over each window (pmdiff semantics)
- Δ% = (B − A) / |A| × 100; judged only when `|Δ| ≥ threshold` (global default 15%,
  per-metric overrides for jittery counters)
- Polarity: `worse_up` (CPU, retransmits), `better_up` (available memory),
  `neutral` (throughput — flagged for attention only)
- A=0 → B≠0 reported as ∞; metrics absent on both sides are skipped silently —
  extend the catalog fearlessly
- Appeared ⊕ / vanished ⊖ are first-class events, separate from magnitude changes

## Security

PBKDF2-HMAC-SHA256 (600k iterations, per-user salt) · HMAC-signed stateless
sessions (HttpOnly, SameSite=Strict) · per-IP login rate limiting with
timing-flattened lookups · metric-name whitelisting + array-form exec (no shell,
ever) · strict CSP `default-src 'self'`, zero inline script/style · hardened
systemd unit (non-root, ProtectSystem=strict) · credentials never leave the host.

## Notes

Windows are interpreted in the server's local timezone · max window 32 days ·
trend step auto-adapts (10s–15m, ~600 points) · charts vendored (Apache-2.0) ·
first-day deployments need archives to accumulate before cross-day comparisons.

<div align="center">

`Δ` — <i>because regressions should be visible on the first sweep.</i>

</div>
