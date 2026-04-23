# Changelog

Все заметные изменения этого проекта документируются в этом файле.

Формат основан на [Keep a Changelog](https://keepachangelog.com/ru/1.1.0/),
версионирование следует [Semantic Versioning](https://semver.org/lang/ru/).

Ветвление — Git Flow (`master`, `feat/*`, `fix/*`, `hotfix/*`, `release/v*`, `chore/*`).
Сообщения коммитов — Conventional Commits на русском. Все коммиты — GPG-signed.

## [Unreleased]

### Added
- —

## [0.1.0] — 2026-04-23

Первый функциональный релиз (MVP). Закрывает полный DoD issue
[QCoreTech/awesome#908](https://github.com/QCoreTech/awesome/issues/908):
ежедневный Telegram-отчёт об ошибках торговых серверов с Grafana-deeplink'ами.

### Added

**Планирование / документация**
- `CLAUDE.md`, `README.md`, `VERSION=0.1.0`, `.env.example`, `.gitignore`, `.dockerignore`.
- 6 ролевых планов в `docs/plans/`: аналитика (FR, NFR, риски, OQ),
  архитектура (C4, интерфейсы, pipeline), SRE (SLO, runbook),
  DevOps (CI/CD), QA (30 сценариев), teamlead-summary.
- 13 Architecture Decision Records (ADR-0001..ADR-0013) — все решения
  по шаблону Нигарда, прошедшие Context7-проверку.

**Код (internal/*)**
- `internal/version` — build-info через `-ldflags`, JSON-совместимая структура.
- `internal/config` — `envconfig` + опциональный YAML overlay (ADR-0011),
  strict `KnownFields`, отказ от секретов в YAML, `SensitiveValues()` для
  маскирования в логах.
- `internal/observability/logging` — `log/slog` (ADR-0006) с `ReplaceAttr`
  для маскирования токенов (NFR-S2).
- `internal/grafana` — builder Explore deeplink'ов по `schemaVersion=1 +
  orgId + panes=<JSON>`; path-префиксы сохраняются; unit 96% + integration
  против реальной Grafana (302 на /login).
- `internal/httpclient` — общая обёртка `net/http` (ADR-0007) с retry/
  backoff/jitter, proxy через `http.ProxyFromEnvironment` (уважает
  `NO_PROXY`), replay-safe POST через `GetBody`, маскирование токенов
  в ошибках.
- `internal/collector` — LogsQL builder + HTTP-клиент VictoriaLogs:
  `StreamQuery` (JSON-lines через `bufio.Scanner`, буфер 10 МБ),
  `Hits`, `Ping`. Интеграционно подтверждено на prod (2026-04-23):
  570 400 записей/час на `t5`, 100 error/critical за 24ч.
- `internal/dedup` — нормализация + fingerprint (ADR-0009):
  `sha1(host + app + normalize(_msg))`, встроенный regex-профиль для
  торговых серверов QCoreTech (ts_ru, ts_iso8601, uuid, ipv4, hex16+,
  numbers); `Aggregator` с потокобезопасным `Add`, эскалацией `Level`
  (critical > error > ...), `AppTotalsFor`, `SummaryFor` с разделением
  на Above/Below шум-порога. Реальный compression ratio на prod-данных:
  5×..105× в зависимости от хоста.
- `internal/render` — `text/template` + `embed.FS` (ADR-0003, ADR-0004):
  `report.md.tmpl` per-host отчёт по схеме 00-analysis.md §15.1;
  `cover.html.tmpl` HTML для Telegram sendMessage с `html.EscapeString`
  против XSS в пользовательских полях; спецкейс «0 записей» → пометка
  «не обнаружено».
- `internal/telegram` — тонкий клиент Bot API (ADR-0008, ~400 LOC):
  `getMe` / `sendMessage` / `sendDocument` / `sendMediaGroup` с правильным
  multipart uploading (`attach://fN`); respect `parameters.retry_after`
  на 429; `APIError` с `errors.As`; `scrubErr` стирает Token из ошибок.
- `internal/pipeline` — оркестратор digest cycle: per-host параллельный
  collect, агрегация, рендер 5 `.md` + cover, доставка через
  `sendMediaGroup` + обязательный fallback на `sendDocument×5`
  (решение §4.3 99-teamlead-summary), partial-delivery маркер в cover,
  incident-level deeplink'и с узким окном ±1 мин.
- `internal/state` — SQLite WAL через `modernc.org/sqlite` (pure Go,
  ADR-0002): `runs` таблица с `UNIQUE(window_from, window_to)`,
  `BeginRun` в IMMEDIATE-транзакции возвращает `ErrAlreadyDelivered`
  если `status='done'`, `MarkCoverSent` как pre-commit marker,
  `FinishRun` / `FailRun`. Решает R-7 (рестарт в середине delivery).
- `internal/scheduler` — обёртка над `robfig/cron/v3` (ADR-0005):
  cron-планировщик с `Europe/Moscow` TZ, детерминированное окно
  `[now.Hour:00 − 24h, now.Hour:00)`, graceful `Stop(ctx)` через
  `cron.Stop()`.
- `internal/observability/metrics` — собственный `prometheus.Registry`
  + 8 custom-метрик (digest_cycle_total{status}, duration_seconds
  histogram, last_successful_delivery_timestamp_seconds, host_records,
  host_incidents_last_cycle, readyz_*) + Go runtime/process/BuildInfo
  коллекторы.
- `internal/observability/httpd` — HTTP-сервер `/metrics` (promhttp
  с OpenMetrics), `/healthz` (liveness, всегда 200), `/readyz`
  (readiness с реальным VL.Ping и TG.GetMe + кэш 30с).

**CLI (`cmd/log-analyser`)**
- `run` — daemon с cron + observability HTTP server + graceful shutdown
  по SIGINT/SIGTERM.
- `once --date=YYYY-MM-DD` — ручной прогон за указанные сутки, уважает
  идемпотентность.
- `health` — реальный `VL.Ping` + `TG.GetMe`.
- `version`, `help`.

**DevOps / инфраструктура**
- `Dockerfile` multi-stage → `distroless/static:nonroot`, CGO-free
  (обязательное требование для `modernc.org/sqlite`).
- `.golangci.yml` (v2), `.goreleaser.yaml` (v2, SBOM + cosign keyless),
  `commitlint.config.js`.
- `.github/workflows/`: `ci.yml` (lint + test race+cover + govulncheck +
  cross-build amd64/arm64 + docker-build), `release.yml` (goreleaser +
  GHCR + attestation), `commitlint.yml`, `codeql.yml`, `trivy.yml`,
  `stale.yml`.
- `.github/dependabot.yml` (gomod + docker + github-actions).
- `.github/PULL_REQUEST_TEMPLATE.md`.
- `deploy/docker-compose.dev.yml` + `deploy/prometheus/prometheus.yml`
  + Grafana provisioning.
- `configs/fingerprint-profile.yaml` — дефолтный override-профиль дедупа.
- `tests/README.md` + `tests/fixtures/README.md`.

### Verified on prod (2026-04-23)

Полный end-to-end digest cycle против реальных `10.145.0.43:9428`
(VictoriaLogs) и `@xt_gh_actions_bot` (Telegram):

| Host | Records (24h) | Incidents | Ratio |
|---|---:|---:|---:|
| t1 | 0 | 0 | n/a (файл с пометкой «не обнаружено») |
| ali-t1 | 48 | 7 | 6.9× |
| t2 | 1440 | 15 | 96× |
| aws-t3 | 4 | 2 | 2× |
| t5 | 631 | 14 | 45× |

Cover + 5 файлов доставлены в чат `-1003294700440`, `message_id=3570`,
`partial_errors=0`, длительность всего цикла 8.4s. Повторный `once`
на то же окно корректно пропущен state'ом (`ErrAlreadyDelivered`).

### OQ — закрытые

- **OQ-1** — VL внутри корпсети, без auth.
- **OQ-2** — прокси для VL не требуется.
- **OQ-3** — UID Grafana-datasource получен через `/api/datasources`.
- **OQ-4** — формат отчёта `.md` по умолчанию.
- **OQ-5** — `host`, `app`, `level` — стрим-поля (подтверждено `_stream`
  в реальных записях).

### OQ — открытые

- **OQ-8** — один чат vs threads в Telegram — в v0.1 используется один
  чат с sendMediaGroup; threads зарезервированы под realtime в v0.2.

---

Планируемые релизы (см. `docs/plans/00-analysis.md §12`):

- **v0.2.0** — Real-time алертинг на новые `critical`-fingerprint'ы,
  поддержка прокси для Telegram (SOCKS5/HTTP), опциональное LLM-резюме,
  staging environment.
- **v0.3.0** — Read-only веб-UI (embed.FS + SSR) для просмотра
  накопленных инцидентов.

[Unreleased]: https://github.com/temapskov/log-analyser/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/temapskov/log-analyser/releases/tag/v0.1.0
