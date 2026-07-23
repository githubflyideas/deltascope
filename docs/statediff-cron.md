# Change Accounting · Daily Watch

deltascope snapshots the whole machine's state on a schedule and diffs
"what changed since yesterday" for you. Local only, no external
dependencies, read-only collection.

## What it collects

System identity · kernel parameters (sysctl) · packages · kernel modules ·
network config · listening ports · firewall · storage mounts · LVM/RAID ·
service status · scheduled tasks · config file fingerprints · security
posture. A single snapshot captures ~1700+ facts. Items that are
unreadable or whose tool is missing are skipped gracefully without
failing the rest.

## Install

Snapshot every hour, retain 7 days:

    0 * * * * root /usr/local/bin/deltascope snapshot -data /var/lib/deltascope -quiet

Write the last 24 hours of changes to a log every morning at 8:

    0 8 * * * root /usr/local/bin/deltascope statediff -data /var/lib/deltascope -since 24h -no-color >> /var/log/deltascope-changes.log 2>&1

Hook into alerting (exit code 3 when there are changes):

    */30 * * * * root /usr/local/bin/deltascope statediff -data /var/lib/deltascope -since 30m -summary || /usr/local/bin/notify "$(hostname) has state changes"

## Manual use

    deltascope snapshot                      # capture one now
    deltascope statediff -since 24h          # compare against 24h ago
    deltascope statediff -a 2026-07-20T14:00 -b 2026-07-21T14:00

Exit code 3 means changes were detected, 0 means state is identical --
handy for scripting.
