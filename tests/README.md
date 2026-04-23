# tests/

Каталог хранит интеграционные, e2e и chaos-тесты вне `internal/*` пакетов. Unit-тесты и fuzz-тесты живут рядом с кодом в `internal/<module>/` (Go-конвенция).

См. полный QA-план: [`docs/plans/13-qa.md`](../docs/plans/13-qa.md).

## Структура

```
tests/
├── README.md                  # этот файл
├── integration/               # //go:build integration
│   ├── collector_test.go      # testcontainers-go VL + real LogsQL
│   ├── delivery_test.go       # httptest TG mock + retry/fallback
│   ├── state_test.go          # SQLite WAL concurrency, file-lock
│   └── digest_cycle_test.go   # end-to-end cycle с fake clock
├── e2e/                       # //go:build e2e
│   └── smoke_test.go          # happy-path против staging VL + dev TG
├── chaos/                     # //go:build chaos (nightly only)
│   ├── vl_timeout_test.go
│   ├── tg_flood_test.go
│   ├── sqlite_busy_test.go
│   └── shutdown_test.go
├── fixtures/                  # см. fixtures/README.md
│   └── logs/
│       ├── t5_normal.jsonl
│       ├── t2_noisy.jsonl
│       ├── ali-t1_empty.jsonl
│       └── ...
└── testdata/
    ├── snapshots/             # эталоны для snapshot-тестов
    │   ├── t5_2026-04-22.md
    │   ├── ali-t1_2026-04-22.md
    │   └── cover_2026-04-22.html
    └── fuzz/                  # seed-corpus для go fuzz
        └── FuzzNormalize/
```

## Запуск

```bash
# Unit (внутри internal/*)
gotestsum --format=pkgname -- -race -coverpkg=./... -covermode=atomic -coverprofile=coverage.out ./...

# Integration (testcontainers поднимает VictoriaLogs)
gotestsum --format=pkgname -- -tags=integration ./tests/integration/...

# Fuzz 30с
go test -fuzz=FuzzNormalize_Idempotent -fuzztime=30s ./internal/dedup/

# E2E (требует STAGING_VL_URL, TG_BOT_TOKEN_DEV, STAGING_CHAT_ID)
gotestsum --format=pkgname -- -tags=e2e ./tests/e2e/...

# Chaos — nightly
gotestsum --format=pkgname -- -tags=chaos ./tests/chaos/...

# Обновить snapshot-эталоны
go test ./internal/render -update
```

## Build-tag convention

Каждый файл вне unit-слоя обязан начинаться с:

```go
//go:build integration
// +build integration
```

Это предотвращает случайный запуск в обычном `go test ./...` (testcontainers долго стартует, staging бьёт по сети).

## Порог покрытия

- MVP (v0.1): ≥ 70% global, ≥ 85% для `internal/dedup`, `internal/state`, `internal/config`, `internal/delivery`.
- v0.2: ≥ 80% global.

## Tooling

- **gotestsum** — human-readable output, JUnit XML для CI.
- **testify** (require/assert/suite/mock) — assertions.
- **testcontainers-go** — VictoriaLogs в integration.
- **net/http/httptest** — mock TG и Grafana API.
- **go-cmp** — diff в snapshot-тестах.
- **go.uber.org/mock** (gomock + `mockgen -typed`) — генерация моков interface-ов.
- **testifylint**, **gosec**, **govulncheck** — в golangci-lint pipeline.

## Секреты в E2E

Только через GitHub Actions secrets:
- `TG_BOT_TOKEN_DEV`
- `STAGING_CHAT_ID`
- `STAGING_VL_URL`
- `STAGING_GRAFANA_URL`

Локально — через `.env.test` (в `.gitignore`).
