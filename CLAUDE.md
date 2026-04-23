# CLAUDE.md — память проекта `log_analyser`

> Этот файл читается Claude Code **каждую** сессию. Держи его живым, лаконичным и актуальным.
> Глобальные правила пользователя (`~/.claude/CLAUDE.md`) имеют приоритет и не дублируются здесь.

## 1. О проекте

**Назначение:** сервис-демон на Go, формирующий ежедневно в 08:00 MSK отчёт об ошибках (`level in {error, critical}`) за предыдущие 24 часа по торговым серверам `t1`, `ali-t1`, `t2`, `aws-t3`, `t5`. Источник логов — **VictoriaLogs**. Доставка — **Telegram Bot API** (одно cover-сообщение в приватный чат + 5 файлов-вложений). Каждый инцидент сопровождается **deeplink в Grafana Explore**.

**Issue-источник:** [QCoreTech/awesome#908](https://github.com/QCoreTech/awesome/issues/908). Ассайни: `@temapskov`.

**Режим работы:** долгоживущий daemon (не cron one-shot). Между digest-циклами state хранится локально (SQLite + WAL).

## 2. Стек (зафиксирован аналитиком, подтверждён Context7)

- **Язык:** Go 1.22+ (modules).
- **Контейнеризация:** Docker (multi-stage, distroless/alpine, non-root).
- **CI/CD:** GitHub Actions (lint → test → build → image push → release).
- **State:** SQLite (WAL-режим), без внешней БД в MVP.
- **HTTP-клиенты:** `net/http` + собственная обёртка с retry/backoff и маскировкой токенов.
- **Scheduler:** `robfig/cron/v3`, таймзона `Europe/Moscow`.
- **Логи:** `log/slog` (stdlib, Go 1.21+).
- **Конфиг:** `kelseyhightower/envconfig` (env-first) + опциональный YAML override.
- **Метрики:** Prometheus `/metrics`.
- **Прокси (v0.2):** `golang.org/x/net/proxy` для SOCKS5, `http.Transport.Proxy` для HTTP(S).

Детали/альтернативы см. `docs/plans/00-analysis.md` раздел 13.1 и ADR в `docs/adr/`.

## 3. Рабочая структура репозитория (целевая, v0.1)

```
.
├── CLAUDE.md                 # этот файл
├── CHANGELOG.md              # Keep a Changelog + SemVer
├── VERSION                   # единственная строка с текущей версией
├── README.md                 # quick start + docker run
├── .env.example              # образец конфига (без секретов)
├── .gitignore
├── .github/
│   ├── workflows/            # ci.yml, release.yml
│   ├── ISSUE_TEMPLATE/
│   └── PULL_REQUEST_TEMPLATE.md
├── cmd/
│   └── log-analyser/         # main.go — CLI: run | once | health | version
├── internal/
│   ├── config/               # envconfig + yaml overlay
│   ├── collector/            # VictoriaLogs HTTP client + LogsQL builder
│   ├── dedup/                # fingerprint + normalize regex profiles
│   ├── render/               # шаблоны отчётов (text/template)
│   ├── delivery/             # Telegram sender (sendMessage/sendMediaGroup)
│   ├── grafana/              # deeplink builder
│   ├── scheduler/            # cron-интеграция, MSK-aware
│   ├── state/                # SQLite store (runs, fingerprints, dlq)
│   └── observability/        # prometheus, slog, healthchecks
├── configs/                  # regex-профили, примеры yaml
├── docs/
│   ├── adr/                  # Architecture Decision Records
│   ├── plans/                # планы по ролям
│   └── reviews/              # кросс-ревью
├── scripts/                  # вспомогательные dev-скрипты
├── deploy/                   # docker-compose, k8s манифесты
└── tests/                    # integration + e2e + фикстуры логов
```

## 4. Соглашения (важно для будущих сессий)

### Git

- Ветка по умолчанию: `master`.
- Git Flow: `feat/*`, `fix/*`, `hotfix/*`, `release/v*`, `chore/*`.
- Conventional Commits **на русском**, GPG-подпись (`-S`) — из глобальных правил.
- Запрещено: force-push в `master`, пропуск хуков (`--no-verify`), amend уже опубликованных коммитов.

### Версионирование

- SemVer. Единственный источник истины — файл `VERSION`.
- `git tag v$(cat VERSION)` при релизе (ручной шаг v0.1, автоматизация — goreleaser в v0.2).
- CHANGELOG обновляется **в том же PR**, где меняется `VERSION`.

### Код

- `golangci-lint run` должен быть зелёным до коммита.
- `go test -race -cover ./...` должен быть зелёным.
- Никаких самописных MarkdownV2-escaper'ов; только HTML `parse_mode` для Telegram.
- Никогда не логировать `TG_BOT_TOKEN`, VL-basic-auth, полный URL с query-токеном.
- Шаблоны рендера — в `internal/render/templates/*.tmpl`, а не inline-строки.

### Конфигурация

- **Источник секретов:** только env / k8s Secret / Vault. Файлы `.env*` — ignored.
- Обязательные env: `TG_BOT_TOKEN`, `TG_CHAT_ID`, `VL_URL`, `GRAFANA_URL`, `GRAFANA_ORG_ID`, `GRAFANA_VL_DS_UID`.
- Опциональные: `HOSTS` (default: `t1,ali-t1,t2,aws-t3,t5`), `SCHEDULE_CRON` (`0 8 * * *`), `TZ` (`Europe/Moscow`), `LEVELS` (`error,critical`), `NOISE_K`, `TOP_N`, `TG_PROXY_URL`.

## 5. Контекст — где что лежит

- **Полный системный анализ:** `docs/plans/00-analysis.md` (Глоссарий, FR, NFR, риски, OQ).
- **Архитектурные решения:** `docs/adr/` (ADR-NNNN) + `docs/plans/10-architecture.md`.
- **SRE (SLO, runbook):** `docs/plans/11-sre.md`.
- **DevOps (CI/CD, релизы):** `docs/plans/12-devops.md`.
- **QA (тест-план):** `docs/plans/13-qa.md`.
- **TeamLead summary:** `docs/plans/99-teamlead-summary.md`.
- **Changelog:** `CHANGELOG.md`.

## 6. Решения TL и открытые блокеры (синхронизировано с docs/plans/99-teamlead-summary.md §2)

**Закрыто 2026-04-23:**
- **OQ-1.** VictoriaLogs находится **внутри корпоративной сети, без auth**. `VL_BASIC_USER` / `VL_BASIC_PASS` не задаются; daemon должен работать в той же сети.
- **OQ-2.** Прокси для VL **не требуется** (`VL_PROXY_URL` в env остаётся пустым).
- **OQ-4.** Формат отчёта — `.md` по умолчанию.

**Частично закрыто:**
- **OQ-3.** Есть **доступ к Grafana API** (`GET /api/datasources`) — UID/type VictoriaLogs-datasource извлекаются автоматически при разработке `feat/grafana-deeplink`; см. команды в `docs/plans/99-teamlead-summary.md §2.1`.

**Остаются открытыми:**
- **OQ-5** (поля стрима в VL) — снимается preflight-чеком `stream_field_names` при старте daemon.
- **OQ-8** (один chat_id vs threads) — предложение команды: один чат в v0.1, threads в v0.2.

## 7. Команды, которые Claude может выполнять сам (без доп. согласования)

- Чтение любых файлов в каталоге проекта.
- `git status`, `git diff`, `git log`, `git add <конкретные пути>`, `git commit -S -m "..."`, `git branch`, `git switch`.
- `go test ./...`, `go build`, `golangci-lint run`, `go mod tidy`.
- `gh issue view/list/comment`, `gh pr view/list/diff`, `gh api GET ...`.

## 8. Что требует согласования с пользователем

- `git push` в любой remote, force-push, удаление веток/тегов.
- Создание/закрытие/мёрдж PR и issue в QCoreTech.
- Добавление новых зависимостей в `go.mod` вне уже утверждённого стека (раздел 2).
- Изменение публичных контрактов (ENV-имена, формат файла, схема TG-cover).
- Отправка сообщений в реальный Telegram (даже тестовых).

## 9. Context7 — правило

Для **любых** вопросов про внешние библиотеки/SDK/API (`net/http`, `robfig/cron`, Telegram Bot API, VictoriaLogs LogsQL, Grafana URL, `envconfig`, Docker syntax и т.п.) — **сначала** идти в Context7 MCP (`resolve-library-id` → `query-docs`), цитировать версию и источник. Это требование пользователя (см. `~/.claude/rules/context7.md`).

## 10. VictoriaLogs MCP — правило

MCP-сервер `victorialogs` доступен только в read-only сценариях разведки (узнать поля, проверить доступность). Для продакшн-кода использовать собственный HTTP-клиент к `$VL_URL`. Никогда не полагаться на MCP как на runtime-зависимость сервиса.
