# ADR-0008: Telegram клиент — собственный тонкий (~400 LOC)

- **Статус:** Accepted
- **Дата:** 2026-04-23
- **Автор:** architect
- **Связанные ADR:** [ADR-0012](0012-proxy-support.md), [ADR-0007](0007-http-client-vl.md).

## Контекст

Нужны методы Telegram Bot API (Context7 `/websites/core_telegram_bots_api`, High reputation, 615 сниппетов):
- `getMe` — для `/readyz`;
- `sendMessage` — cover (HTML, ≤ 4096 symbols);
- `sendDocument` — fallback и single-file;
- `sendMediaGroup` — основной способ отправки 5 файлов (2-10 элементов в альбоме).

Из требований FR-7, FR-14, R-2:
- **multipart upload** файлов до 50 МБ;
- поддержка `attach://<name>` для inputMediaDocument внутри multipart;
- **proxy support** (v0.2) — SOCKS5/HTTP через общий `httpclient.Factory`;
- **rate limit** — token bucket (даже если в MVP не упираемся, в v0.2 realtime упрёмся);
- работа только через приватный chat_id (NFR-S3).

Кандидаты на готовую библиотеку:
- `go-telegram-bot-api/telegram-bot-api/v5` (Context7 ID `/go-telegram-bot-api/telegram-bot-api`, **Medium**, Score **39** — сомнительно для prod);
- `go-telegram/bot` (Context7 ID `/websites/pkg_go_dev_github_com_go-telegram_bot`, High, Score 68.8, 434 snippets — больше фреймворк).

## Решение

**Собственный тонкий клиент** в `internal/delivery/telegram/`. ~400 LOC, покрывает строго 4 метода + multipart builder + rate limiter.

Структура:
```
internal/delivery/telegram/
  client.go       # *Client, NewClient(httpclient.Factory, token, chatID)
  send_message.go # SendMessage(ctx, html string) (*Message, error)
  send_document.go
  send_media_group.go
  multipart.go    # безопасный builder с attach://
  ratelimit.go    # token bucket: 30/s global, 1/s per chat (v0.2)
  errors.go       # TGError{method, code, retry_after}
  health.go       # GetMe()
```

### Multipart builder (подтверждено Context7 `/websites/core_telegram_bots_api`)

- `sendMediaGroup` принимает `media` (JSON array с `type`, `media: "attach://<name>"`), плюс multipart parts с именами `<name>` (binary).
- Все 5 файлов → 5 multipart parts + 1 JSON-часть с массивом `InputMediaDocument`.
- Caption для каждого файла — в массиве, не в part.

### Rate limiter

`golang.org/x/time/rate` (stdlib-рядом, `x/time` — extension). В MVP не критичен (1 cover/сутки), но:
- 30 tokens/sec global (per bot) — защита от любых будущих flood'ов.
- 1 token/sec per chat.

Это **не** зависимость на уровне бизнес-логики — `x/time/rate` мелкий, стандартный, допустим.

### Обработка `retry_after` (429)

Когда TG вернул 429 с `parameters.retry_after=N`, уважаем **этот** N, а не наш backoff. Context7 подтверждает наличие `retry_after` в Telegram responses.

## Последствия

**Плюсы**
- ~400 LOC, читается за 30 минут, весь код в голове команды.
- Контроль над multipart (важно для TG quirks с `attach://`).
- Proxy через `httpclient.Factory` — unified с VL-клиентом.
- Rate limit — наш, под наш usage pattern.
- Не зависим от сторонних мейнтейнеров, API Telegram меняется редко и back-compat.
- 3rd-party deps: только `x/time/rate` (микро-пакет из Go-экосистемы).

**Минусы / trade-offs**
- Пишем multipart руками (± 80 LOC). Риск багов → покрываем табличными тестами с byte-level сравнением.
- Новые API-методы (будет `sendPoll`? вряд ли) — дорабатываем сами.
- Нет готовых типов `Update`/`Message` — определяем только нужные подмножества.

## Альтернативы

1. **`go-telegram-bot-api/v5`.** Отвергнуто: Context7 `Medium/39` — низкий score и medium reputation, мейнтейнеры отвечают медленно, API-coverage неполный. NFR проекта требует надёжность state-критичного слоя.
2. **`go-telegram/bot` (`/websites/pkg_go_dev_github_com_go-telegram_bot`).** Отвергнуто: High reputation, но это **фреймворк** (middleware, handlers, Update dispatch) — нам избыточно. Мы — исключительно sender, не receiver. Зависимость с большим surface area.
3. **`gotd/td` / MTProto-клиенты.** Отвергнуто: это MTProto, не Bot API — избыточно, нужна аутентификация иначе.
4. **Прямой curl через `exec.Command`.** Отвергнуто: ) прокси, multipart, stream — все сложности пришлось бы реализовывать в shell.

## Ссылки

- Context7: `/websites/core_telegram_bots_api` — `sendMediaGroup`, `sendDocument`, `InputMediaDocument`, 50 МБ, multipart.
- Context7 (rejected): `/go-telegram-bot-api/telegram-bot-api`, `/websites/pkg_go_dev_github_com_go-telegram_bot`.
- `docs/plans/10-architecture.md §5.6`, §12.3, §12.6.
