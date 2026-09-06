# deltascope

**Whole-machine change & performance diff for Linux.**

A single static Go binary. Point it at two moments in time and it tells you
what's different — not a wall of dashboards, a diagnosis.

## One click

The **Diagnose** tab runs all four engines against the last hour versus the
same hour yesterday, correlates them, and answers in one sentence:

> 🔴 **CPU is degraded: user CPU +2733%**
> Responsible — `mysqld` at 310% of a core (+7650%)
> Changed — `kernel.sched_migration_cost_ns` 500000 → 5000000
> Next — `mpstat -P ALL 1 5` · `pidstat 1 5`

Which resource, which process, what changed, what to run — full detail from
each engine folds out underneath.

## Four engines

- **Regression diff** — 146 performance metrics between two windows, turned
  into plain-language conclusions. A dual-significance floor (relative %
  *and* an absolute minimum) keeps a quiet machine from lighting up on noise.
- **Change accounting** (`statediff` / `verify`) — snapshots ~1700 machine
  facts (sysctl, packages, modules, ports, firewall, services, config
  fingerprints) and diffs two points in time. `verify` is a release gate.
- **Process accounting** — per-process CPU/memory with restart detection,
  read straight from `/proc`.
- **Reasoning chain** — 78 named states, then 58 diagnoses over combinations
  of them *including negation*: what is deliberately absent separates
  hypervisor contention from an ordinary busy guest. Thresholds scale with
  the machine, and peaks are read alongside means.

A triage dashboard organizes it as CPU / memory / disk / network, plus a
fifth "software gremlin" block for process and config changes.

## PCP is optional

Two of the four engines read PCP archives; the other two read `/proc` and
config files directly and need nothing installed. Without PCP, deltascope
starts anyway, says so in the log, and the web UI dims the tabs it cannot
serve instead of handing you a broken chart.

| Engine | Needs PCP |
| --- | --- |
| Change accounting | no |
| Process accounting | no |
| Regression diff · Trends · Reasoning chain | yes |
| Diagnose | runs either way, with two legs instead of three |

So on a plain Ubuntu box with no pmlogger you still get drift detection and
per-process accounting on the first run. Install PCP when you want metric
comparison as well:

```bash
# Debian / Ubuntu
sudo apt install -y pcp pcp-system-tools
# RHEL / Rocky / Alma / CentOS Stream / Fedora
sudo dnf install -y pcp pcp-system-tools    # older: yum install
# openSUSE / SLES
sudo zypper install -y pcp pcp-system-tools
# Arch
sudo pacman -S pcp

sudo systemctl enable --now pmcd pmlogger   # all distros
```

Detection happens once at startup, so restart deltascope afterwards.
`deploy.sh` does all of this plus a tuned sampling config, a systemd
service, and a locked-down user — recommended for a real deployment.

## Install deltascope

```bash
curl -L -o deltascope https://github.com/githubflyideas/deltascope/raw/main/dist/deltascope-linux-amd64
chmod +x deltascope && sudo mv deltascope /usr/local/bin/
```

ARM64 is in [`dist/`](dist/). Verify with `sha256sum -c dist/SHA256SUMS`.

## Quick start

```bash
deltascope serve -listen 0.0.0.0:8080 -data /var/lib/deltascope
```

Open it in a browser; the login page creates the admin account on first
visit. State is snapshotted every 10 minutes, so history accumulates with
no cron job. The UI ships in ten languages and six themes, and any report
exports as JSON.
<img width="473" height="647" alt="image" src="https://github.com/user-attachments/assets/f38fca46-e2d6-4412-b07f-7c0c4420601e" />

if you forget passwd 
 ./deltascope user del  admin
 add admin in web


## Command line

Everything the UI does is available headless, exiting non-zero on a finding
so it drops into cron or CI:

```bash
deltascope compare   -a-start ... -b-start ...   # metric regression, exit 2
deltascope statediff -since 24h                  # config changes, exit 3
deltascope proc-diff -since 24h                  # per-process CPU/mem, exit 3
deltascope verify start -name deploy-42          # baseline, deploy, then:
deltascope verify report -name deploy-42 -format md
deltascope catalog export > catalog.json         # tune, load with -catalog
deltascope rules export   > rules.json           # tune, load with -rules
```

## Design

- **Offline-first** — no telemetry, archives read locally.
- **Single static binary**, amd64/arm64, no runtime dependencies.
- **No invented numbers** — where a percentage would divide by an idle
  baseline, absolute values are shown instead; weaker-basis figures are
  labelled, not presented as measured.
- **A number and its scale travel together** — one saturated core is
  1000 ms/s on any host, so each condition picks its own threshold form.
- **Customizable** — metric catalog and diagnosis rules are external JSON;
  [`profiles/`](profiles/) ships full and slim presets.

## License

Apache 2.0
