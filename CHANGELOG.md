# Changelog

Все заметные изменения этого проекта документируются в этом файле.

Формат основан на [Keep a Changelog](https://keepachangelog.com/ru/1.1.0/),
версионирование следует [Semantic Versioning](https://semver.org/lang/ru/).

Ветвление — Git Flow (`master`, `feat/*`, `fix/*`, `hotfix/*`, `release/v*`, `chore/*`).
Сообщения коммитов — Conventional Commits на русском.

## [Unreleased]

### Added

**Документация / планирование (этап pre-v0.1)**
- Связь с задачей: [QCoreTech/awesome#908 — Ежедневный отчёт в Telegram об ошибках на торговых серверах](https://github.com/QCoreTech/awesome/issues/908).
- `CLAUDE.md` — память проекта для Claude Code: стек, структура, конвенции, OQ-блокеры.
- `README.md`, `VERSION=0.0.0`, `CHANGELOG.md`, `.env.example`, `.gitignore`, `.dockerignore`.
- `docs/plans/00-analysis.md` — полный системный анализ (senior analyst, Context7-verified): глоссарий, 19 FR, NFR, 12 рисков, 15 OQ, MoSCoW, roadmap v0.1..v0.3, DoD↔FR маппинг.
- `docs/plans/10-architecture.md` — архитектурный план (senior architect): C4-диаграммы, digest pipeline, SQLite schema, Go-интерфейсы, error policy, concurrency, extensibility, UI v0.3 и прокси v0.2.
- `docs/plans/11-sre.md` — SLO/observability/runbook (senior SRE): 5 SLO с 28d rolling окном, 10+ метрик, PromQL burn-rate alerts, healthchecks `/healthz` + `/readyz`, DR/backup, chaos-сценарии, dead-man switch.
- `docs/plans/12-devops.md` — CI/CD & release (senior DevOps): Git Flow, Conventional Commits, SemVer контракт, Docker multi-stage, workflows, release-процесс, Dependabot, secrets, environments, rollback, quality gates.
- `docs/plans/13-qa.md` — тест-план (senior QA): 30 сценариев (T-1..T-30), покрытие FR-1..FR-19, пирамида unit/integration/e2e, фикстуры, fuzz, snapshot, security-тесты, go/no-go чек-лист.
- `docs/plans/99-teamlead-summary.md` — сводка для TL: блокеры, 7 спорных точек с рекомендациями, roadmap, оценка трудозатрат (~20+12+15 man-days).
- ADR-0001 — фиксация решений в Architecture Decision Records (шаблон Нигарда).
- ADR-0002 — State store: SQLite (WAL) на pure-Go драйвере `modernc.org/sqlite` (критично для distroless deploy).
- ADR-0003 — Формат отчёта: Markdown по умолчанию, опция через конфиг.
- ADR-0004 — Templating: `text/template` (stdlib) + обязательный FuncMap safeguards против injection.
- ADR-0005 — Scheduler: `robfig/cron/v3` с явным `Europe/Moscow`.
- ADR-0006 — Логгер: `log/slog` (stdlib, Go 1.21+).
- ADR-0007 — HTTP клиент VictoriaLogs: `net/http` + собственная обёртка с retry/backoff/маскировкой токенов.
- ADR-0008 — Telegram клиент: собственный тонкий (~400 LOC) — только `getMe`/`sendMessage`/`sendDocument`/`sendMediaGroup`.
- ADR-0009 — Dedup fingerprint: `sha1(host + app + normalize(_msg))`, regex-профиль в YAML.
- ADR-0010 — Модульность: `cmd/log-analyser` + `internal/*` по доменам (collector, dedup, render, delivery, scheduler, state, config, grafana, observability).
- ADR-0011 — Конфигурация: `kelseyhightower/envconfig` + опциональный YAML overlay (секреты только env).
- ADR-0012 — Прокси-поддержка (Proposed, v0.2): `golang.org/x/net/proxy` для SOCKS5 + `http.Transport.Proxy` для HTTP, раздельные `TG_PROXY_URL` и `VL_PROXY_URL`.
- ADR-0013 — Retention cleanup: in-process goroutine + ежедневный тикер.

**DevOps / инфраструктура**
- `Dockerfile` — multi-stage: `golang:1.22-alpine` → `gcr.io/distroless/static-debian12:nonroot`, CGO-free, buildx `TARGETOS/TARGETARCH`, OCI-labels.
- `.golangci.yml` — golangci-lint v2 (errcheck, staticcheck, gosec, errorlint, bodyclose, revive, gocritic, …).
- `.goreleaser.yaml` — v2: cross-compile linux amd64+arm64, GHCR manifest list, SBOM (Syft), cosign keyless, auto-changelog по Conventional Commits.
- `commitlint.config.js` — `@commitlint/config-conventional` + русский subject.
- `.github/dependabot.yml` — gomod + docker + github-actions (weekly).
- `.github/workflows/ci.yml` — lint / test (race+cover) / govulncheck / cross-build amd64+arm64 / docker-build (no push on PR).
- `.github/workflows/release.yml` — goreleaser на tag `v*.*.*` + GHCR + SBOM + cosign + build-provenance attestation.
- `.github/workflows/commitlint.yml`, `codeql.yml`, `trivy.yml`, `stale.yml`.
- `.github/PULL_REQUEST_TEMPLATE.md`.
- `deploy/docker-compose.dev.yml` + `deploy/prometheus/prometheus.yml` + `deploy/grafana/provisioning/*` — локальное окружение dev.
- `tests/README.md` + `tests/fixtures/README.md` — структура тестов и спецификация фикстур логов.

### Changed
- —

### Deprecated
- —

### Removed
- —

### Fixed
- —

### Security
- —

---

Планируемые релизы (см. `docs/plans/00-analysis.md`):

- **v0.1.0** — MVP: ингест из VictoriaLogs → дедуп/группировка → рендер отчётов → Telegram (обложка + вложения), Grafana Explore deeplinks, daemon с ежедневным сбросом в 08:00 MSK.
- **v0.2.0** — Real-time алертинг, поддержка прокси для Telegram, опциональное LLM-резюме.
- **v0.3.0** — Read-only UI для просмотра накопленных инцидентов.

[Unreleased]: https://github.com/QCoreTech/log_analyser/compare/v0.0.0...HEAD
