# 00. Системный анализ: Ежедневный Telegram-отчёт об ошибках торговых серверов

> **Issue:** [QCoreTech/awesome#908](https://github.com/QCoreTech/awesome/issues/908) «Ежедневный отчёт в Telegram об ошибках на торговых серверах».
> **Роль автора:** Senior Business / System Analyst.
> **Дата:** 2026-04-23. **Версия анализа:** 0.1 (pre-architecture).
> **Статус:** DRAFT — ожидает ответов по блоку [Открытые вопросы](#9-открытые-вопросы-oq).

---

## Оглавление

1. [Цель и ценность](#1-цель-и-ценность)
2. [Глоссарий](#2-глоссарий)
3. [Проверка документации (Context7)](#3-проверка-документации-context7)
   1. [VictoriaLogs](#31-victorialogs-logsql--http-api)
   2. [Telegram Bot API](#32-telegram-bot-api)
   3. [Grafana Explore URL schema](#33-grafana-explore-url-schema)
4. [Акторы и use-cases](#4-акторы-и-use-cases)
5. [Функциональные требования (FR)](#5-функциональные-требования-fr)
6. [Нефункциональные требования (NFR)](#6-нефункциональные-требования-nfr)
7. [Scope / Out-of-scope](#7-scope--out-of-scope)
8. [Предположения и риски (R)](#8-предположения-и-риски-r)
9. [Открытые вопросы (OQ)](#9-открытые-вопросы-oq)
10. [Метрики успеха](#10-метрики-успеха)
11. [Приоритизация MoSCoW](#11-приоритизация-moscow)
12. [Phased roadmap](#12-phased-roadmap)
13. [Входы для смежных ролей](#13-входы-для-смежных-ролей)
    1. [Архитектор](#131-для-senior-архитектора)
    2. [SRE](#132-для-sre)
    3. [DevOps](#133-для-devops)
    4. [QA](#134-для-qa)
14. [Связь DoD ↔ FR](#14-связь-dod--fr)
15. [Приложения](#15-приложения)

---

## 1. Цель и ценность

**Проблема:** разработчики не замечают ошибок уровня `error` / `critical` на 5 торговых серверах (`t1`, `ali-t1`, `t2`, `aws-t3`, `t5`), потому что логи лежат в VictoriaLogs и никто не ходит туда регулярно. Инциденты становятся видны только по результатам клиентских жалоб или сбоев торгов.

**Решение (MVP):** сервис-демон на Go, ежедневно в 08:00 MSK формирующий в Telegram:
- одно сообщение-обложку (cover) с суммарной статистикой за прошедшие 24 ч,
- 5 файлов-вложений `<host>_YYYY-MM-DD.<ext>`, в каждом — группировка по `app`/`module`, выделенные инциденты и паттерны, выдержки логов, рабочие Grafana-deeplinks.

**Ценность:**
- Leading: MTTA (mean time to acknowledge) по ошибкам торговых серверов падает с «случайно» до ≤ 24 ч гарантированно.
- Lagging: ↓ числа инцидентов, дошедших до клиента, за счёт раннего обнаружения паттернов.

---

## 2. Глоссарий

| Термин | Определение |
|---|---|
| **Инцидент** | Группа логов одного `host` + `app` + нормализованного сообщения (`fingerprint`), попавшая в отчётное окно. Минимальная единица, которую разработчик должен рассмотреть. |
| **Паттерн** | Регулярно повторяющаяся нормализованная сигнатура ошибки, встречающаяся ≥ N раз (конфигурируемый порог, по умолчанию N=10) и/или на ≥ 2 серверах. |
| **Fingerprint (дедуп-ключ)** | Хэш `sha1(host + app + normalize(_msg))`, где `normalize` убирает числа, UUID/hex-токены, IP, timestamps, trade/order IDs. База дедупликации. |
| **Шум-порог (noise threshold)** | Нижняя граница по `count`: инциденты с `count < K` (по умолчанию K=3) не попадают в основное тело отчёта, но учитываются в агрегатной сводке «прочее: N ошибок». |
| **Окно (window)** | Временной интервал отчёта: `[now_MSK − 24h, now_MSK)` при ежедневном прогоне. |
| **«Взять» окно** | Операция сбора: зафиксировать границы, сходить в VictoriaLogs, накопить в state. В daemon-режиме окно может формироваться инкрементально (см. FR-3). |
| **Сводка** | Содержимое отчётного файла: заголовок + агрегаты + топ-инцидентов + паттерны + выдержки. |
| **Обложка (cover)** | Короткое текстовое сообщение в TG-группу с агрегатами по серверам, к которому прикрепляются файлы. |
| **Delivery unit** | Атомарная единица доставки = cover + 5 attachments. Доставляется или целиком, или с явным indication об ошибке. |
| **Digest cycle** | Полный цикл: collect → dedupe → render → deliver → persist. |

---

## 3. Проверка документации (Context7)

> Все факты ниже подтверждены через MCP Context7 (`resolve-library-id` + `query-docs`) на 2026-04-23.

### 3.1. VictoriaLogs (LogsQL + HTTP API)

**Источник:** Context7 library `/victoriametrics/victorialogs` (High reputation, 806 snippets).
**Канонические ссылки:**
- LogsQL: <https://docs.victoriametrics.com/victorialogs/logsql/>
- Querying HTTP API: <https://docs.victoriametrics.com/victorialogs/querying/>

**Ключевые API-эндпоинты:**

| Endpoint | Назначение | Используем для |
|---|---|---|
| `GET/POST /select/logsql/query` | Стрим JSON-lines с логами по LogsQL-запросу. | Основной забор записей ошибок. |
| `POST /select/logsql/hits` | Гистограмма числа записей с шагом (для cover-агрегатов по часам/дням). | Быстрая агрегированная статистика. |
| `POST /select/logsql/stats_query` / `stats_query_range` | Агрегирующие расчёты (count, uniq, …). | Подсчёт «сколько ошибок по app/level на хост». |
| `GET /select/logsql/stream_field_values` | Значения полей стрима с числом попаданий. | Контроль, что все 5 хостов присутствуют. |

**_time filter — формально поддерживаемые формы** (цитата из `/victorialogs/LogsQL.md`):

```logsql
# Относительная длительность:
_time:24h

# Интервал [min, max):
_time:[2026-04-22T05:00:00Z, 2026-04-23T05:00:00Z)

# С таймзоной:
_time:[2026-04-22T08:00:00+03:00, 2026-04-23T08:00:00+03:00)

# Сдвиг окна:
_time:24h offset 0s
```

> «Specify the smallest possible time range to reduce scanned log entries. Prefer a single `_time` filter at the top level of the query for better performance» — официальная performance-рекомендация. Используем **один** `_time` фильтр наверху.

**Стрим-фильтр:** формат `{field="value"}` — быстрее обычного фильтра, т.к. пропускает блоки данных на уровне индексов. Поля стримов из issue: `host`, `app`, `level`.

**Пример целевого запроса MVP (на один сервер, за 24 ч):**

```logsql
{host="t5"} _time:[2026-04-22T05:00:00Z, 2026-04-23T05:00:00Z)
  AND (level:=error OR level:=critical)
```

**Пример cover-агрегата (распределение ошибок по хостам за 24 ч):**

```logsql
_time:24h (level:=error OR level:=critical)
  AND {host=~"t1|ali-t1|t2|aws-t3|t5"}
  | stats by (host, level) count() rows
```

**Пример «топ-app внутри хоста»:**

```logsql
{host="t5"} _time:24h (level:=error OR level:=critical)
  | stats by (app) count() rows
  | sort by (rows desc)
  | limit 20
```

**Ответ `/select/logsql/query`:** `Content-Type: application/x-jsonlines`, каждая строка — JSON с `_msg`, `_stream`, `_time` и произвольными полями (включая `host`, `app`, `level`).

> **Важно:** в issue поля `host`, `app`, `level` заявлены как поля стримов — значит стрим-фильтр `{host=...,app=...}` использовать можно, но `level` в стрим мог не попасть. Это — OQ-5.

### 3.2. Telegram Bot API

**Источник:** Context7 library `/websites/core_telegram_bots_api` (High, 615 snippets). Канон: <https://core.telegram.org/bots/api>.

**Ключевые факты (подтверждённые):**

| Метод / ограничение | Значение | Следствие |
|---|---|---|
| `sendMessage.text` | 1–4096 символов | Cover жёстко урезаем, детали — во вложения. |
| `sendDocument.document` | **до 50 МБ** (цитата: «Bots can currently send files of any type of up to 50 MB in size»). | Один файл на сервер почти гарантированно влезает. |
| `sendDocument.caption` | 0–1024 символа | Caption используем для мини-описания «t5 • 2026-04-23 • err=412 crit=3». |
| `sendMediaGroup.media` | **2–10 элементов, документы можно только с документами** | Идеальный способ отправить 5 файлов одним «альбомом». |
| `parse_mode` | `HTML`, `MarkdownV2`, `Markdown` (legacy). | Рекомендуется **HTML** (меньше экранирования, более предсказуем для автогенерации). |
| Rate limit (default) | «30 messages per second» суммарно; «1 message / second / group» для групп; Paid Broadcasts до 1000 msg/s (платно, 10000 Stars минимум). | В MVP не упираемся: 1 cover + 1 media group в сутки. При realtime alerts (v0.2) — нужен token bucket. |
| MarkdownV2 escaping | Экранировать `_ * [ ] ( ) ~ \` > # + - = \| { } . !` | Если всё-таки MDv2 — через библиотечный escaper, самописный — запрещено. |

**Рекомендация:** HTML parse_mode, `<pre>` для выдержек логов, `<code>` для имён `host`/`app`, `<a href="...">` для Grafana-ссылок.

### 3.3. Grafana Explore URL schema

**Источник:** Context7 `/grafana/grafana` (v11.2.2, High, 5056 snippets). Канон: <https://grafana.com/docs/grafana/latest/explore/get-started-with-explore/#generate-explore-urls-from-external-tools>.

**Формат (подтверждён документацией):**

```
https://<grafana_url>/explore?schemaVersion=1&orgId=<org>&panes=<url-encoded-json>
```

**`panes`** — URL-encoded JSON, ключ — pane ID (любой, например `"a"`):

```json
{
  "a": {
    "datasource": "<VL_DS_UID>",
    "queries": [
      {
        "refId": "A",
        "datasource": { "uid": "<VL_DS_UID>", "type": "victoriametrics-logs-datasource" },
        "expr": "{host=\"t5\"} (level:=error OR level:=critical)"
      }
    ],
    "range": {
      "from": "1745298000000",
      "to":   "1745384400000"
    }
  }
}
```

- `schemaVersion=1` — актуально.
- `from`/`to` — **миллисекунды Unix epoch** (или относительная форма `now-24h` / `now`).
- `type` VL-datasource зависит от установленного плагина (`victoriametrics-logs-datasource` либо legacy `victorialogs`) — см. OQ-3.

**Важные поля для deeplink:**
- Ссылка на конкретный инцидент = `expr` с fingerprint-нормализованным шаблоном ИЛИ точным `_msg` + `host` + `app` + узкое окно `[t_first, t_last + 1m]`.
- Ссылка в cover/attachment должна быть short & clickable; если URL > 4096 (лимит сообщения), либо прячем под `<a>`, либо шортим (в MVP — без шортера, URL пойдёт внутрь HTML-ссылки).

---

## 4. Акторы и use-cases

| Актор | Роль | Use-case |
|---|---|---|
| **Разработчик (dev)** | Первичный потребитель | UC-1: открыть TG утром, прочитать обложку, скачать файл своего сервиса, посмотреть топ-инцидентов, перейти по Grafana-deeplink. |
| **SRE** | Реакция на аномалии | UC-2: по cover'у увидеть пик (например, `t2: 8912 ошибок vs обычно 200`), открыть файл и Grafana, начать расследование. |
| **TeamLead / PO** | Тренды и здоровье | UC-3: смотреть только cover, отслеживать динамику за недели (прикинуть, улучшается ли ситуация). |
| **Админ / DevOps** | Эксплуатация | UC-4: менять набор хостов, пороги, chat_id, токен — через env/config без пересборки. UC-5: перезапускать daemon без потери накопленного state. |
| **Security** | Комплаенс | UC-6: убедиться, что токен/chat_id/URLs не уходят в логи, только в secret store. |

---

## 5. Функциональные требования (FR)

> Каждое требование помечено ссылкой на пункт DoD из issue (D1..D5) или на дополнительное пожелание TeamLead (T1..T7).

| # | Требование | Источник |
|---|---|---|
| **FR-1** | Сервис ежедневно в **08:00 MSK** (Europe/Moscow, корректно обрабатывать DST, хотя MSK её не использует с 2011) формирует и доставляет отчёт за окно `[now − 24h, now)`. | D1 |
| **FR-2** | Сервис запрашивает **только** записи `level in {error, critical}` по хостам `t1, ali-t1, t2, aws-t3, t5`. Список хостов — конфиг. | D1, D5 |
| **FR-3** | Режим работы — **long-running daemon** (не cron one-shot). Внутренний планировщик (`robfig/cron` или native ticker) запускает digest cycle. Между прогонами daemon может инкрементально вытягивать события и хранить их в локальном state (SQLite/BoltDB) для дедупа и инкрементального ingest. | T2 |
| **FR-4** | Для **каждого** из 5 хостов формируется **отдельный** файл `<host>_YYYY-MM-DD.<ext>` (формат — OQ-4, по умолчанию **Markdown** `.md`). Один общий файл — **не** создаём. | D2 |
| **FR-5** | Содержимое файла: (a) заголовок (host, дата, окно, итоги); (b) агрегаты по `app`/`module` (count error vs critical); (c) топ-N инцидентов (по умолчанию N=20), каждый = fingerprint + count + first/last seen + 1-2 выдержки `_msg`; (d) выделенные паттерны (повтор ≥ K раз или на ≥ 2 хостах); (e) блок «прочее/noise» с агрегатной цифрой; (f) Grafana-deeplink на весь хост и на каждый инцидент. | D2, D4 |
| **FR-6** | В Telegram-группу отправляется **cover-сообщение**: дата, окно, таблица «host → count(error) / count(critical) / top-app», ссылка на Grafana «всё за сутки». Cover ≤ 4096 симв. | D3 |
| **FR-7** | Пять файлов доставляются как **`sendMediaGroup`** из `InputMediaDocument` (2–10 элементов, группировка документов только с документами). При `sendMediaGroup` недоступности (чат не поддерживает, ошибка) — fallback в 5 последовательных `sendDocument`. | D3 |
| **FR-8** | **Все** Grafana-deeplinks должны открываться и показывать именно те записи, что упомянуты в файле (точность окна ≤ ±60с). | D4 |
| **FR-9** | Конфиг: `TG_BOT_TOKEN`, `TG_CHAT_ID`, `VL_URL`, `GRAFANA_URL`, `GRAFANA_ORG_ID`, `GRAFANA_VL_DS_UID`, список `HOSTS`, `SCHEDULE_CRON`, `TZ`, `LEVELS`, `NOISE_K`, `TOP_N` — через env + опциональный yaml/toml. Никаких хардкодов токенов. | D5 |
| **FR-10** | Дедупликация: `fingerprint = sha1(host + app + normalize(_msg))` с нормализацией (удаление timestamps, UUID, hex, IP, trade/order IDs через regex-profile). Два инцидента с одним fingerprint в одном окне считаются одним (агрегируются: count, first_seen, last_seen, примеры). | Best practice + «шум/дедуп — на усмотрение исполнителя» |
| **FR-11** | Шум-порог: инциденты с `count < NOISE_K` в основной блок не идут, учитываются в «прочее: N инцидентов, M записей». | Best practice |
| **FR-12** | Идемпотентность: повторный запуск digest cycle за **уже отправленное** окно не приводит к дублю в TG. Маркер отправки хранится в state. | NFR (reliability) |
| **FR-13** | **(v0.2)** Realtime alert-канал: при появлении `critical`-сообщения с fingerprint, не встречавшимся за последние 7 дней, отправить в **отдельный** TG-thread одиночный алерт с throttle. | T3 |
| **FR-14** | **(v0.2)** Поддержка прокси для исходящего TG-трафика: SOCKS5 / HTTP(S). Конфигурируется `TG_PROXY_URL`. Прокси для VL-трафика — отдельный OQ-2. | T4 |
| **FR-15** | **(v0.3)** Read-only UI: список инцидентов за N дней, фильтры (host, app, level, pattern), ретроспектива. Авторизация — на усмотрение архитектора (OQ-9). | T5 |
| **FR-16** | Cover-агрегаты должны поддерживать формат «t5: ошибок такого типа N, других M», т.е. группировка по топ-pattern + `others`. | T6 |
| **FR-17** | **(опционально, v0.2)** LLM-резюме: 3–5 предложений по каждому хосту, встраиваемые в начало файла. При недоступности LLM — секция отсутствует, отчёт всё равно уходит. | Issue: «желательно краткое LLM-резюме» |
| **FR-18** | Retention отчётов: отправленные файлы хранятся локально N дней (default 30) для перегонки/аудита; старше — чистятся. | NFR + ops |
| **FR-19** | CLI-подкоманды: `run` (daemon), `once --date=YYYY-MM-DD` (ручной прогон за произвольные сутки — manual re-send), `health` (проверка VL + TG), `version`. | Ops usability |

---

## 6. Нефункциональные требования (NFR)

### 6.1. Надёжность / устойчивость

- **NFR-R1.** При недоступности VL — экспоненциальный retry (до 15 минут), после — пропуск текущего окна, но запись в `failed_runs` + алерт в **тот же** TG-чат с тегом `[delivery-failed]` о пропуске.
- **NFR-R2.** При недоступности TG — retry с backoff (1s, 2s, 4s, …, до 5 минут), сохранение payload (cover + files) на диск и дозаправка при восстановлении (dead-letter queue локально).
- **NFR-R3.** При рестарте daemon внутри digest cycle — возобновить с последней успешной фазы (collect / render / deliver), не отправлять дубль (FR-12).
- **NFR-R4.** «Циклическая поломка» (например, шаблон крашится) не должна положить весь daemon: per-host рендер изолирован, сбой одного хоста → отчёт по остальным 4 уходит + `[partial-report]` маркер в cover.

### 6.2. Производительность

- **NFR-P1.** Целевая ёмкость: до **5 млн записей / сутки / хост** × 5 = 25 млн строк. На практике ожидаем ≤ 10% это уровня `error/critical` (~2.5 млн). Это оценочное — подтвердить у заказчика (OQ-10).
- **NFR-P2.** Digest cycle должен уложиться в **10 минут** wall-clock для всех 5 хостов. Использовать concurrent fetch (по 1 goroutine на host).
- **NFR-P3.** Память daemon < 512 МБ в MVP (стриминг JSON-lines из VL, не загружать весь ответ в память).

### 6.3. Безопасность

- **NFR-S1.** Секреты (`TG_BOT_TOKEN`, опционально VL-basic-auth) — только env или из secret store (Vault/k8s secret). **Никогда** не логировать.
- **NFR-S2.** Маскирование в логах: при попадании токена в URL — `***` (middleware для HTTP клиента).
- **NFR-S3.** Telegram-файлы могут содержать тексты логов → потенциально PII / чувствительные торговые данные. Публичные каналы — запрещены, только приватный chat_id с контролируемым составом.
- **NFR-S4.** Docker image — non-root, readonly rootfs, distroless или alpine-slim.

### 6.4. Локализация / таймзоны

- **NFR-TZ1.** Вся бизнес-логика в `Europe/Moscow`. Внутри сервиса — UTC, конверсия **только** на входе/выходе (рендер, cron trigger).
- **NFR-TZ2.** Формат дат в именах файлов: `YYYY-MM-DD` в MSK (день, **заканчивающийся** в момент запуска).
- **NFR-TZ3.** Язык отчётов — русский (соответствует issue и команде).

### 6.5. Наблюдаемость

- **NFR-O1.** Структурированные логи JSON (`slog`/`zerolog`), уровни, `run_id` для корреляции.
- **NFR-O2.** Prometheus `/metrics`: `digest_cycle_duration_seconds`, `digest_cycle_total{status}`, `vl_query_errors_total`, `tg_send_errors_total{method}`, `incidents_total{host,level}`, `dedup_cache_size`, `last_successful_delivery_timestamp_seconds`.
- **NFR-O3.** Health: `/healthz` (live), `/readyz` (VL reachable + TG bot getMe OK).

### 6.6. Идемпотентность / state

- **NFR-I1.** State store (OQ-7): SQLite по умолчанию, схема `runs(run_id, window_from, window_to, status, tg_cover_message_id)`, `fingerprints(fp, host, app, first_seen, last_seen, count_7d)`, `dead_letter(payload, created_at, retries)`.
- **NFR-I2.** Один «owner» на state-файл (file lock), чтобы не запустить случайно два daemon'а на одной БД.

### 6.7. Retention

- **NFR-Ret1.** Файлы отчётов — 30 дней локально.
- **NFR-Ret2.** Fingerprints — 90 дней (чтобы понимать «новый паттерн или старый»).
- **NFR-Ret3.** Metrics — по политике Prometheus (external).

---

## 7. Scope / Out-of-scope

### 7.1. In-scope MVP (v0.1.0)

- Daemon + scheduler + digest cycle + dedup + render + TG delivery + Grafana deeplinks + Prometheus metrics + healthchecks + конфиг через env + Docker image + GitHub CI/CD.

### 7.2. Out-of-scope MVP

- **UI** — v0.3.
- **LLM-резюме** — опционально в v0.2 (или вынести совсем отдельно).
- **Realtime alerts** — v0.2.
- **ML-классификация инцидентов** (выходит за «паттерны по regex»). Не планируется.
- **Roles/ACL в TG** — считаем, что канал приватный и корректно сконфигурирован снаружи.
- **Поддержка произвольных источников логов** (Loki, ELK) — только VictoriaLogs.
- **Хранение полных копий логов в сервисе** — нет, только fingerprints + примеры (в рендере).
- **Замена полноценных алертинговых систем** (Alertmanager, Grafana Alerting) — это digest-утилита, не замена.

---

## 8. Предположения и риски (R)

Формат: `R-N | описание | Вероятность (L/M/H) | Воздействие (L/M/H) | Митигация`.

| # | Риск | В | В | Митигация |
|---|---|---|---|---|
| **R-1** | VictoriaLogs недоступна/медленная в момент запуска → отчёт не уйдёт. | M | H | Retry policy, `[delivery-failed]` алерт, CLI `once` для ручного повторного прогона. |
| **R-2** | Telegram API rate limit / блокировка бота. | L | H | Token bucket (30 msg/s глобально, 1 msg/s/chat), в MVP ненагружен, но код уже с лимитером. |
| **R-3** | Размер суточного файла по «шумному» серверу > 50 МБ. | M | M | (a) агрессивный dedup; (b) truncation с маркером `[файл усечён до 45 МБ, остальное в Grafana: <link>]`; (c) zip/gzip (TG принимает). |
| **R-4** | Нормализация fingerprint'а «склеивает» разные ошибки (false dedup) или наоборот (false split). | H | M | Конфигурируемый regex-профиль, A/B: за первую неделю сверять fp → \_msg вручную, затюнить. |
| **R-5** | Grafana-deeplink «мёртвый» (datasource UID протух, схема URL поменялась). | M | H | Smoke-тест в CI: парс URL + валидация схемы через e2e против dev-Grafana (OQ-3). |
| **R-6** | MSK в Docker-контейнере не установлен (tzdata missing). | M | M | В Dockerfile явно `RUN apk add tzdata` + `ENV TZ=Europe/Moscow` + unit-тест на парс timezone. |
| **R-7** | Перезапуск daemon во время `sendMediaGroup` → «полотчёта» в TG. | L | M | Идемпотентный marker в state до отправки (pre-commit), после успеха — commit. Дубли фильтруются по `run_id`. |
| **R-8** | Утечка токена в логи / в Grafana-deeplinks. | L | H | Маскирование в HTTP middleware, unit-тест на `strings.Contains(log, token)`. |
| **R-9** | LLM-резюме (v0.2) выдаёт галлюцинации про инциденты → вводит в заблуждение. | H | M | Резюме чисто статистическое (шаблон-промпт), temperature=0, пометка `[auto-summary]`, disable flag. |
| **R-10** | Поля `host`/`app`/`level` оказались **не** полями стрима, а просто полями логов → стрим-фильтр `{host=...}` бесполезен, перформанс упадёт. | M | M | В preflight-чекапе daemon'а запрашивать `/select/logsql/stream_field_names` и выдавать warning в лог + документации + OQ-5. |
| **R-11** | Объём логов >> ожидаемого (например, 100 млн/сутки) → daemon не успевает в окно. | L | M | Chunked fetch (по часам), горизонтальный sharding по хостам (одна инстанс-на-хост), scale-out в v0.2. |
| **R-12** | Прокси для TG недоступен в момент отправки. | M | M | Fallback: прямое подключение (если whitelisted) либо отложенная дозаправка. Настраивается. |

---

## 9. Открытые вопросы (OQ)

> **До старта имплементации обязательно получить ответ на: OQ-1, OQ-3, OQ-4, OQ-5, OQ-8.** Остальные можно уточнять по ходу v0.1.

| # | Вопрос | К кому | Блокер для |
|---|---|---|---|
| **OQ-1** | URL VictoriaLogs (prod/read-only endpoint), нужна ли авторизация (basic auth / bearer / mTLS). | TeamLead / SRE | v0.1 (collect) |
| **OQ-2** | Нужен ли прокси для исходящих запросов к **VictoriaLogs**? Если VL внутри корпсети, а daemon снаружи — нужен bastion/tunnel. | SRE | v0.1 |
| **OQ-3** | Grafana URL, `orgId`, UID VictoriaLogs-datasource и точный `type` плагина (`victoriametrics-logs-datasource` / `victorialogs` / кастомный). | DevOps / Grafana-admin | v0.1 (deeplink) |
| **OQ-4** | Формат файла отчёта: `.md` (человекочитаемый, рендерится в TG превью), `.html` (красивее в браузере), `.txt` (универсально), `.gz`-архив с `.log`? **Предложение:** `.md` по умолчанию, опция в конфиге. | TeamLead / Dev | v0.1 (render) |
| **OQ-5** | В VictoriaLogs `host`, `app`, `level` — **поля стрима** или обычные поля лога? Нужен точный синтаксис (разные операторы дают разную производительность). | SRE / VL-admin | v0.1 (query perf) |
| **OQ-6** | Где именно запускается daemon: k8s (Deployment/CronJob-hybrid), Docker host, systemd-unit? Нужен для выбора liveness/readyness и shutdown hooks. | DevOps | v0.1 (deploy) |
| **OQ-7** | Хранилище state: SQLite-файл в persistent volume (MVP) vs Postgres (overkill?) vs BoltDB. **Предложение:** SQLite + WAL. | Архитектор | v0.1 (state) |
| **OQ-8** | TG chat_id — один чат с прикреплёнными файлами, или отдельные threads в супергруппе (`message_thread_id`)? Если threads — нужен topic-id под realtime (v0.2). | TeamLead | v0.1 (delivery) |
| **OQ-9** | Размер TG-файла сверху: официально до 50 МБ через Bot API (подтверждено); нужен ли локальный Bot API Server для 2 ГБ? **По умолчанию — нет.** | TeamLead | v0.1 (edge-case) |
| **OQ-10** | Объём логов error/critical в сутки на хост (порядок)? 1 млн? 10 млн? 100 млн? Влияет на perf-бюджет и chunking. | SRE | v0.1 (perf) |
| **OQ-11** | Политика секретов: env-vars Docker, k8s Secret, Vault? При Vault — нужны `vault agent` / инжектор. | Security / DevOps | v0.1 (sec) |
| **OQ-12** | LLM: какой провайдер (OpenAI-compatible endpoint, локальный `llama.cpp`, внутренний сервис)? Бюджет, SLA, PII-политика (можно ли отправлять выдержки логов во внешний LLM?). | Security / TeamLead | v0.2 |
| **OQ-13** | Для v0.3 UI: отдельный веб-сервис или встроенный в daemon? Требования к auth (LDAP/OIDC/basic)? | Архитектор | v0.3 |
| **OQ-14** | Период realtime throttle: сколько раз в минуту бот может слать один и тот же critical? Пороги escalation? | SRE | v0.2 |
| **OQ-15** | «Рабочие дни» или 24/7? Торговля идёт 24/5 — стоит ли отдельный scheduler для субботы/воскресенья (может, digest тише)? | TeamLead | v0.1 (nice-to-have) |

---

## 10. Метрики успеха

### 10.1. Leading (операционные)

| Метрика | Цель | Измерение |
|---|---|---|
| `delivery_success_rate` | ≥ 99% за месяц | `sum(status=ok) / total` по `digest_cycle_total` |
| `delivery_latency_p95` | ≤ 10 мин от 08:00 MSK | `digest_cycle_duration_seconds` |
| `incidents_rendered_per_run` | корректно отражает VL-состояние | cross-check raw count vs rendered |
| `grafana_deeplink_validity` | 100% | sample-check в CI (v0.2) |

### 10.2. Lagging (бизнес)

| Метрика | Цель | Источник |
|---|---|---|
| MTTA по ошибкам торгсерверов | ≤ 24 ч | retrospective review |
| Доля клиентских инцидентов, «пойманных» через digest | ≥ 30% | пост-морт анализ |
| Удовлетворённость команды (опрос) | ≥ 4/5 | ежеквартальный опрос |

---

## 11. Приоритизация MoSCoW

- **Must have (v0.1.0):** FR-1, FR-2, FR-3, FR-4, FR-5, FR-6, FR-7, FR-8, FR-9, FR-10, FR-11, FR-12, FR-18, FR-19; все NFR-R, NFR-S, NFR-TZ, NFR-O; Dockerfile + GitHub Actions CI/CD.
- **Should have (v0.2.0):** FR-13 (realtime), FR-14 (proxy), FR-16 (агрегаты «типа X / других Y» — часть MVP, но улучшение топ-N паттернов — здесь), NFR-P2 на реальной нагрузке.
- **Could have (v0.2 / v0.3):** FR-17 (LLM), FR-15 (UI read-only), advanced retention UI.
- **Won't have (MVP):** ML-классификация, замена Alertmanager, универсальный log-source коннектор, public TG broadcast.

---

## 12. Phased roadmap

> SemVer + gitflow. Основная ветка `master`, фичи `feat/*`, релизы `release/v*`.

### v0.1.0 — MVP digest (target: 2 спринта)

**Scope:** все MUST HAVE. Без LLM, без realtime, без прокси, без UI.

**DoD v0.1.0:**
1. Daemon запущен в docker, крутится > 7 дней без рестартов.
2. 7 подряд успешных daily deliveries (cover + 5 файлов).
3. ≥ 1 сверка с VL вручную: содержимое файла соответствует реальным логам.
4. Все 5 Grafana-deeplinks проверены кликом, открывают нужные данные.
5. CLAUDE.md, CHANGELOG.md, VERSION, README — на месте.
6. CI зелёный (lint, test, build, image push).

**Критерий перехода на v0.2:** стабильные 7 дней + нет open S1/S2 багов + получен sign-off от TeamLead.

### v0.2.0 — Realtime + proxy + (опц.) LLM

**Scope:** FR-13, FR-14, FR-17 (behind flag), token bucket rate limiter, e2e-тест Grafana-deeplinks.

**DoD v0.2.0:** те же критерии + критический alert доставляется < 60с от `_time` лога + прокси проверен на SOCKS5 + LLM-резюме выключается одним флагом.

**Критерий перехода на v0.3:** 2 недели стабильной работы realtime; анализ false-positive rate по паттернам < 10%.

### v0.3.0 — Read-only UI

**Scope:** FR-15. Вебка со списком инцидентов, фильтрами, ретроспективой. Reuse state store.

**DoD v0.3.0:** UI доступен авторизованным (OQ-13), покрывает 100% поля за 30 дней, пагинация, экспорт в .md.

---

## 13. Входы для смежных ролей

### 13.1. Для senior-архитектора

**Решения, которые нужно принять и обосновать в ADR (`docs/adr/`):**

1. **ADR-01.** State store: SQLite (WAL) vs BoltDB vs Postgres. **Рекомендация аналитика:** SQLite + WAL (единая persistent-файловая БД, бэкап = копия файла, embedded).
2. **ADR-02.** Формат отчёта: `.md` (default) vs `.html`. **Рекомендация:** `.md` — рендерится в TG preview, универсально, легко diff'ается.
3. **ADR-03.** Templating engine для рендера: `text/template` (stdlib) vs `html/template` vs внешний (templ). **Рекомендация:** `text/template` + ручной escaping (минимум зависимостей).
4. **ADR-04.** Планировщик: `robfig/cron/v3` vs ручной `time.Ticker` + timezone arithmetic. **Рекомендация:** `robfig/cron/v3` с явным `TZ=Europe/Moscow`.
5. **ADR-05.** Логгер: `log/slog` (stdlib) vs `zerolog`. **Рекомендация:** `log/slog` (Go 1.21+, no deps).
6. **ADR-06.** HTTP клиент для VL: `net/http` + retry-wrapper (`cenkalti/backoff`). **Рекомендация:** `net/http` + свой interface для mock в тестах.
7. **ADR-07.** TG клиент: `go-telegram-bot-api/telegram-bot-api` (v5) vs собственный тонкий клиент. **Рекомендация:** лёгкий собственный клиент — нужны только `sendMessage`, `sendDocument`, `sendMediaGroup`, `getMe`; проще поддерживать прокси/rate-limit; меньше зависимостей.
8. **ADR-08.** Dedup fingerprint algorithm: какие regex-профили нормализации (UUID, ISO timestamp, IPv4, hex ≥ 8, trade/order IDs). Вынести в YAML-profile.
9. **ADR-09.** Модульность: пакеты `internal/collector`, `internal/dedup`, `internal/render`, `internal/delivery`, `internal/scheduler`, `internal/state`, `internal/config`, `internal/observability`.
10. **ADR-10.** Конфиг: `envconfig` (kelseyhightower) vs `viper`. **Рекомендация:** `envconfig` (проще, без магии).
11. **ADR-11.** Прокси-саппорт: `golang.org/x/net/proxy` для SOCKS5 + стандартный `http.Transport.Proxy` для HTTP.
12. **ADR-12.** Retention cleanup: in-process goroutine vs side-car. **Рекомендация:** in-process тикер раз в сутки (простота).

### 13.2. Для SRE

**SLO-черновик (v0.1):**

| SLI | Target | Error budget (30d) |
|---|---|---|
| Успешность доставки ежедневного digest | ≥ 99.0% (допустимо 1 пропуск в месяц) | ~1 run |
| `digest_cycle_duration_seconds` p95 | ≤ 10 мин | — |
| Доступность `/healthz` | ≥ 99.5% | ~3 ч/мес |

**Что мониторить (дашборд):**
- `digest_cycle_total{status}` — counter cycles by status.
- `digest_cycle_duration_seconds` — histogram.
- `vl_query_errors_total`, `vl_query_duration_seconds`.
- `tg_send_errors_total{method}`, `tg_send_retry_total`.
- `incidents_rendered_total{host,level}`.
- `dead_letter_queue_size` — **алерт** если > 0 дольше 10 минут.
- `last_successful_delivery_timestamp_seconds` — **алерт** если `time() - metric > 26h` (на случай пропуска).
- `fingerprint_cache_size`, `fingerprint_eviction_total`.

**Runbook-points (для SRE):**
- «Digest не ушёл» — проверить `last_successful_delivery_timestamp_seconds`, `/readyz`, логи за окно, DLQ.
- «Пик ошибок» — проверить, не изменился ли уровень логгирования на сервере, не flood ли.
- «TG 429» — временно снизить rate (rate-limit env), дождаться окна.

### 13.3. Для DevOps

**CI/CD требования:**
- GitHub Actions workflow (`.github/workflows/ci.yml`):
  - `on: [pull_request, push to master, release]`.
  - Jobs: `lint` (golangci-lint), `test` (`go test -race -cover`), `build` (cross-compile linux/amd64 + linux/arm64), `docker` (buildx multi-arch), `security` (trivy image scan, gosec).
  - На тэге `v*.*.*` — релизный workflow: build → push to GHCR → GitHub Release с changelog-автогеном.
- GPG-подписанные коммиты (`-S`), conventional commits на русском (как в CLAUDE.md пользователя).
- Dependabot / Renovate для Go-модулей.
- Semantic Versioning:
  - MAJOR — несовместимые изменения конфига (ENV rename, формат state).
  - MINOR — новая фича (realtime, proxy, UI).
  - PATCH — баги.

**Deploy-требования (MVP):**
- Docker image: multi-stage, `FROM gcr.io/distroless/static:nonroot` или `alpine:3.20`.
- Healthchecks в Docker + в k8s.
- Ресурсы (предложение): `requests: 100m CPU / 128Mi RAM`, `limits: 500m / 512Mi`.
- Volume для state (`/var/lib/log_analyser/state.db`) и отчётов (`/var/lib/log_analyser/reports/`).
- Secrets: env-injection через k8s Secret / docker secret / Vault agent (OQ-11).

### 13.4. Для QA

**Ключевые приёмочные сценарии:**

| # | Scenario | Expected |
|---|---|---|
| **T-1** | Happy path: 5 хостов × логи есть × VL OK × TG OK. | 1 cover + 5 attachments в чате, имена файлов корректны, deeplinks кликаются. |
| **T-2** | Один хост молчал — в VL 0 error/critical. | Файл для него **всё равно** есть с пометкой «За окно ошибок не обнаружено»; в cover — `t5: 0 / 0`. |
| **T-3** | VL вернул 5xx. | Retry ×3, затем `[delivery-failed]` алерт в чат, daemon живёт. |
| **T-4** | TG 429. | Backoff, retry до успеха, метрика инкрементится. |
| **T-5** | Повторный запуск daemon в окне уже отправленного отчёта. | Дубля нет, в логах `already delivered run_id=...`. |
| **T-6** | «Шумный» хост → файл > 50 МБ. | Truncation с маркером, доставка ОК. |
| **T-7** | DST switch / смена часового пояса хоста daemon'а. | Отчёт всё равно в 08:00 MSK. |
| **T-8** | Нормализация: 1000 строк `error processing trade #123 for user <uuid>` агрегируются в 1 инцидент с count=1000. | Один incident, один fingerprint. |
| **T-9** | Grafana-deeplink: клик → открывается Explore с корректным окном ±60с. | Вручную + авто-тест URL схемы. |
| **T-10** | Перезапуск middle of delivery. | Pre-commit marker есть, второй запуск дубля не шлёт. |
| **T-11** | CLI `once --date=2026-04-15`. | Ручная отправка отчёта за произвольные сутки, не затирает «ежедневный» slot. |
| **T-12** | Все обязательные env не заданы → graceful fail с понятным error message. | Exit 2, сообщение в stderr с перечнем missing vars. |

---

## 14. Связь DoD ↔ FR

| DoD (из issue) | Покрывают FR |
|---|---|
| D1 «ежедневно 08:00 MSK за 24ч» | FR-1, FR-2 |
| D2 «отдельный файл на каждый из 5 серверов» | FR-4, FR-5 |
| D3 «сообщение-обложка + вложения» | FR-6, FR-7 |
| D4 «рабочие Grafana Explore ссылки» | FR-5(f), FR-8 |
| D5 «токен/chat_id/серверы в env/конфиг» | FR-9 |

Все пункты DoD покрыты. Дополнительные пожелания TeamLead:
- T1 «Go+Docker+CI/CD» → см. раздел 13.3.
- T2 «daemon» → FR-3.
- T3 «realtime» → FR-13 (v0.2).
- T4 «proxy» → FR-14 (v0.2).
- T5 «UI» → FR-15 (v0.3).
- T6 «агрегаты типа X / других Y» → FR-16.
- T7 «CLAUDE.md, CHANGELOG, SemVer, gitflow» → раздел 13.3.

---

## 15. Приложения

### 15.1. Каркас шаблона отчёта (`<host>_YYYY-MM-DD.md`)

```markdown
# Отчёт по ошибкам: <host>
**Период:** YYYY-MM-DD 08:00 MSK — YYYY-MM-DD 08:00 MSK (24 ч)
**Всего:** error=<N_err>, critical=<N_crit>, инцидентов (fingerprints)=<N_inc>, паттернов=<N_pat>

## Краткая сводка
- Топ app по числу ошибок: `app-a` (512), `app-b` (201), `app-c` (87), …
- Новых паттернов за сутки: 3
- Ссылка на все логи за окно: [Grafana →](https://...)

## Агрегаты по app
| app | error | critical | всего | доля |
|-----|-------|----------|-------|------|
| app-a | 500 | 12 | 512 | 62% |
| ...

## Топ-20 инцидентов
### 1. [`app-a`] connection refused to <addr>
- **count:** 412 (34% error от хоста)
- **first_seen:** 2026-04-22 08:14:03 MSK
- **last_seen:** 2026-04-23 07:49:51 MSK
- **fingerprint:** `a3f9c...`
- **выдержка:**
  ```
  2026-04-22T08:14:03.123Z ERROR app-a: connection refused to 10.0.1.45:8443
  ```
- **Grafana:** [посмотреть 412 событий →](https://...)

### 2. ...

## Паттерны (повтор ≥ 10 раз или на ≥ 2 хостах)
- `auth: invalid token for user <id>` — 87 раз, app-a/app-b
- ...

## Прочее (ниже шум-порога)
**174 инцидентов**, **231 запись**. [Открыть в Grafana →](https://...)
```

### 15.2. Каркас cover-сообщения

```html
<b>Ежедневный отчёт об ошибках</b>
<b>Период:</b> 22.04.2026 08:00 — 23.04.2026 08:00 MSK

<b>Итог по серверам:</b>
<pre>
host       error  crit   топ-app
t1         1 204    12    order-svc
ali-t1       412     3    md-gateway
t2         8 912    45    exec-engine
aws-t3        87     0    risk-engine
t5         2 041    18    fix-gw
</pre>

<b>Всего:</b> 12 656 ошибок / 78 critical
<b>Новых паттернов:</b> 7

<a href="https://grafana.../explore?...">Открыть всё в Grafana →</a>

(далее 5 прикреплённых файлов)
```

### 15.3. Пример Grafana-deeplink (MSK → epoch ms)

```
https://grafana.example.com/explore?schemaVersion=1&orgId=1&panes=%7B
  %22a%22%3A%7B
    %22datasource%22%3A%22<UID>%22%2C
    %22queries%22%3A%5B%7B
      %22refId%22%3A%22A%22%2C
      %22datasource%22%3A%7B%22uid%22%3A%22<UID>%22%2C%22type%22%3A%22victoriametrics-logs-datasource%22%7D%2C
      %22expr%22%3A%22%7Bhost%3D%5C%22t5%5C%22%7D%20(level%3A%3Derror%20OR%20level%3A%3Dcritical)%22
    %7D%5D%2C
    %22range%22%3A%7B%22from%22%3A%221745298000000%22%2C%22to%22%3A%221745384400000%22%7D
  %7D
%7D
```

### 15.4. Чеклист перед стартом разработки

- [ ] OQ-1 (VL URL + auth) — ответ есть
- [ ] OQ-3 (Grafana URL + DS UID + plugin type) — ответ есть
- [ ] OQ-4 (формат файла) — зафиксирован (по умолчанию `.md`)
- [ ] OQ-5 (поля стрима) — проверено на реальном VL
- [ ] OQ-8 (chat_id / threads) — зафиксирован
- [ ] ADR-01..12 заполнены архитектором
- [ ] CLAUDE.md, CHANGELOG.md, VERSION созданы
- [ ] Репозиторий создан в `QCoreTech/*`, `master` protected, GPG signing required

---

**Автор анализа:** Claude (senior analyst mode).
**Следующий шаг:** передать архитектору для ADR'ов по пунктам раздела 13.1.
