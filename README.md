# deltascope

**Whole-machine change & performance diff for Linux, built on PCP.**

A single static Go binary. No internet dependency, minimal external
requirements. Point it at two moments in time and it tells you what's
different — not a wall of dashboards, a diagnosis.

## One click

The **Diagnose** tab runs all three engines at once against the last hour
versus the same hour yesterday, correlates them, and answers in one
sentence:

> 🔴 **CPU is degraded: user CPU +2733%**
> Responsible — `mysqld` at 310% of a core (+7650%)
> Changed — `kernel.sched_migration_cost_ns` 500000 → 5000000
> Next — `mpstat -P ALL 1 5` · `pidstat 1 5`

Which resource, which process, what changed, what to run. Full detail from
each engine folds out underneath.

## Why

Most incidents are caused by a change. The usual question after "it's
slow" is "what changed since it wasn't". deltascope answers that
directly, across three layers:

- **Regression diff** — compare performance metrics between two windows
  (146 built-in metrics: CPU, memory, disk, filesystem, network), with a
  rule engine that turns raw deltas into plain-language conclusions
  ("swap is active and available memory is falling — memory pressure")
  plus next steps to run. A dual-significance floor (relative % *and* an
  absolute minimum) keeps a quiet machine from lighting up over noise —
  0.005 → 0.097 on an idle metric is "+1840%" by percentage alone but
  isn't a regression, and doesn't get reported as one.
- **Change accounting** (`statediff` / `verify`) — snapshot ~1700 facts
  about the machine (sysctl, packages, kernel modules, routes, listening
  ports, firewall, mounts, services, cron, config file fingerprints,
  security posture) and diff two points in time, showing only what
  changed. `verify` turns this into a release tool: baseline before a
  deploy, report after, paste the Markdown into a PR.
- **Process accounting** — per-process CPU/memory between two windows with
  restart detection, so "today mysqld pegged the CPU and nginx ate 90% of
  memory, yesterday both were idle" is one command away. Read directly
  from `/proc` into the local snapshot store: no PCP hotproc PMDA, no
  pmlogger configuration, no waiting for an archive to fill. A process
  that rose from an idle baseline is reported on absolute evidence rather
  than as an infinite percentage, and one that started mid-window still
  gets a CPU figure — labelled `~` because it is a lifetime average, not a
  measured rate.
- **Reasoning chain** — 59 named states (`state.cpu.core_pegged`,
  `state.mem.oom_killing`, `state.net.listen_dropping`) evaluated
  independently, then 39 diagnoses matched over combinations of them
  including negation. The negation is the point: hypervisor CPU
  contention and an ordinary busy guest both show a long run queue, and
  what separates them is that under contention the guest's own userspace
  is *not* the thing consuming CPU. Every state is reported whether or not
  it held, so a conclusion that hinges on something being absent can be
  audited. Thresholds scale with the machine where the underlying metric
  is a whole-machine sum, and peaks are read alongside means so a core
  saturated for 20 minutes of an hour isn't averaged into invisibility.

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

ARM64 is in [`dist/`](dist/) too. Verify with `sha256sum -c dist/SHA256SUMS`.

The host needs PCP installed (`pcp` + `pcp-system-tools`). `deploy.sh`
handles that plus a tiered sampling config, a systemd service, and a
locked-down user.

## Quick start

```bash
deltascope serve -listen 0.0.0.0:8080 -data /var/lib/deltascope
```

Open the address in a browser. There's no account yet, so the login page
offers to create the admin account right there — no separate CLI step,
no `-data` path to keep in sync between two commands. Sign in, pick two
time windows, run a comparison. The server captures a state snapshot
every 10 minutes on its own, so process and change accounting start
accumulating history immediately, no cron job required.

The web UI is available in English, 中文, Español, Français, Português,
Deutsch, Русский, 日本語, 한국어, and Bahasa Indonesia, with six colour
themes (three families, each in a dark and light variant). Any report you
run can be exported as a single JSON file — useful for handing raw
comparison data to a colleague, or to an AI, without re-deriving it from
the UI.

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
- **Single binary.** Static builds for amd64 and arm64. No runtime
  dependencies.
- **Conclusions over data.** A diagnosis rule engine and a triage
  dashboard sit on top of the raw metrics, because a wall of graphs
  isn't an answer.
- **A number and its scale travel together.** One saturated core is
  1000 ms/s whether the host has 4 cores or 64, so a threshold written as
  a fraction of the whole machine cannot see it — and a threshold written
  as a bare number is wrong on one class of host by construction. Which
  form a given condition uses is decided per condition, not globally.
- **No invented numbers.** Where a percentage change would require
  dividing by an idle baseline, none is shown: the absolute values are
  reported instead, and the row is judged on those. A figure derived from
  a weaker basis than the usual one is labelled as such rather than
  presented as measured.
- **Own the display layer.** PCP reports whatever it's configured to
  collect, loopback traffic and snap-package loop devices included —
  deciding what's worth showing is deltascope's job, not PCP's. Known
  structural noise (loopback interfaces, `/dev/loopN` filesystems) is
  filtered before it reaches a report.
- **Customizable, not hardcoded.** The metric catalog and diagnosis
  rules are both external JSON — `deltascope catalog export` /
  `deltascope rules export`, edit, reload with `-catalog` / `-rules`.
  Metrics absent from an archive are skipped silently.

## Command line

Everything the web UI does is available headless, and exits non-zero when
it finds something, so it drops into cron or CI without a wrapper:

```bash
deltascope compare   -a-start ... -b-start ...   # metric regression, exit 2 on worse
deltascope statediff -since 24h                  # config/environment changes, exit 3
deltascope proc-diff -since 24h                  # per-process CPU/memory, exit 3
deltascope verify start -name deploy-42           # baseline, then deploy, then:
deltascope verify report -name deploy-42 -format md
deltascope catalog export > catalog.json          # tune metrics/floors, load with -catalog
deltascope rules export   > rules.json            # tune diagnosis rules, load with -rules
deltascope user add <name> -data ...              # create/reset an account from the CLI, if needed
```

[`profiles/`](profiles/) ships full and slim catalog presets.

## License

Apache 2.0
