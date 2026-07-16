<div align="center">

<img src="../logo.svg" alt="deltascope" width="96">

# deltascope

**Oscilloscope de régression de performance pour un hôte Linux — avec l'avis du médecin**

🌐 [English](../../README.md) · [中文](README.zh-CN.md) · [日本語](README.ja.md) · [한국어](README.ko.md) · [Deutsch](README.de.md) · **Français** · [Español](README.es.md) · [Português](README.pt.md) · [Italiano](README.it.md) · [Русский](README.ru.md) · [ไทย](README.th.md) · [Bahasa Indonesia](README.id.md) · [Tiếng Việt](README.vi.md)


</div>

---

<div align="center">
<img src="../preview-diff.svg" width="100%" alt="A/B report">
<br><br>
<img src="../preview-trend.svg" width="100%" alt="Trends">
<br><sub>Plus de vues dans le README anglais</sub>
</div>

## Ce qu'il fait

Choisissez deux fenêtres temporelles — **référence A** et **suspecte B** — et deltascope compare les moyennes par métrique issues des archives locales, juge chaque écart selon la polarité de la métrique et produit un rapport à trois niveaux : **diagnostic → preuves → données complètes**.

## Fonctionnalités

- **Moteur de règles de diagnostic** — 16 règles inter-métriques intégrées (spirale de swap, saturation disque, débordement de la file accept, OOM, point chaud mono-cœur, pression SYN, détection de redémarrage…). Chaque déclenchement fournit une conclusion en clair, ses preuves et les commandes suivantes. Jamais de score de santé synthétique.
- **146 métriques, 5 catégories** — PSI, pertes softnet, points chauds par cœur (repli automatique), distribution des états TCP, direct reclaim, LVM/MD. Seuils dédiés pour les compteurs bruyants.
- **Rapport en données complètes** — les lignes stables restent visibles mais atténuées, ordre des lignes stable, teinte ∝ |Δ|, apparition ⊕ / disparition ⊖ distinguées, ancres Top-5.
- **Tout est fichier de configuration** — catalogue, règles et seuils en JSON exportable, validé au chargement. `profiles/` fournit full/core.
- **Mode headless** — `deltascope compare` imprime le même rapport en texte (ANSI) ou JSON, code de sortie 2 en cas de régression : prêt pour cron.
- **Conçu pour l'air-gap** — un binaire statique, UI et graphiques embarqués, auth locale, pas de CDN, pas de télémétrie, aucun trafic sortant.

## Démarrage rapide

Binaires précompilés dans [`dist/`](../../dist/) : `linux-amd64` (noyau ≥ 3.2), `linux-arm64`, `linux-amd64-el6` (noyau 2.6.32).

Compilation (machine connectée, une fois) :

```bash
make vendor && make test && make build
```

Déploiement (référence Rocky Linux 9, hors-ligne complet possible) :

```bash
RETENTION_DAYS=7 LISTEN_ADDR=0.0.0.0:8080 \
DSCOPE_ADMIN_USER=admin DSCOPE_ADMIN_PASS='...' ./deploy.sh
```

## Utilisation

```bash
deltascope serve   -listen :8080 -archive DIR -data DIR [-catalog F] [-rules F]
deltascope user    add|del|list <name>
deltascope catalog export > catalog.json
deltascope rules   export > rules.json
deltascope compare -a-start 2026-07-09T14:00 -a-end 2026-07-09T15:00 \
                   -b-start 2026-07-10T14:00 -b-end 2026-07-10T15:00 \
                   [-format text|json] [-all] [-no-color]
```

## Sémantique du diff

- Les compteurs sont moyennés **en taux** par fenêtre (sémantique pmdiff)
- Δ% = (B − A) / |A| × 100 ; jugé seulement si `|Δ| ≥ seuil` (15 % global, seuils par métrique)
- Polarité : `worse_up` / `better_up` / `neutral`
- A=0 → B≠0 rapporté ∞ ; les métriques absentes des deux côtés sont ignorées silencieusement
- Apparition ⊕ / disparition ⊖ sont des événements à part entière

## Sécurité

PBKDF2-HMAC-SHA256 (600k itérations) · sessions sans état signées HMAC · limitation de connexion par IP · liste blanche des noms de métriques + exec en tableau (jamais de shell) · CSP stricte sans inline · unité systemd durcie · les identifiants ne quittent jamais l'hôte.

## Notes

Fenêtres interprétées dans le fuseau local du serveur · fenêtre max 32 jours · pas de tendance adaptatif · graphiques embarqués (Apache-2.0) · le premier jour, laisser les archives s'accumuler.
