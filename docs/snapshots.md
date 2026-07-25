# Snapshots & Process Accounting

`deltascope serve` captures a full state snapshot every 10 minutes on its
own -- no cron job to install. Each snapshot holds ~1700 facts about the
machine plus per-process CPU and memory, read directly from `/proc`.

## Why /proc and not PCP hotproc

Process accounting used to require enabling PCP's hotproc PMDA, editing a
predicate config, reconfiguring pmlogger, and then waiting a day for the
archive to fill. That is four manual steps and a delay before the feature
does anything.

Reading `/proc/<pid>/stat` directly -- the same source `ps` and `top` use
-- removes all of it. Data exists from the first capture onward, costs
about as much as running `ps`, needs no privileges beyond reading /proc,
and never leaves the machine.

## What gets recorded

Whitelisted services (mysqld, nginx, java, postgres, redis, php-fpm,
dockerd, and friends) are always recorded so a core service always has
history even while idle. Beyond those, the 40 heaviest userspace
processes by memory + CPU are kept. Kernel threads are excluded: their
cost belongs to the system-level metrics, and nobody acts on "kworker
used two ticks".

CPU is stored as cumulative ticks, so a single snapshot is not meaningful
on its own -- a rate needs two readings. That is why process comparison
takes two snapshots per window, and why the process section is excluded
from the generic change diff (a cumulative counter always differs, and
would otherwise report every process as "modified" every ten minutes).

## Retention

Snapshots are pruned to 7 days by default. At 10-minute intervals that is
roughly 1000 snapshots; each is a compressed JSON blob in the same SQLite
file the rest of deltascope uses.

## Manual capture

The scheduler runs inside `serve`, but the CLI works standalone too:

    deltascope snapshot -data /var/lib/deltascope
    deltascope proc-diff -data /var/lib/deltascope -since 24h
    deltascope statediff -data /var/lib/deltascope -since 24h
