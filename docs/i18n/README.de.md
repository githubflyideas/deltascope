<div align="center">

<img src="../logo.svg" alt="deltascope" width="96">

# deltascope

**Performance-Regressions-Oszilloskop für einen einzelnen Linux-Host — mit ärztlichem Befund**

🌐 [English](../../README.md) · [中文](README.zh-CN.md) · [日本語](README.ja.md) · [한국어](README.ko.md) · **Deutsch** · [Français](README.fr.md) · [Español](README.es.md) · [Português](README.pt.md) · [Italiano](README.it.md) · [Русский](README.ru.md) · [ไทย](README.th.md) · [Bahasa Indonesia](README.id.md) · [Tiếng Việt](README.vi.md)


</div>

---

<div align="center">
<img src="../preview-diff.svg" width="100%" alt="A/B report">
<br><br>
<img src="../preview-trend.svg" width="100%" alt="Trends">
<br><sub>Weitere Ansichten in der englischen README</sub>
</div>

## Was es tut

Wählen Sie zwei Zeitfenster — **Baseline A** und **Verdacht B** — und deltascope vergleicht die Fenstermittelwerte aller Metriken aus lokalen Verlaufsarchiven, bewertet jede Änderung anhand der Metrik-Polarität und erzeugt einen dreischichtigen Bericht: **Diagnose → Belege → Volldaten**.

## Funktionen

- **Diagnose-Regelwerk** — 16 eingebaute metrikübergreifende Regeln (Swap-Spirale, Platten-Sättigung, Accept-Queue-Überlauf, OOM, Einzelkern-Hotspot, SYN-Druck, Reboot-Erkennung…). Jeder Treffer liefert Klartext-Fazit, Belege und die nächsten Befehle. Keine synthetischen Health-Scores.
- **146 Metriken, 5 Kategorien** — inkl. PSI, Softnet-Drops, Pro-Kern-Hotspots (automatisch gefaltet), TCP-Zustandsverteilung, Direct Reclaim, LVM/MD. Rauschige Zähler haben eigene Schwellwerte.
- **Volldaten-Bericht** — unveränderte Zeilen bleiben gedimmt sichtbar, stabile Zeilenreihenfolge, Zeilentönung ∝ |Δ|, Neu ⊕ / Verschwunden ⊖ getrennt markiert, Top-5-Anker.
- **Alles ist Konfiguration** — Katalog, Regeln und Schwellwerte sind exportierbares JSON, beim Laden validiert. `profiles/` liefert full/core.
- **Headless-Modus** — `deltascope compare` gibt denselben Bericht als Text (ANSI) oder JSON aus, Exit-Code 2 bei Regression: cron-fähig.
- **Air-gapped by design** — ein statisches Binary, eingebettete UI und Charts, lokale Auth, kein CDN, keine Telemetrie, kein ausgehender Verkehr.

## Schnellstart

Vorgefertigte Binaries in [`dist/`](../../dist/): `linux-amd64` (Kernel ≥ 3.2), `linux-arm64`, `linux-amd64-el6` (Kernel 2.6.32).

Aus dem Quellcode (einmalig, mit Internet):

```bash
make vendor && make test && make build
```

Deployment (Referenz Rocky Linux 9, vollständig offline möglich):

```bash
RETENTION_DAYS=7 LISTEN_ADDR=0.0.0.0:8080 \
DSCOPE_ADMIN_USER=admin DSCOPE_ADMIN_PASS='...' ./deploy.sh
```

## Verwendung

```bash
deltascope serve   -listen :8080 -archive DIR -data DIR [-catalog F] [-rules F]
deltascope user    add|del|list <name>
deltascope catalog export > catalog.json
deltascope rules   export > rules.json
deltascope compare -a-start 2026-07-09T14:00 -a-end 2026-07-09T15:00 \
                   -b-start 2026-07-10T14:00 -b-end 2026-07-10T15:00 \
                   [-format text|json] [-all] [-no-color]
```

## Diff-Semantik

- Zähler werden pro Fenster als **Raten gemittelt** (pmdiff-Semantik)
- Δ% = (B − A) / |A| × 100; bewertet nur bei `|Δ| ≥ Schwellwert` (global 15 %, metrikspezifische Overrides)
- Polarität: `worse_up` / `better_up` / `neutral`
- A=0 → B≠0 wird als ∞ gemeldet; beidseitig fehlende Metriken werden still übersprungen
- Neu ⊕ / Verschwunden ⊖ sind eigenständige Ereignisse

## Sicherheit

PBKDF2-HMAC-SHA256 (600k Iterationen) · HMAC-signierte zustandslose Sessions · Login-Ratenbegrenzung pro IP · Metriknamen-Whitelist + Array-exec (nie über eine Shell) · strikte CSP ohne Inline-Code · gehärtete systemd-Unit · Zugangsdaten verlassen den Host nie.

## Hinweise

Zeitfenster in der lokalen Zeitzone des Servers · Fenster max. 32 Tage · Trend-Schrittweite adaptiv · Charts gebündelt (Apache-2.0) · am ersten Tag müssen sich Archive erst ansammeln.
