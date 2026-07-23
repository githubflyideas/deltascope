# Catalog Profiles

- catalog-full.json — the full built-in catalog (matches what's embedded in the binary)
- catalog-core.json — a slim profile: drops per-core / ICMP / IP-layer / LVM / MD and
  other high-cardinality, deep-water metrics; good for low-spec hosts or a
  main-signal-only view

Usage: `deltascope serve -catalog profiles/catalog-core.json`

Customizing: copy either file and edit. `polarity` is one of
`worse_up | better_up | neutral`; `fold: true` means the frontend
collapses that metric's instances by default.

Diagnosis rules work the same way: `deltascope rules export > rules.json`,
edit, then `serve -rules rules.json`. Metrics absent from the archive are
skipped automatically, so it's safe to add generously.
