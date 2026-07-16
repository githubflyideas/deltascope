<div align="center">

<img src="../logo.svg" alt="deltascope" width="96">

# deltascope

**Osiloskop regresi performa untuk satu host Linux — dengan catatan dokter**

🌐 [English](../../README.md) · [中文](README.zh-CN.md) · [日本語](README.ja.md) · [한국어](README.ko.md) · [Deutsch](README.de.md) · [Français](README.fr.md) · [Español](README.es.md) · [Português](README.pt.md) · [Italiano](README.it.md) · [Русский](README.ru.md) · [ไทย](README.th.md) · **Bahasa Indonesia** · [Tiếng Việt](README.vi.md)


</div>

---

<div align="center">
<img src="../preview-diff.svg" width="100%" alt="A/B report">
<br><br>
<img src="../preview-trend.svg" width="100%" alt="Trends">
<br><sub>Tampilan lain ada di README bahasa Inggris</sub>
</div>

## Apa fungsinya

Pilih dua jendela waktu — **baseline A** dan **tersangka B** — dan deltascope membandingkan rata-rata tiap metrik dari arsip riwayat lokal, menilai setiap perubahan berdasarkan polaritas metrik, lalu menghasilkan laporan tiga lapis: **diagnosis → bukti → data lengkap**.

## Fitur

- **Mesin aturan diagnosis** — 16 aturan lintas-metrik bawaan (spiral swap, saturasi disk, overflow antrean accept, OOM, hotspot satu core, tekanan SYN, deteksi reboot…). Setiap kecocokan memberi kesimpulan bahasa manusia, bukti, dan perintah berikutnya. Tidak pernah ada skor kesehatan sintetis.
- **146 metrik bawaan, 5 kategori** — termasuk PSI, softnet drop, hotspot per-core (dilipat otomatis), distribusi status TCP, direct reclaim, LVM/MD. Ambang khusus untuk counter yang berisik.
- **Laporan data penuh** — baris stabil tetap terlihat namun diredupkan, urutan baris stabil, warna latar ∝ |Δ|, muncul ⊕ / hilang ⊖ ditandai terpisah, jangkar Top-5.
- **Semuanya file konfigurasi** — katalog, aturan, dan ambang berupa JSON yang bisa diekspor, divalidasi saat dimuat. `profiles/` menyediakan full/core.
- **Mode headless** — `deltascope compare` mencetak laporan yang sama sebagai teks (ANSI) atau JSON, kode keluar 2 saat ada regresi: siap untuk cron.
- **Dirancang untuk air-gap** — satu binary statis, UI dan grafik tertanam, autentikasi lokal, tanpa CDN, tanpa telemetri, tanpa lalu lintas keluar.

## Mulai cepat

Binary siap pakai di [`dist/`](../../dist/): `linux-amd64` (kernel ≥ 3.2), `linux-arm64`, `linux-amd64-el6` (kernel 2.6.32).

Bangun dari sumber (sekali, mesin ber-internet):

```bash
make vendor && make test && make build
```

Deployment (referensi Rocky Linux 9, bisa offline penuh):

```bash
RETENTION_DAYS=7 LISTEN_ADDR=0.0.0.0:8080 \
DSCOPE_ADMIN_USER=admin DSCOPE_ADMIN_PASS='...' ./deploy.sh
```

## Penggunaan

```bash
deltascope serve   -listen :8080 -archive DIR -data DIR [-catalog F] [-rules F]
deltascope user    add|del|list <name>
deltascope catalog export > catalog.json
deltascope rules   export > rules.json
deltascope compare -a-start 2026-07-09T14:00 -a-end 2026-07-09T15:00 \
                   -b-start 2026-07-10T14:00 -b-end 2026-07-10T15:00 \
                   [-format text|json] [-all] [-no-color]
```

## Semantik diff

- Counter dirata-rata **sebagai laju** per jendela (semantik pmdiff)
- Δ% = (B − A) / |A| × 100; dinilai hanya bila `|Δ| ≥ ambang` (global 15%, override per-metrik)
- Polaritas: `worse_up` / `better_up` / `neutral`
- A=0 → B≠0 dilaporkan sebagai ∞; metrik yang absen di kedua sisi dilewati diam-diam
- Muncul ⊕ / hilang ⊖ adalah peristiwa kelas satu

## Keamanan

PBKDF2-HMAC-SHA256 (600k iterasi) · sesi stateless bertanda tangan HMAC · pembatasan login per-IP · whitelist nama metrik + exec array (tak pernah lewat shell) · CSP ketat tanpa inline · unit systemd yang diperkeras · kredensial tidak pernah meninggalkan host.

## Catatan

Jendela ditafsirkan pada zona waktu lokal server · jendela maks 32 hari · langkah tren adaptif · grafik disertakan (Apache-2.0) · hari pertama perlu menunggu arsip terkumpul.
