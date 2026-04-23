# ADR-0012: Прокси-поддержка (v0.2) — `golang.org/x/net/proxy` + `http.Transport.Proxy`

- **Статус:** Proposed (для v0.2.0)
- **Дата:** 2026-04-23
- **Автор:** architect
- **Связанные ADR:** [ADR-0007](0007-http-client-vl.md), [ADR-0008](0008-telegram-client-custom.md).

## Контекст

FR-14: в v0.2 поддержать прокси для исходящего TG-трафика (`TG_PROXY_URL`, SOCKS5/HTTP). OQ-2: нужен ли прокси для VL — оставлено открытым, но я ввожу сразу две независимые переменные (см. `10-architecture.md §12.7`): `TG_PROXY_URL`, `VL_PROXY_URL`. В среднем TG-прокси нужен (обход блокировок/корп-сеть), VL-прокси — если сервис в другой VLAN.

Форматы:
- HTTP/HTTPS: `http://user:pass@host:port`, `https://host:port`.
- SOCKS5/5h: `socks5://user:pass@host:port`, `socks5h://host:port`.

## Решение

**Единая абстракция `httpclient.Factory`** (см. `10-architecture.md §10`), внутри — switch по scheme:
- HTTP/HTTPS → `http.Transport.Proxy = http.ProxyURL(u)` (stdlib).
- SOCKS5 → `golang.org/x/net/proxy` (`proxy.FromURL` → `proxy.Dialer` → адаптировать в `http.Transport.DialContext`).

```go
import "golang.org/x/net/proxy"

func dialerFromURL(proxyURL string) (proxy.ContextDialer, error) {
    u, err := url.Parse(proxyURL)
    if err != nil { return nil, err }
    d, err := proxy.FromURL(u, proxy.Direct)
    if err != nil { return nil, err }
    cd, ok := d.(proxy.ContextDialer)
    if !ok { return nil, errors.New("dialer does not support context") }
    return cd, nil
}
```

В `http.Transport`:
```go
tr := &http.Transport{
    DialContext: contextDialer.DialContext,
    // ...
}
```

Для HTTP-прокси:
```go
tr := &http.Transport{ Proxy: http.ProxyURL(u), ... }
```

### Fallback / R-12

По умолчанию: при недоступности прокси — fail. При `TG_PROXY_FALLBACK=direct` (explicit opt-in) — retry без прокси. DLQ собирает недоставленные unit'ы независимо от прокси-mode.

### Маскирование в логах

Query-часть и userinfo в proxy-URL (`user:pass@`) — маскируются в логах через те же хуки (NFR-S2).

## Последствия

**Плюсы**
- `golang.org/x/net/proxy` — официальная точка входа в экосистеме Go, зрелая, часть `x/net` (semi-stdlib).
- Одна фабрика на весь проект — VL, TG, Grafana-links (генератор URL не ходит в сеть) используют единый слой.
- Zero vendor surprises — ни в одной ADR мы не берём «прокси-специфичную» библиотеку типа `h12io/socks`.
- HTTP и SOCKS5 — оба покрыты.

**Минусы / trade-offs**
- `golang.org/x/net` — отдельный модуль, требует явного `go get golang.org/x/net/proxy`. Это одна зависимость, но официальная.
- `proxy.FromURL` возвращает `proxy.Dialer`, не `proxy.ContextDialer`. Приходится type-assert. Если type-assert не прошёл — на практике не происходит, но код аккуратно возвращает error.
- Для `socks5h` (remote DNS) — всё работает из коробки в `x/net/proxy`.

## Альтернативы

1. **`armon/go-socks5`.** Отвергнуто: это **сервер** SOCKS5, не клиент.
2. **Собственный SOCKS5 реализовать.** Отвергнуто: протокол не сложный, но зачем — когда есть `x/net/proxy`.
3. **Только HTTP-прокси, без SOCKS5.** Отвергнуто: FR-14 прямо требует оба.
4. **Системные переменные `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY`.** Рассмотрено как fallback: `http.ProxyFromEnvironment`. Это **допускаем** для Grafana/VL (внутренний traffic), но TG — **всегда** через явный `TG_PROXY_URL`, потому что мы хотим детерминизм (не хотим, чтобы TG-traffic случайно утёк через общий corp-proxy).
5. **Подключить `net/http` через Unix socket к локальному прокси.** Отвергнуто: экзотика, нет в use-case.

## Ссылки

- `pkg.go.dev/golang.org/x/net/proxy`.
- `pkg.go.dev/net/http#Transport.Proxy`.
- `docs/plans/10-architecture.md §10`, §12.7.
