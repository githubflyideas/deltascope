<div align="center">

<img src="../logo.svg" alt="deltascope" width="96">

# deltascope

**単一 Linux ホストの性能リグレッション観測台 —— 医師の所見つき**

🌐 [English](../../README.md) · [中文](README.zh-CN.md) · **日本語** · [한국어](README.ko.md) · [Deutsch](README.de.md) · [Français](README.fr.md) · [Español](README.es.md) · [Português](README.pt.md) · [Italiano](README.it.md) · [Русский](README.ru.md) · [ไทย](README.th.md) · [Bahasa Indonesia](README.id.md) · [Tiếng Việt](README.vi.md)


</div>

---

<div align="center">
<img src="../preview-diff.svg" width="100%" alt="A/B report">
<br><br>
<img src="../preview-trend.svg" width="100%" alt="Trends">
<br><sub>その他の画面プレビューは英語版 README を参照</sub>
</div>

## 何をするか

**ベースライン A** と **比較対象 B** の 2 つの時間帯を選ぶと、deltascope はローカルの履歴アーカイブから各メトリクスの窓平均を算出し、極性に基づいて逐項判定し、三層レポートを生成します:**診断 → 根拠 → 全量データ**。

## 特徴

- **診断ルールエンジン** —— 16 の組み込みクロスメトリクスルール(スワップスパイラル、ディスク飽和、accept キュー溢れ、OOM、コア別ホットスポット、SYN 圧力、再起動検知…)。命中すると平易な結論 + 根拠 + 次に実行すべきコマンドを提示。合成ヘルススコアは作りません。
- **146 メトリクス・5 カテゴリ** —— PSI、softnet ドロップ、コア別ホットスポット(自動折りたたみ)、TCP 状態分布、直接回収、LVM/MD を含む。ノイズの多いカウンタには個別しきい値。
- **全量データレポート** —— 変化なし行は灰色で保持、行位置固定、行の濃淡は |Δ| に比例、新規 ⊕ / 消失 ⊖ を区別表示、Top-5 アンカー。
- **すべて設定ファイル** —— カタログ・ルール・しきい値はエクスポート可能な JSON。読み込み時に検証。`profiles/` に full/core 二档。
- **ヘッドレスモード** —— `deltascope compare` は同一レポートをテキスト(ANSI)または JSON で出力し、リグレッション検出時に終了コード 2。cron・アラートに直結。
- **隔離環境のための設計** —— 静的バイナリ 1 つ、UI・チャート内蔵、ローカル認証、CDN なし、テレメトリなし、外向き通信ゼロ。

## クイックスタート

ビルド済みバイナリは [`dist/`](../../dist/):`linux-amd64`(カーネル ≥3.2)、`linux-arm64`、`linux-amd64-el6`(カーネル 2.6.32)。

ソースからビルド(ネット接続のある開発機で一度):

```bash
make vendor && make test && make build
```

デプロイ(Rocky Linux 9 参考、完全オフライン可):

```bash
RETENTION_DAYS=7 LISTEN_ADDR=0.0.0.0:8080 \
DSCOPE_ADMIN_USER=admin DSCOPE_ADMIN_PASS='強いパスワード' ./deploy.sh
```

## 使い方

```bash
deltascope serve   -listen :8080 -archive DIR -data DIR [-catalog F] [-rules F]
deltascope user    add|del|list <name>
deltascope catalog export > catalog.json
deltascope rules   export > rules.json
deltascope compare -a-start 2026-07-09T14:00 -a-end 2026-07-09T15:00 \
                   -b-start 2026-07-10T14:00 -b-end 2026-07-10T15:00 \
                   [-format text|json] [-all] [-no-color]
```

## 判定ロジック

- カウンタは窓ごとに**レート平均**へ換算(pmdiff と同一の意味論)
- Δ% = (B − A) / |A| × 100。`|Δ| ≥ しきい値` のみ判定(既定 15%、ノイズ指標は個別値)
- 極性:`worse_up` / `better_up` / `neutral`
- A=0 → B≠0 は ∞。両側欠損のメトリクスは自動スキップ —— カタログは安心して拡張可能
- 新規 ⊕ / 消失 ⊖ は独立イベントとして区別

## セキュリティ

PBKDF2-HMAC-SHA256(600k 回)· HMAC 署名ステートレスセッション · IP 単位ログイン制限 · メトリクス名ホワイトリスト + 配列形式 exec · 厳格 CSP · 強化 systemd ユニット · 資格情報はホスト外に出ません。

## 備考

時間帯はサーバーのローカルタイムゾーン · 窓は最長 32 日 · トレンドステップ自動調整 · チャートは同梱(Apache-2.0)· 初日はアーカイブ蓄積待ちが必要。
