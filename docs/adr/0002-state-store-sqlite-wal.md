# ADR-0002: State store — SQLite (WAL) на драйвере `modernc.org/sqlite`

- **Статус:** Accepted
- **Дата:** 2026-04-23
- **Автор:** architect
- **Связанные ADR:** следует из [ADR-0001](0001-record-architecture-decisions.md). Влияет на [ADR-0010](0010-internal-module-structure.md), [ADR-0013](0013-retention-cleanup-in-process.md).

## Контекст

Daemon нуждается в persistent локальном state для:
- идемпотентности (`runs`, `delivery_markers` — не отправить cover дважды при рестарте),
- 7-30-90-дневной истории fingerprint'ов (для `IsNew7d` и pattern detection),
- dead-letter очереди доставки.

Объём ничтожный: ≤ 10k строк в сутки во все таблицы, итого < 1 ГБ за год. Нагрузка write-dominant, но < 100 ops/sec. Read-нагрузка из UI (v0.3) — тоже низкая. Один writer-процесс (file lock, NFR-I2).

Требования:
- **Pure-Go build**, без CGO — чтобы сохранить deploy в `FROM gcr.io/distroless/static:nonroot` (NFR-S4).
- Zero-ops backup = copy файла.
- Transactions + crash-safety.
- Одна БД — один писатель; допустимо несколько читателей (WAL).

## Решение

**SQLite в режиме WAL, драйвер `modernc.org/sqlite`** (Context7 ID `/gitlab_cznic/sqlite`, High reputation, Score 91.6 — pure Go SQLite, `database/sql`-совместимый).

DSN:
```
file:/var/lib/log_analyser/state.db?
  _pragma=journal_mode(WAL)&
  _pragma=busy_timeout(5000)&
  _pragma=synchronous(NORMAL)&
  _pragma=foreign_keys(1)&
  _txlock=immediate
```

Миграции — самописный мини-мигратор (`internal/state/migrations/NNN_*.sql` + `schema_version` таблица). Полноценные `golang-migrate`/`goose` — overkill для < 10 миграций/год и добавляют зависимости.

File lock через `flock(2)` на старте daemon — страховка от двух инстансов на одной БД.

## Последствия

**Плюсы**
- Pure-Go: контейнер `FROM scratch` / distroless static, `-tags osusergo,netgo`, статически собранный binary.
- Нулевая эксплуатационная стоимость — бэкап = `cp state.db` (при активном WAL — `sqlite3 state.db ".backup"` для консистентности).
- WAL: параллельные reads (UI v0.3) не блокируют writes daemon'а.
- `database/sql` — стандартный API, легко mocked в тестах через sqlmock.
- Context7 подтвердил полный набор нужных pragma/DSN-опций.

**Минусы / trade-offs**
- `modernc.org/sqlite` — транспилированная версия C-кода, performance ~15-30% ниже `mattn/go-sqlite3`. Для нас < 100 tx/s — не ограничение.
- Бинарный размер +~7 МБ (vs CGO-driver ~2 МБ). Не критично.
- Один writer — при попытке шардировать через несколько daemon'ов потребуется Postgres/Raft. Это out-of-scope MVP и v0.2.
- WAL-файлы (`.db-wal`, `.db-shm`) должны быть в том же persistent volume, что и `.db`.

## Альтернативы

1. **`mattn/go-sqlite3` (CGO).** Отвергнуто: ломает distroless-static deploy, требует alpine + libc, усложняет cross-compile. Performance-преимущество для нас не нужно.
2. **BoltDB / `bbolt`.** Отвергнуто: key-value, а у нас чётко реляционные данные (несколько таблиц, индексы, JOIN-like запросы в UI). Пришлось бы городить вторичные индексы руками. WAL-эквивалент отсутствует, writer-lock жёстче.
3. **Postgres.** Отвергнуто в MVP: внешний сервис, оператор, бэкап, сеть. ROI отрицательный при наших объёмах. Переход возможен в v0.4+ при необходимости HA / multi-instance.
4. **Файлы JSON/YAML + Mutex.** Отвергнуто: atomicity только полной перезаписи, плохо масштабируется, нет транзакций по нескольким сущностям одновременно.
5. **`ncruces/go-sqlite3` (WASM-based).** Интересная альтернатива pure-Go; отвергнуто на текущем этапе как менее зрелая (меньше users, нам важна стабильность state-слоя). Пересмотреть при необходимости performance.

## Ссылки

- Context7: `/gitlab_cznic/sqlite` — DSN parameters, pragmas, connection hooks.
- SQLite docs: <https://www.sqlite.org/wal.html>, <https://www.sqlite.org/pragma.html>.
- `docs/plans/10-architecture.md` §4.
