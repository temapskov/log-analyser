# ADR-0005: Scheduler — `robfig/cron/v3` с явным `Europe/Moscow`

- **Статус:** Accepted
- **Дата:** 2026-04-23
- **Автор:** architect
- **Связанные ADR:** [ADR-0011](0011-config-envconfig-with-yaml-overlay.md) (env TZ), [ADR-0010](0010-internal-module-structure.md).

## Контекст

По FR-1: daemon ежедневно в 08:00 **Europe/Moscow** формирует digest. FR-3: режим long-running daemon, не cron one-shot. FR-19: CLI `once` — ручной прогон за произвольные сутки.

Требования:
- Поддержка IANA-таймзон и корректный DST (на случай изменения политик MSK или работы в других TZ).
- Cron-expression в env (`SCHEDULE_CRON=0 8 * * *`).
- Graceful shutdown: текущий running-job может дождаться окончания (или отмениться по ctx).

## Решение

**`github.com/robfig/cron/v3`** (Context7 ID `/robfig/cron`, High reputation, Score 83.65).

Инициализация:
```go
loc, err := time.LoadLocation(cfg.TZ) // default "Europe/Moscow"
if err != nil { return err }
c := cron.New(
    cron.WithLocation(loc),
    cron.WithLogger(cron.VerbosePrintfLogger(slogAdapter)),
    cron.WithChain(
        cron.Recover(slogAdapter),          // не валим daemon от panic в job
        cron.SkipIfStillRunning(slogAdapter),// нельзя запустить второй cycle, пока первый идёт
    ),
)
```

Per-job override возможен через `CRON_TZ=` в expression (подтверждено Context7):
```go
c.AddFunc("CRON_TZ=Europe/Moscow 0 8 * * *", job)
```

Но мы полагаемся на `WithLocation` — оно ясно, единая TZ для всех jobs.

## Последствия

**Плюсы**
- High reputation, Go modules, поддерживается годами.
- Явный `WithLocation` решает проблему DST / TZ-переходов (зимнее/летнее время) — Context7 подтвердил, что библиотека уважает зоны.
- `SkipIfStillRunning` — встроенная защита от наложения циклов: если digest ещё идёт в 08:00, следующий 08:00 завтра не стартанёт второй параллельно.
- `Recover` — panic в одной job не валит daemon.
- Формат cron — стандартный 5-field (minute, hour, dom, month, dow). В v3 убрано путающее «seconds» поле по умолчанию.

**Минусы / trade-offs**
- +1 внешняя зависимость. Альтернатива — `time.Timer` с ручным вычислением next-fire-time. Но TZ-арифметика с DST вручную — источник багов (R-6).
- `robfig/cron` не поддерживает семантики типа «в первую пятницу месяца» без расширений. Нам не нужно.
- Нет persistent schedule (при рестарте cron'а расписание всё в памяти). Для нас ок — расписание из env/yaml.

## Альтернативы

1. **Ручной `time.Ticker` + `time.NowIn(loc)`.** Отвергнуто: нужно самому реализовать TZ-aware next-fire, DST-корректность, overlap-protection. Это велосипед → багоопасно. Context7 цитата из `robfig/cron` README: «v3 ... cleans up rough edges like the timezone support».
2. **`jasonlvhit/gocron`.** Отвергнуто: менее mature, меньше users, своеобразный DSL.
3. **`go-co-op/gocron`.** Рассмотрено: хорошая библиотека, богатый API. Но нам нужен минимум — 1 job + CLI trigger. Overkill для MVP.
4. **Systemd timer / k8s CronJob.** Отвергнуто: FR-3 прямо требует daemon-режим (state continuity между циклами, DLQ, потенциально realtime в v0.2). One-shot cron недопустим.
5. **`CRON_TZ=` через env-prefix в экспрешшене, без `WithLocation`.** Отвергнуто как дефолт: мы хотим явный fail-fast если TZ некорректен (TZ data не установлен в контейнере — R-6).

## Ссылки

- Context7 `/robfig/cron`: `WithLocation`, `CRON_TZ`, upgrade v3 notes.
- `pkg.go.dev/github.com/robfig/cron/v3`.
- `docs/plans/10-architecture.md §5.1`.
