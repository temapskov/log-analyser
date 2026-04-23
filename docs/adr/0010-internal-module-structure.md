# ADR-0010: Модульность — `cmd/` + `internal/*` по доменам

- **Статус:** Accepted
- **Дата:** 2026-04-23
- **Автор:** architect
- **Связанные ADR:** почти все (определяет, где живут реализации).

## Контекст

Аналитик и CLAUDE.md §3 предложили структуру. Закрепляем её как архитектурное решение со ссылкой на Go Standards Project Layout (Context7 ID `/golang-standards/project-layout`, Medium reputation, Score 91.6).

Требования:
- `internal/` — private-пакеты, компилятор Go запрещает внешний import (Go 1.4+);
- `cmd/<binary>/` — entry points, минимум логики;
- чёткое разделение доменов (collector, dedup, render, delivery, scheduler, state, observability, config);
- в пакете `api.go` — интерфейс, реализация — отдельные файлы, tests — `*_test.go` рядом;
- `pkg/` не используем в MVP (не планируется экспорт).

## Решение

```
.
├── cmd/
│   └── log-analyser/
│       └── main.go                # CLI: run | once | health | version
├── internal/
│   ├── config/                    # envconfig + YAML overlay (ADR-0011)
│   │   ├── api.go                 # Config struct
│   │   ├── load.go
│   │   └── load_test.go
│   ├── collector/                 # VL HTTP client + LogsQL builder (ADR-0007)
│   │   ├── api.go                 # Collector interface
│   │   ├── vlclient.go
│   │   ├── logsql_builder.go
│   │   ├── stream_decoder.go
│   │   └── preflight.go
│   ├── dedup/                     # ADR-0009
│   │   ├── api.go
│   │   ├── normalize.go
│   │   ├── profile.go             # YAML-load
│   │   ├── fingerprint.go
│   │   └── aggregator.go
│   ├── render/                    # ADR-0003, ADR-0004
│   │   ├── api.go
│   │   ├── templates/
│   │   │   ├── cover.tmpl
│   │   │   └── host_report_md.tmpl
│   │   ├── funcmap.go
│   │   ├── host.go
│   │   └── cover.go
│   ├── delivery/                  # ADR-0008
│   │   ├── api.go
│   │   ├── telegram/
│   │   │   ├── client.go
│   │   │   ├── send_message.go
│   │   │   ├── send_document.go
│   │   │   ├── send_media_group.go
│   │   │   ├── multipart.go
│   │   │   ├── ratelimit.go
│   │   │   └── errors.go
│   │   └── deliverer.go           # высокоуровневый с fallback
│   ├── grafana/                   # URL builder
│   │   ├── api.go
│   │   └── linker.go
│   ├── scheduler/                 # ADR-0005
│   │   ├── api.go
│   │   └── cron.go
│   ├── state/                     # ADR-0002
│   │   ├── api.go
│   │   ├── sqlite.go
│   │   ├── migrations.go
│   │   └── migrations/
│   │       └── 001_init.sql
│   ├── digest/                    # orchestrator — digest.Cycle
│   │   └── cycle.go
│   ├── httpclient/                # Factory, proxy (ADR-0012)
│   │   ├── factory.go
│   │   └── mask.go
│   ├── observability/             # slog + prometheus + healthchecks
│   │   ├── logger.go              # ADR-0006
│   │   ├── metrics.go
│   │   └── health.go
│   └── errs/                      # sentinel errors (см. 10-architecture.md §6.2)
│       └── errs.go
├── configs/
│   ├── normalize.yaml             # dedup profile
│   └── example.yaml               # overlay-образец
├── docs/
│   ├── adr/
│   └── plans/
├── deploy/                        # Dockerfile, compose, k8s
├── scripts/
├── tests/
│   ├── integration/
│   └── e2e/
├── go.mod
├── go.sum
├── VERSION
├── CHANGELOG.md
├── CLAUDE.md
├── README.md
└── .env.example
```

### Правила импорта

1. `cmd/log-analyser/main.go` импортирует **только** `internal/config`, `internal/digest`, `internal/observability`. Dependency injection — в `main.go`.
2. `internal/digest` (orchestrator) импортирует все остальные `internal/*` пакеты через их интерфейсы (`api.go`).
3. Реализации-пакеты **не импортируют** другие реализации-пакеты — только через `api.go`-интерфейсы. Исключения — utility-пакеты (`internal/errs`, `internal/httpclient`).
4. Никаких circular deps — lint `importboss`/`golangci-lint`.
5. `internal/render/templates/*.tmpl` — через `embed.FS` в `render/embed.go`.

## Последствия

**Плюсы**
- Каждый домен изолирован, легко unit-тестировать (DI через `api.go`).
- Стандартная раскладка для Go-сообщества — новый разработчик ориентируется за 15 минут.
- `internal/` защищает private code от случайного импорта (например, из `cmd/` другого бинаря, если появится).
- Тесты лежат рядом с кодом, нет «`tests/`-хранилища-всего-подряд».

**Минусы / trade-offs**
- Много маленьких пакетов → много `import` в `main.go`. Читаемо.
- `digest.Cycle` концентрирует dependencies — потенциальная «god-structure». Митигация: keep-it-thin — orchestrator только цепочкой вызывает методы интерфейсов, не содержит бизнес-логики.

## Альтернативы

1. **Плоская структура (всё в корне).** Отвергнуто: не масштабируется, мешает test-isolation.
2. **`pkg/` для «что может импортироваться снаружи».** Отвергнуто в MVP: ничего не планируем экспортировать. Добавим, когда появится нужда.
3. **Hexagonal / clean-architecture (adapters/, domain/, usecases/).** Отвергнуто как over-engineering для утилиты. Линия проще: domain-пакет = `internal/<domain>`.
4. **Monorepo с несколькими бинарями.** Отвергнуто в MVP (только один бинарь). Добавить можно будет без ломки структуры — `cmd/<new-binary>/main.go`.
5. **Игнорировать golang-standards layout, придумать своё.** Отвергнуто: bus factor, onboarding.

## Ссылки

- Context7: `/golang-standards/project-layout`.
- `docs/plans/10-architecture.md §5`.
- CLAUDE.md §3.
