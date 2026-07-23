# deltascope

**Whole-machine change & performance diff for Linux, built on PCP.**

A single static Go binary. No internet dependency, minimal external
requirements. Point it at two moments in time and it tells you what's
different — not a wall of dashboards, a diagnosis.

## Why

Most incidents are caused by a change. The usual question after "it's
slow" is "what changed since it wasn't". deltascope answers that
directly, across three layers:

- **Regression diff** — compare performance metrics between two windows
  (146 built-in metrics: CPU, memory, disk, filesystem, network), with a
  rule engine that turns raw deltas into plain-language conclusions
  ("swap is active and available memory is falling — memory pressure")
  plus next steps to run.
- **Change accounting** (`statediff` / `verify`) — snapshot ~1700 facts
  about the machine (sysctl, packages, kernel modules, routes, listening
  ports, firewall, mounts, services, cron, config file fingerprints,
  security posture) and diff two points in time, showing only what
  changed. `verify` turns this into a release tool: baseline before a
  deploy, report after, paste the Markdown into a PR.
- **Process accounting** — per-process CPU/memory accounting between two
  windows, with restart detection, so "today mysqld pegged the CPU and
  nginx ate 90% of memory, yesterday both were idle" is one command away.

A triage dashboard on top organizes all of this the way an engineer
actually thinks about a machine: CPU / memory / disk / network, plus a
fifth block for "the software gremlin" — process and configuration
changes that hardware counters alone won't show you.

## Install

```bash
curl -L -o deltascope https://github.com/githubflyideas/deltascope/raw/main/dist/deltascope-linux-amd64
chmod +x deltascope
sudo mv deltascope /usr/local/bin/
```

ARM64 and CentOS/RHEL 6 builds are in [`dist/`](dist/). Verify with
`sha256sum -c dist/SHA256SUMS`.

The host needs PCP installed (`pcp` + `pcp-system-tools`). `deploy.sh`
handles that plus a tiered sampling config, a systemd service, and a
locked-down user.

## Quick start

```bash
sudo DSCOPE_PASSWORD='a-strong-password' deltascope user add admin -data /var/lib/deltascope
deltascope serve -listen 0.0.0.0:8080 -data /var/lib/deltascope
```

Open the address in a browser, sign in, pick two time windows, run a
comparison.

For change accounting without the web UI:

```bash
deltascope snapshot -data /var/lib/deltascope         # capture current state
deltascope statediff -data /var/lib/deltascope -since 24h   # diff against 24h ago
```

For release verification:

```bash
deltascope verify start -name deploy-2026w30
./your-deploy.sh
deltascope verify report -name deploy-2026w30 -format md > impact.md
```

`verify report` and `statediff` exit with code 3 when changes are
detected — useful for CI gating and cron alerting.

## Design

- **Offline-first.** No telemetry, no external services. PCP archives
  live on the host; deltascope reads them locally.
- **Single binary.** Static builds for amd64, arm64, and el6 (Go 1.23,
  for CentOS 6's ancient kernel). No runtime dependencies.
- **Conclusions over data.** A diagnosis rule engine and a triage
  dashboard sit on top of the raw metrics, because a wall of graphs
  isn't an answer.
- **Customizable, not hardcoded.** The metric catalog and diagnosis
  rules are both external JSON — `deltascope catalog export` /
  `deltascope rules export`, edit, reload with `-catalog` / `-rules`.
  Metrics absent from an archive are skipped silently.

## More

- [`docs/hotproc.config`](docs/hotproc.config) — enabling per-process
  accounting
- [`docs/statediff-cron.md`](docs/statediff-cron.md) — scheduled change
  watching
- [`docs/verify.md`](docs/verify.md) — release impact verification
- [`profiles/`](profiles/) — full and slim metric catalog presets

## License

MIT
