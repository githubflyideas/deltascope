<div align="center">

<img src="../logo.svg" alt="deltascope" width="96">

# deltascope

**단일 리눅스 호스트 성능 회귀 관측기 — 의사 소견 포함**

🌐 [English](../../README.md) · [中文](README.zh-CN.md) · [日本語](README.ja.md) · **한국어** · [Deutsch](README.de.md) · [Français](README.fr.md) · [Español](README.es.md) · [Português](README.pt.md) · [Italiano](README.it.md) · [Русский](README.ru.md) · [ไทย](README.th.md) · [Bahasa Indonesia](README.id.md) · [Tiếng Việt](README.vi.md)


</div>

---

<div align="center">
<img src="../preview-diff.svg" width="100%" alt="A/B report">
<br><br>
<img src="../preview-trend.svg" width="100%" alt="Trends">
<br><sub>추가 화면 미리보기는 영어 README 참조</sub>
</div>

## 무엇을 하는가

두 시간 창 — **기준선 A** 와 **비교 대상 B** — 을 선택하면 deltascope 가 로컬 이력 아카이브에서 지표별 창 평균을 계산하고, 지표 극성에 따라 판정하여 3계층 리포트를 생성합니다: **진단 → 근거 → 전체 데이터**.

## 특징

- **진단 규칙 엔진** — 16개 내장 교차 지표 규칙(스왑 스파이럴, 디스크 포화, accept 큐 오버플로, OOM, 단일 코어 핫스팟, SYN 압력, 재부팅 감지…). 매칭 시 평이한 결론 + 근거 + 다음 명령을 제시. 합성 헬스 점수는 만들지 않습니다.
- **146개 내장 지표 · 5개 분류** — PSI, softnet 드롭, 코어별 핫스팟(자동 접기), TCP 상태 분포, 직접 회수, LVM/MD 포함. 노이즈가 큰 카운터엔 개별 임계값.
- **전체 데이터 리포트** — 변화 없는 행은 흐리게 유지, 행 위치 고정, 행 배경 농도 ∝ |Δ|, 신규 ⊕ / 소실 ⊖ 구분 표시, Top-5 앵커.
- **모든 것이 설정 파일** — 카탈로그·규칙·임계값은 내보내기 가능한 JSON, 로드 시 검증. `profiles/` 에 full/core 제공.
- **헤드리스 모드** — `deltascope compare` 는 동일 리포트를 텍스트(ANSI) 또는 JSON 으로 출력, 회귀 발견 시 종료 코드 2. cron·알림 파이프라인에 바로 연결.
- **폐쇄망 우선 설계** — 정적 바이너리 하나, UI·차트 내장, 로컬 인증, CDN 없음, 텔레메트리 없음, 외부 통신 전무.

## 빠른 시작

빌드된 바이너리는 [`dist/`](../../dist/): `linux-amd64`(커널 ≥3.2), `linux-arm64`, `linux-amd64-el6`(커널 2.6.32).

소스 빌드(인터넷 연결 개발 머신에서 1회):

```bash
make vendor && make test && make build
```

배포(Rocky Linux 9 기준, 완전 오프라인 가능):

```bash
RETENTION_DAYS=7 LISTEN_ADDR=0.0.0.0:8080 \
DSCOPE_ADMIN_USER=admin DSCOPE_ADMIN_PASS='강한암호' ./deploy.sh
```

## 사용법

```bash
deltascope serve   -listen :8080 -archive DIR -data DIR [-catalog F] [-rules F]
deltascope user    add|del|list <name>
deltascope catalog export > catalog.json
deltascope rules   export > rules.json
deltascope compare -a-start 2026-07-09T14:00 -a-end 2026-07-09T15:00 \
                   -b-start 2026-07-10T14:00 -b-end 2026-07-10T15:00 \
                   [-format text|json] [-all] [-no-color]
```

## 판정 의미론

- 카운터는 창별 **속도 평균**으로 환산(pmdiff 와 동일)
- Δ% = (B − A) / |A| × 100, `|Δ| ≥ 임계값`만 판정(기본 15%, 노이즈 지표는 개별값)
- 극성: `worse_up` / `better_up` / `neutral`
- A=0 → B≠0 은 ∞. 양쪽 모두 없는 지표는 자동 생략 — 카탈로그 확장은 부담 없이
- 신규 ⊕ / 소실 ⊖ 은 독립 이벤트로 구분

## 보안

PBKDF2-HMAC-SHA256(600k 반복) · HMAC 서명 무상태 세션 · IP별 로그인 제한 · 지표명 화이트리스트 + 배열형 exec · 엄격한 CSP · 강화된 systemd 유닛 · 자격 증명은 호스트 밖으로 나가지 않습니다.

## 참고

시간 창은 서버 로컬 시간대 기준 · 창 최대 32일 · 트렌드 스텝 자동 조절 · 차트 내장(Apache-2.0) · 첫날은 아카이브 축적 대기 필요.
