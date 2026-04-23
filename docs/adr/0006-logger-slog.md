# ADR-0006: Логгер — `log/slog` (stdlib)

- **Статус:** Accepted
- **Дата:** 2026-04-23
- **Автор:** architect
- **Связанные ADR:** [ADR-0010](0010-internal-module-structure.md).

## Контекст

NFR-O1: структурированные логи JSON, уровни (`debug|info|warn|error`), `run_id` для корреляции.
NFR-S1/S2: секреты не должны попадать в логи; middleware маскирования.

Целевая среда: Go 1.22+, что включает `log/slog` в stdlib (появился в Go 1.21).

Типичные альтернативы: `zerolog`, `zap`, `logrus`.

## Решение

**`log/slog`** (stdlib), handler — `slog.NewJSONHandler` для продакшна, `slog.NewTextHandler` для dev.

Инициализация:
```go
var handler slog.Handler
switch cfg.LogFormat {
case "json":
    handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
        Level:       parseLevel(cfg.LogLevel),
        AddSource:   false, // включать только в dev
        ReplaceAttr: redactSecrets,
    })
case "text":
    handler = slog.NewTextHandler(os.Stdout, ...)
}
logger := slog.New(handler).With("service", "log_analyser", "version", buildVersion)
slog.SetDefault(logger)
```

### Контекстная корреляция

`run_id` проставляется через `slog.Logger.With("run_id", id)` при старте digest cycle и пробрасывается в `context.Context`-scoped logger внутри пайплайна.

### Маскирование секретов

`ReplaceAttr` hook:
```go
var secretKeys = map[string]struct{}{
    "tg_bot_token": {}, "vl_basic_pass": {}, "authorization": {},
}
func redactSecrets(groups []string, a slog.Attr) slog.Attr {
    if _, ok := secretKeys[strings.ToLower(a.Key)]; ok {
        return slog.String(a.Key, "***")
    }
    // маскируем URL с query-token
    if s, ok := a.Value.Any().(string); ok && containsSecretInURL(s) {
        return slog.String(a.Key, mustRedactURL(s))
    }
    return a
}
```

Тест (NFR-S2): `strings.Contains(renderedLog, realToken)` → ожидаем `false`.

### Sampling

В MVP — не применяем. Объём логов daemon'а низкий (< 100 msg/sec). Если появится flood (например, за счёт retry-loop), рассмотрим `samber/slog-sampling` (Context7 ID `/samber/slog-sampling`) как точечный wrapper. Не в v0.1.

## Последствия

**Плюсы**
- Zero 3rd-party deps — обязательство ADR-0010.
- Stdlib стабилен, обратно совместим, покрыт go test suite.
- JSON-handler готов из коробки, подходит для Loki/Elastic/Splunk.
- `Handler` interface позволяет легко обернуть (masking, sampling, multi-sink).
- `context.Context`-aware: `logger.InfoContext(ctx, ...)`.

**Минусы / trade-offs**
- Performance чуть ниже `zerolog`/`zap` (allocate-free). Для нас не узкое место — `logger.Info` вызывается десятки раз в секунду, не миллионы.
- Меньше готовых энтерпрайз-фич (auto-caller, sentry-integration), но они не нужны.
- API `slog` местами менее элегантен (`slog.Int`, `slog.String` vs `.Str("k", v)`). Привыкается.

## Альтернативы

1. **`rs/zerolog`.** Отвергнуто: внешняя зависимость без реальной выгоды. Производительность не нужна в наших объёмах.
2. **`uber-go/zap`.** Отвергнуто: ещё тяжелее (sugared API), внешняя dep, overkill.
3. **`sirupsen/logrus`.** Отвергнуто: официально deprecated in favor of newer loggers; slog решает те же задачи из stdlib.
4. **`samber/slog-sampling`, `samber/slog-formatter`.** Рассмотрено (Context7 High reputation). Не принимаем в v0.1, но держим в уме для flood-протекции в v0.2.

## Ссылки

- `pkg.go.dev/log/slog` (stdlib).
- Context7: `/samber/slog-sampling` — reference для будущих sampling нужд.
- `docs/plans/10-architecture.md §6.2` (sentinel errors, wrapping).
