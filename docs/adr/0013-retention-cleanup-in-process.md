# ADR-0013: Retention cleanup — in-process goroutine + ежедневный тикер

- **Статус:** Accepted
- **Дата:** 2026-04-23
- **Автор:** architect
- **Связанные ADR:** [ADR-0002](0002-state-store-sqlite-wal.md), [ADR-0010](0010-internal-module-structure.md).

## Контекст

NFR-Ret1: файлы отчётов хранятся локально 30 дней, потом удаляются.
NFR-Ret2: fingerprints хранятся 90 дней (для pattern-detection 7-30-90-дневных окон).
FR-18: retention чистится автоматически.

Необходимо: простой, надёжный, без внешних инструментов cleaner, который не тормозит digest cycle.

## Решение

**Отдельная goroutine в том же процессе**, запускается из `main.go` после инициализации state и scheduler.

```go
type Retention struct {
    ReportsDir    string
    ReportsDays   int
    FingerprintsDays int
    DLQDays       int
    Store         state.StateStore
    Log           *slog.Logger
    Clock         Clock // для тестов
}

func (r *Retention) Run(ctx context.Context) error {
    // запуск при старте (catch-up), потом — раз в сутки
    if err := r.sweep(ctx); err != nil {
        r.Log.Error("initial sweep failed", "err", err)
    }
    ticker := time.NewTicker(24 * time.Hour)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-ticker.C:
            if err := r.sweep(ctx); err != nil {
                r.Log.Error("sweep failed", "err", err)
            }
        }
    }
}

func (r *Retention) sweep(ctx context.Context) error {
    // 1) FS: reports/*.{md,html,txt}, mtime < now-REPORTS_DAYS → unlink
    // 2) DB: DELETE FROM fingerprints WHERE last_seen < now - FP_DAYS
    // 3) DB: DELETE FROM runs WHERE finished_at < now - REPORTS_DAYS AND status='ok'
    // 4) DB: DELETE FROM dead_letter WHERE retries >= max_retries AND updated_at < now - DLQ_DAYS
}
```

Время запуска — **03:00 MSK** (после ночного trough, до утреннего digest в 08:00, исключая конкуренцию с основным циклом). Добавляется как отдельный cron job:
```go
c.AddFunc("CRON_TZ=Europe/Moscow 0 3 * * *", retention.Sweep)
```

## Последствия

**Плюсы**
- Zero ops — не нужен cron-sidecar, не нужен systemd-timer, не нужен k8s CronJob.
- Единый контейнер — меньше moving parts (CLAUDE.md §3).
- Использует общий StateStore — никаких дублирующих DB-коннектов.
- Graceful: при shutdown sweep не в полёте — просто ctx cancel, следующий sweep завтра.
- Идемпотентен: DELETE по условию; повторный запуск — no-op.

**Минусы / trade-offs**
- Небольшое окно «лога не видно»: если daemon упал между sweep'ами, файлы/строки могут просрочиться на N часов лишних. Ок.
- При очень большой БД DELETE может держать блокировку — у нас объёмы ничтожные (< 10k строк/сутки), lock < 1 сек.
- `sweep` в той же goroutine-pool, что и digest — но время разное (03:00 vs 08:00), пересечение минимально. Если вдруг цикл digest «затянется» до 03:00 (R-1 + retry 15 мин) — `SkipIfStillRunning` в cron (ADR-0005) отложит sweep на сутки.

## Альтернативы

1. **External sidecar (find + -mtime -delete cron).** Отвергнуто: отдельный процесс, sync с схемой БД (кто удаляет `fingerprints`?), ломает single-container deploy.
2. **SQL triggers / events.** Отвергнуто: SQLite не поддерживает server-side scheduled events. `pragma auto_vacuum` — про VACUUM, не про retention.
3. **CronJob в k8s.** Отвергнуто для MVP: не всё деплоится в k8s (см. OQ-6).
4. **Retention по размеру (not age).** Отвергнуто: для fingerprints-истории нужен именно возраст, size-based ломает pattern-detection «новый паттерн за 7 дней».
5. **logrotate-style external tool.** Отвергнуто: пересекается с кодом, двойной источник истины.

## Ссылки

- `docs/plans/10-architecture.md §2.2` (observability/retention в контейнере).
- ADR-0005 (тот же cron, что и digest).
