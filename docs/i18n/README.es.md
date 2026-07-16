<div align="center">

<img src="../logo.svg" alt="deltascope" width="96">

# deltascope

**Osciloscopio de regresiones de rendimiento para un host Linux — con notas del médico**

🌐 [English](../../README.md) · [中文](README.zh-CN.md) · [日本語](README.ja.md) · [한국어](README.ko.md) · [Deutsch](README.de.md) · [Français](README.fr.md) · **Español** · [Português](README.pt.md) · [Italiano](README.it.md) · [Русский](README.ru.md) · [ไทย](README.th.md) · [Bahasa Indonesia](README.id.md) · [Tiếng Việt](README.vi.md)


</div>

---

<div align="center">
<img src="../preview-diff.svg" width="100%" alt="A/B report">
<br><br>
<img src="../preview-trend.svg" width="100%" alt="Trends">
<br><sub>Más vistas en el README en inglés</sub>
</div>

## Qué hace

Elija dos ventanas de tiempo — **línea base A** y **sospechosa B** — y deltascope compara los promedios por métrica de los archivos históricos locales, juzga cada cambio según la polaridad de la métrica y genera un informe de tres capas: **diagnóstico → evidencia → datos completos**.

## Características

- **Motor de reglas de diagnóstico** — 16 reglas integradas entre métricas (espiral de swap, saturación de disco, desbordamiento de cola accept, OOM, punto caliente de un núcleo, presión SYN, detección de reinicio…). Cada acierto entrega una conclusión en lenguaje llano, su evidencia y los siguientes comandos. Nunca puntuaciones sintéticas de salud.
- **146 métricas, 5 categorías** — PSI, descartes softnet, puntos calientes por núcleo (plegado automático), distribución de estados TCP, direct reclaim, LVM/MD. Umbrales propios para contadores ruidosos.
- **Informe con datos completos** — las filas estables quedan visibles pero atenuadas, orden estable, tinte ∝ |Δ|, aparición ⊕ / desaparición ⊖ diferenciadas, anclas Top-5.
- **Todo es configuración** — catálogo, reglas y umbrales en JSON exportable, validado al cargar. `profiles/` incluye full/core.
- **Modo headless** — `deltascope compare` imprime el mismo informe en texto (ANSI) o JSON, código de salida 2 ante regresiones: listo para cron.
- **Diseñado para entornos aislados** — un binario estático, UI y gráficos embebidos, autenticación local, sin CDN, sin telemetría, sin tráfico saliente.

## Inicio rápido

Binarios precompilados en [`dist/`](../../dist/): `linux-amd64` (kernel ≥ 3.2), `linux-arm64`, `linux-amd64-el6` (kernel 2.6.32).

Compilar desde código (una vez, con internet):

```bash
make vendor && make test && make build
```

Despliegue (referencia Rocky Linux 9, totalmente offline posible):

```bash
RETENTION_DAYS=7 LISTEN_ADDR=0.0.0.0:8080 \
DSCOPE_ADMIN_USER=admin DSCOPE_ADMIN_PASS='...' ./deploy.sh
```

## Uso

```bash
deltascope serve   -listen :8080 -archive DIR -data DIR [-catalog F] [-rules F]
deltascope user    add|del|list <name>
deltascope catalog export > catalog.json
deltascope rules   export > rules.json
deltascope compare -a-start 2026-07-09T14:00 -a-end 2026-07-09T15:00 \
                   -b-start 2026-07-10T14:00 -b-end 2026-07-10T15:00 \
                   [-format text|json] [-all] [-no-color]
```

## Semántica del diff

- Los contadores se promedian **como tasas** por ventana (semántica pmdiff)
- Δ% = (B − A) / |A| × 100; se juzga solo si `|Δ| ≥ umbral` (15 % global, overrides por métrica)
- Polaridad: `worse_up` / `better_up` / `neutral`
- A=0 → B≠0 se reporta como ∞; las métricas ausentes en ambos lados se omiten en silencio
- Aparición ⊕ / desaparición ⊖ son eventos de primera clase

## Seguridad

PBKDF2-HMAC-SHA256 (600k iteraciones) · sesiones sin estado firmadas con HMAC · limitación de intentos por IP · lista blanca de nombres de métricas + exec en array (nunca shell) · CSP estricta sin inline · unidad systemd endurecida · las credenciales nunca salen del host.

## Notas

Ventanas en la zona horaria local del servidor · máx. 32 días por ventana · paso de tendencia adaptativo · gráficos incluidos (Apache-2.0) · el primer día hay que dejar acumular archivos.
