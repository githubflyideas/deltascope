<div align="center">

<img src="../logo.svg" alt="deltascope" width="96">

# deltascope

**Osciloscópio de regressão de desempenho para um host Linux — com parecer médico**

🌐 [English](../../README.md) · [中文](README.zh-CN.md) · [日本語](README.ja.md) · [한국어](README.ko.md) · [Deutsch](README.de.md) · [Français](README.fr.md) · [Español](README.es.md) · **Português** · [Italiano](README.it.md) · [Русский](README.ru.md) · [ไทย](README.th.md) · [Bahasa Indonesia](README.id.md) · [Tiếng Việt](README.vi.md)


</div>

---

<div align="center">
<img src="../preview-diff.svg" width="100%" alt="A/B report">
<br><br>
<img src="../preview-trend.svg" width="100%" alt="Trends">
<br><sub>Mais telas no README em inglês</sub>
</div>

## O que faz

Escolha duas janelas de tempo — **linha de base A** e **suspeita B** — e o deltascope compara as médias por métrica dos arquivos históricos locais, julga cada mudança pela polaridade da métrica e gera um relatório em três camadas: **diagnóstico → evidências → dados completos**.

## Recursos

- **Motor de regras de diagnóstico** — 16 regras integradas entre métricas (espiral de swap, saturação de disco, estouro da fila accept, OOM, hotspot de núcleo único, pressão SYN, detecção de reboot…). Cada acerto entrega conclusão em linguagem clara, evidências e os próximos comandos. Nunca pontuações sintéticas de saúde.
- **146 métricas, 5 categorias** — PSI, descartes softnet, hotspots por núcleo (dobra automática), distribuição de estados TCP, direct reclaim, LVM/MD. Limiares próprios para contadores ruidosos.
- **Relatório com dados completos** — linhas estáveis permanecem visíveis porém esmaecidas, ordem estável, tonalidade ∝ |Δ|, surgimento ⊕ / desaparecimento ⊖ distintos, âncoras Top-5.
- **Tudo é configuração** — catálogo, regras e limiares em JSON exportável, validado ao carregar. `profiles/` traz full/core.
- **Modo headless** — `deltascope compare` imprime o mesmo relatório em texto (ANSI) ou JSON, código de saída 2 em regressões: pronto para cron.
- **Feito para ambientes isolados** — um binário estático, UI e gráficos embutidos, autenticação local, sem CDN, sem telemetria, sem tráfego de saída.

## Início rápido

Binários pré-compilados em [`dist/`](../../dist/): `linux-amd64` (kernel ≥ 3.2), `linux-arm64`, `linux-amd64-el6` (kernel 2.6.32).

Compilar do código (uma vez, com internet):

```bash
make vendor && make test && make build
```

Implantação (referência Rocky Linux 9, offline total possível):

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

## Semântica do diff

- Contadores são calculados como **médias de taxa** por janela (semântica pmdiff)
- Δ% = (B − A) / |A| × 100; julgado apenas quando `|Δ| ≥ limiar` (15 % global, overrides por métrica)
- Polaridade: `worse_up` / `better_up` / `neutral`
- A=0 → B≠0 é reportado como ∞; métricas ausentes dos dois lados são ignoradas em silêncio
- Surgimento ⊕ / desaparecimento ⊖ são eventos de primeira classe

## Segurança

PBKDF2-HMAC-SHA256 (600k iterações) · sessões sem estado assinadas com HMAC · limitação de login por IP · whitelist de nomes de métricas + exec em array (nunca shell) · CSP estrita sem inline · unidade systemd endurecida · credenciais nunca saem do host.

## Notas

Janelas na hora local do servidor · máx. 32 dias por janela · passo de tendência adaptativo · gráficos embutidos (Apache-2.0) · no primeiro dia é preciso acumular arquivos.
