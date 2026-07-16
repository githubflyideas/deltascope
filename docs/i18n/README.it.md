<div align="center">

<img src="../logo.svg" alt="deltascope" width="96">

# deltascope

**Oscilloscopio di regressione delle prestazioni per un host Linux — con il parere del medico**

🌐 [English](../../README.md) · [中文](README.zh-CN.md) · [日本語](README.ja.md) · [한국어](README.ko.md) · [Deutsch](README.de.md) · [Français](README.fr.md) · [Español](README.es.md) · [Português](README.pt.md) · **Italiano** · [Русский](README.ru.md) · [ไทย](README.th.md) · [Bahasa Indonesia](README.id.md) · [Tiếng Việt](README.vi.md)


</div>

---

<div align="center">
<img src="../preview-diff.svg" width="100%" alt="A/B report">
<br><br>
<img src="../preview-trend.svg" width="100%" alt="Trends">
<br><sub>Altre viste nel README inglese</sub>
</div>

## Cosa fa

Scegli due finestre temporali — **baseline A** e **sospetta B** — e deltascope confronta le medie per metrica dagli archivi storici locali, giudica ogni variazione secondo la polarità della metrica e produce un report a tre livelli: **diagnosi → prove → dati completi**.

## Caratteristiche

- **Motore di regole diagnostiche** — 16 regole integrate tra metriche (spirale di swap, saturazione disco, overflow della coda accept, OOM, hotspot single-core, pressione SYN, rilevamento riavvio…). Ogni match produce una conclusione in linguaggio semplice, le prove e i comandi successivi. Mai punteggi sintetici di salute.
- **146 metriche, 5 categorie** — PSI, drop softnet, hotspot per core (raggruppamento automatico), distribuzione stati TCP, direct reclaim, LVM/MD. Soglie dedicate per i contatori rumorosi.
- **Report a dati completi** — le righe stabili restano visibili ma attenuate, ordine stabile, tinta ∝ |Δ|, comparsa ⊕ / scomparsa ⊖ distinte, ancore Top-5.
- **Tutto è configurazione** — catalogo, regole e soglie in JSON esportabile, validato al caricamento. `profiles/` offre full/core.
- **Modalità headless** — `deltascope compare` stampa lo stesso report in testo (ANSI) o JSON, exit code 2 in caso di regressione: pronto per cron.
- **Progettato per l'air-gap** — un binario statico, UI e grafici incorporati, autenticazione locale, niente CDN, niente telemetria, zero traffico in uscita.

## Avvio rapido

Binari precompilati in [`dist/`](../../dist/): `linux-amd64` (kernel ≥ 3.2), `linux-arm64`, `linux-amd64-el6` (kernel 2.6.32).

Compilazione (una volta, con internet):

```bash
make vendor && make test && make build
```

Deployment (riferimento Rocky Linux 9, offline completo possibile):

```bash
RETENTION_DAYS=7 LISTEN_ADDR=0.0.0.0:8080 \
DSCOPE_ADMIN_USER=admin DSCOPE_ADMIN_PASS='...' ./deploy.sh
```

## Utilizzo

```bash
deltascope serve   -listen :8080 -archive DIR -data DIR [-catalog F] [-rules F]
deltascope user    add|del|list <name>
deltascope catalog export > catalog.json
deltascope rules   export > rules.json
deltascope compare -a-start 2026-07-09T14:00 -a-end 2026-07-09T15:00 \
                   -b-start 2026-07-10T14:00 -b-end 2026-07-10T15:00 \
                   [-format text|json] [-all] [-no-color]
```

## Semantica del diff

- I contatori sono mediati **come tassi** per finestra (semantica pmdiff)
- Δ% = (B − A) / |A| × 100; giudicato solo se `|Δ| ≥ soglia` (15 % globale, override per metrica)
- Polarità: `worse_up` / `better_up` / `neutral`
- A=0 → B≠0 è riportato come ∞; le metriche assenti da entrambi i lati vengono saltate in silenzio
- Comparsa ⊕ / scomparsa ⊖ sono eventi di prima classe

## Sicurezza

PBKDF2-HMAC-SHA256 (600k iterazioni) · sessioni stateless firmate HMAC · rate limiting del login per IP · whitelist dei nomi metrica + exec ad array (mai shell) · CSP severa senza inline · unità systemd indurita · le credenziali non lasciano mai l'host.

## Note

Finestre nel fuso orario locale del server · max 32 giorni per finestra · passo del trend adattivo · grafici inclusi (Apache-2.0) · il primo giorno gli archivi devono accumularsi.
