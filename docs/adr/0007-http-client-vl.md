# ADR-0007: HTTP клиент VictoriaLogs — `net/http` + собственная обёртка

- **Статус:** Accepted
- **Дата:** 2026-04-23
- **Автор:** architect
- **Связанные ADR:** [ADR-0012](0012-proxy-support.md), [ADR-0010](0010-internal-module-structure.md).

## Контекст

VictoriaLogs — обычный HTTP-сервис (доки подтверждены Context7 `/victoriametrics/victorialogs`, см. `00-analysis.md §3.1`). Используем эндпоинты:
- `POST /select/logsql/query` — стрим JSON-lines;
- `POST /select/logsql/stats_query_range` — для cover-агрегатов;
- `GET /select/logsql/stream_field_names` — preflight (R-10).

Нужно:
- stream-парсинг JSON-lines (не грузить весь ответ в память, NFR-P3),
- retry с экспоненциальным backoff (R-1),
- опционально basic-auth,
- (v0.2) поддержка прокси для VL (OQ-2),
- маскирование URL с credentials в логах (NFR-S2).

## Решение

**Stdlib `net/http`** + собственная обёртка в `internal/httpclient/` + специализированный `internal/collector/vlclient.go`.

### Архитектура

- `httpclient.Factory` (см. `10-architecture.md §10`) выдаёт `*http.Client` с правильным `Transport` (прокси, timeouts, connection pool).
- `collector.VLClient` — тонкий wrapper поверх клиента, знает эндпоинты VL, авторизацию, retry policy.

### Retry policy

Экспоненциальный backoff **собственной** реализации (≤ 50 LOC):
- delays: 1s, 2s, 4s, 8s, 16s, 32s, cap 60s;
- total cap: 15 мин (NFR-R1);
- только для transient (5xx, сетевые ошибки, `context.DeadlineExceeded` на подключение);
- 4xx (кроме 429) — permanent, не retry.

Не тянем `cenkalti/backoff` — 50 LOC собственного кода стоят дешевле зависимости.

### Streaming

```go
resp, err := client.Do(req)
// ...
dec := json.NewDecoder(resp.Body)
for dec.More() {
    var ev Event
    if err := dec.Decode(&ev); err != nil { ... }
    out <- ev
}
```

JSON-lines VictoriaLogs — один объект на строку; `json.Decoder.More()` корректно обрабатывает.

### Timeouts

```go
&http.Client{
    Timeout: 0, // streaming не ограничиваем общим
    Transport: &http.Transport{
        MaxIdleConns: 5,
        IdleConnTimeout: 60 * time.Second,
        TLSHandshakeTimeout: 10 * time.Second,
        ResponseHeaderTimeout: 30 * time.Second, // VL должен ответить header'ом быстро
        ExpectContinueTimeout: 1 * time.Second,
    },
}
```

Не ставим `Client.Timeout` — убьёт стрим. Вместо этого — `ctx.WithTimeout(15*time.Minute)` на каждый запрос-per-host, передаваемый как `Request.Context()`.

### Маскирование

Middleware-декоратор `RoundTripper` логирует URL, предварительно прогоняя через `mustRedactURL`: query-параметры `extra_filters=...=token` маскируются, basic-auth user-info в URL → `***@host`.

## Последствия

**Плюсы**
- Zero 3rd-party deps (кроме будущего `x/net/proxy` — см. ADR-0012).
- Полный контроль — retry policy под наши requirements, не чужие defaults.
- Streaming работает корректно (важно для NFR-P3).
- Легко mocked в тестах (`httptest.Server`).
- Прозрачность — 100% понятно что куда и зачем идёт.

**Минусы / trade-offs**
- Пишем ретраер сами. Баги возможны, но тестируемо + < 100 LOC.
- Если VL изменит endpoints / формат — придётся править клиент, нет «либы которая знает про VL». В VL доки стабильны, риск низок.
- Нет автоматической circuit breaker — не требуется на MVP.

## Альтернативы

1. **`hashicorp/go-retryablehttp`.** Отвергнуто: полноценный retry, но свой `http.Client` тип (не `*http.Client`), что ломает interchangeability с `httpclient.Factory`. Объём кода, который мы избегаем — ~100 LOC. Не стоит зависимости.
2. **`cenkalti/backoff/v4`.** Отвергнуто: шикарная backoff-либа, но у нас экспоненциальный + cap — тривиально; плюс нам нужны transient-классификаторы под HTTP-коды, самописное проще.
3. **`github.com/VictoriaMetrics/VictoriaLogs/...` (native client, если есть).** Отвергнуто: на 2026-04-23 нет официального Go-клиента VL (см. Context7 `/victoriametrics/victorialogs` — там docs snippets, не Go SDK).
4. **gRPC.** Отвергнуто: VL не экспонирует gRPC для query API.

## Ссылки

- Context7: `/victoriametrics/victorialogs` — API endpoints.
- `pkg.go.dev/net/http`.
- `docs/plans/10-architecture.md §5.2`, §10.
