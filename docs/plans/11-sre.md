# 11. SRE-план: SLO, observability, runbook, capacity

> **Роль автора:** Senior SRE.
> **Проект:** `log_analyser` (Go-daemon, ежедневный Telegram-digest ошибок с 5 торговых серверов через VictoriaLogs).
> **Дата:** 2026-04-23. **Версия:** 0.1 (pre-implementation).
> **Связанные документы:** `docs/plans/00-analysis.md` (аналитик), будущий `docs/plans/10-architecture.md`, `docs/plans/12-devops.md`.
> **Issue:** [QCoreTech/awesome#908](https://github.com/QCoreTech/awesome/issues/908).

> Все факты про Prometheus client, Grafana SLO-рекомендации, Telegram Bot API rate-limits и VictoriaLogs query-деталей сверены через Context7 MCP 2026-04-23 и процитированы в тексте при первом появлении.

---

## Оглавление

1. [Принципы SRE-дизайна](#1-принципы-sre-дизайна)
2. [SLI / SLO каталог](#2-sli--slo-каталог)
3. [Error budget policy](#3-error-budget-policy)
4. [Burn-rate alerts (multi-window, multi-burn-rate)](#4-burn-rate-alerts-multi-window-multi-burn-rate)
5. [Метрики — полный каталог](#5-метрики--полный-каталог)
6. [Логи](#6-логи)
7. [Tracing (v0.2, nice-to-have)](#7-tracing-v02-nice-to-have)
8. [Healthchecks](#8-healthchecks)
9. [Алерты Prometheus — rule'ы в YAML](#9-алерты-prometheus--ruleы-в-yaml)
10. [Runbook](#10-runbook)
11. [Capacity planning](#11-capacity-planning)
12. [Disaster recovery / backup](#12-disaster-recovery--backup)
13. [Dead-man switch](#13-dead-man-switch)
14. [Chaos engineering (v0.2+)](#14-chaos-engineering-v02)
15. [Security SRE](#15-security-sre)
16. [Расхождения с аналитиком](#16-расхождения-с-аналитиком)
17. [Чек-лист «ready for prod»](#17-чек-лист-ready-for-prod)

---

## 1. Принципы SRE-дизайна

1. **Главный KPI — факт доставки cover+5-файлов в TG ≤ 26 ч после предыдущей успешной доставки.** Всё остальное (latency цикла, p95, healthz) — вторично. Это цель пользователя, а не сервис-левел.
2. **Scheduler-driven сервис — специальный зверь.** В отличие от RPS-сервиса, здесь SLI считается «от события» (1 ожидаемая доставка в сутки), а не «от запросов». Burn-rate нужно пересчитывать под это.
3. **Dead-man switch обязателен.** Если daemon завис и тихо не шлёт — сам сервис об этом не скажет. Нужен внешний наблюдатель за `last_successful_delivery_timestamp_seconds`.
4. **Белый ящик > чёрный ящик.** `/metrics` + структурированные логи. Чёрный ящик (синтетический ping VL + TG) — только как дополнение.
5. **Пер-хост изоляция.** Падение 1 из 5 хостов ≠ провал SLO. Учитываем это в SLI-определении.
6. **Error budget без burn-rate в MVP не имеет смысла** при 30 доставках/месяц (при 99.0% бюджет = 0.3 run). Окно удлиняем до 28 дней rolling, считаем вероятностно (см. §3).

---

## 2. SLI / SLO каталог

Все SLO измеряются на **28-дневном rolling окне** (28 = ровно 4 недели, стабильнее календарного месяца с его 28-31 днём). Цель — не выделять отдельный «первый бизнес-день месяца» как стартовую точку.

| # | SLI (short) | Формальное определение | SLO | Error budget (28d) | Приоритет |
|---|---|---|---|---|---|
| **SLO-1** | `digest_delivery_success_rate` | Доля суточных окон, в которых **cover + ≥4 файла из 5** доставлены в TG до 09:00 MSK текущего дня. Отсутствие доставки = провал. | **≥ 96.4%** (допустим 1 пропуск за 28 дней) | 1 run (= 1 пропущенный день) | **P0** — главный KPI |
| **SLO-2** | `digest_cycle_latency_p95` | p95 `digest_cycle_duration_seconds` (от start цикла до последнего успешного `sendMediaGroup`). | **≤ 600 сек (10 мин)** в 95% циклов | 1.4 циклов в 28d | P1 |
| **SLO-3** | `healthz_availability` | Доля минут за 28d, где `/healthz` возвращал 200 OK при scraping раз в 30 сек. | **≥ 99.5%** | ~3.4 ч / 28d | P2 |
| **SLO-4** | `vl_query_success_rate` | Доля HTTP-запросов к VL `/select/logsql/query`, завершившихся `2xx` за ≤ `VL_QUERY_TIMEOUT`. | **≥ 99.0%** | 1% запросов | P1 |
| **SLO-5** | `tg_send_success_rate` | Доля TG API-вызовов (`sendMessage` / `sendDocument` / `sendMediaGroup` / `getMe`), завершившихся `ok:true` **не учитывая штатные 429 с последующим retry-успехом**. | **≥ 99.5%** | 0.5% вызовов | P1 |

### 2.1. Формулы SLI в PromQL

```promql
# SLO-1: digest delivery success rate (28d)
# Успех = инкремент digest_cycle_total{status="ok"}, провал = status in {failed, partial}.
# "partial" (≥4 файла из 5) допустим в MVP, считаем за успех.
sli:digest_delivery_success_rate:28d =
  (
    sum(increase(digest_cycle_total{status=~"ok|partial"}[28d]))
  )
  /
  (
    sum(increase(digest_cycle_total[28d]))
  )

# SLO-2: digest cycle latency p95 (rolling 7d для стабильности histogram_quantile)
sli:digest_cycle_latency_p95:7d =
  histogram_quantile(
    0.95,
    sum by (le) (rate(digest_cycle_duration_seconds_bucket{status="ok"}[7d]))
  )

# SLO-3: healthz availability (28d) — считается Prometheus-ом из blackbox_exporter
sli:healthz_availability:28d =
  avg_over_time(probe_success{job="log-analyser-healthz"}[28d])

# SLO-4: VL query success rate (28d)
sli:vl_query_success_rate:28d =
  1 - (
    sum(increase(vl_query_errors_total[28d]))
    /
    sum(increase(vl_query_total[28d]))
  )

# SLO-5: TG send success rate (28d)
sli:tg_send_success_rate:28d =
  1 - (
    sum(increase(tg_send_errors_total{retried!="true"}[28d]))
    /
    sum(increase(tg_send_total[28d]))
  )
```

> Эти выражения — кандидаты в recording rules (interval 1m для latency, 5m для success rates — последние меняются медленно).

---

## 3. Error budget policy

**Единица бюджета:**
- SLO-1: 1 «провал дня» = 1 неудачная digest-доставка (cover + <4 файлов доставлены за окно 08:00–09:00 MSK).
- SLO-2: 1 цикл > 10 мин wall-clock.
- SLO-3/4/5: доля, измеряется в PromQL.

**Политика (по мотивам Google SRE workbook):**

| Состояние бюджета | Действие |
|---|---|
| **Budget ≥ 50%** | Нормальная разработка, фичи разрешены. |
| **Budget 50% → 25%** | Приоритизировать надёжность. Новые фичи с impact на digest-путь — требуют ревью SRE. |
| **Budget 25% → 0%** | Feature freeze на `master`. Принимаются только `fix/*`, `hotfix/*`, `chore/*`. Обязательный postmortem на каждый инцидент. |
| **Budget < 0%** | Полный freeze до конца окна + публичный postmortem + обязательное action-item «Не случится снова». Релизы только через explicit approval TeamLead. |

**Восстановление бюджета:** rolling 28d окно сдвигается автоматически, budget восстанавливается по мере «устаревания» провалов.

**Документирование инцидентов:** каждый провал SLO-1 (пропуск доставки) → запись в `docs/incidents/YYYY-MM-DD-<slug>.md` с 5-why + action items.

---

## 4. Burn-rate alerts (multi-window, multi-burn-rate)

> По мотивам Google SRE Workbook «The Art of SLOs». Цель — ловить сжигание бюджета ДО того, как он уйдёт в 0.

**Замечание, которое важно для данного сервиса:** классический multi-burn-rate спроектирован для RPS-сервисов с высоким QPS. Наш сервис — **1 событие/сутки на SLO-1**. Для SLO-1 burn-rate считаем иначе:

- За 28d бюджет = 1 провал.
- «2 провала подряд» = 200% бюджета за 2 дня = немедленный ticketing alert.
- «1 провал» = 100% бюджета за 1 день = warning + investigate.

Для SLO-2/3/4/5 (high-frequency) используем классическую формулу из SRE workbook:

### 4.1. SLO-1 (digest delivery) — специальные правила

```yaml
# Fast alert: 1 пропуск доставки подряд = warning.
# Триггер через 26 часов без успешной доставки (DigestOverdue).
# Для SLO-1 burn-rate формула не нужна — достаточно absolute overdue.

# Slow alert: 2 неудачных дня за 7 дней = critical (это >= 100% бюджета 28d за 7 дней).
- alert: DigestFailureRateHighSlow
  expr: |
    sum(increase(digest_cycle_total{status=~"failed|partial"}[7d]))
      / sum(increase(digest_cycle_total[7d]))
      > 2/7
  for: 1h
  labels:
    severity: critical
    slo: digest_delivery
  annotations:
    summary: "Digest: 2+ провала за 7 дней (сгорает бюджет 28d)"
    runbook: "docs/plans/11-sre.md#R-2"
```

### 4.2. SLO-2 (digest_cycle_latency_p95) — классический multi-burn

```yaml
# Fast burn: 2× бюджета за 1 час (1h window, взгляд 5m)
- alert: DigestLatencySLOBurnFast
  expr: |
    (
      histogram_quantile(0.95, sum by (le) (rate(digest_cycle_duration_seconds_bucket[1h]))) > 600
    ) and (
      histogram_quantile(0.95, sum by (le) (rate(digest_cycle_duration_seconds_bucket[5m]))) > 600
    )
  for: 2m
  labels:
    severity: warning
    slo: digest_latency
    burn_rate: fast

# Slow burn: 6× бюджета за 6 часов (6h window, взгляд 30m)
- alert: DigestLatencySLOBurnSlow
  expr: |
    (
      histogram_quantile(0.95, sum by (le) (rate(digest_cycle_duration_seconds_bucket[6h]))) > 600
    ) and (
      histogram_quantile(0.95, sum by (le) (rate(digest_cycle_duration_seconds_bucket[30m]))) > 600
    )
  for: 15m
  labels:
    severity: warning
    slo: digest_latency
    burn_rate: slow
```

> **Нюанс:** при 1 цикле/сутки 1h и 5m окна для `rate()` будут пустыми большую часть времени. Выражение фактически даёт алерт ровно когда предыдущий цикл был медленным. Это ОК для нашего кейса — больше смысл имеет absolute threshold «последний run > 10 мин», а `burn-rate` оставляем для v0.2, когда добавится realtime (FR-13).

### 4.3. SLO-4/SLO-5 (high-frequency VL/TG вызовы) — классический multi-burn

```yaml
# VL query error budget burn, 2× fast (1h / 5m) и 6× slow (6h / 30m)
- alert: VLQueryErrorBudgetBurnFast
  expr: |
    (
      sum(rate(vl_query_errors_total[1h])) / sum(rate(vl_query_total[1h])) > (1 - 0.99) * 14.4
    ) and (
      sum(rate(vl_query_errors_total[5m])) / sum(rate(vl_query_total[5m])) > (1 - 0.99) * 14.4
    )
  for: 2m
  labels:
    severity: critical
    slo: vl_query
    burn_rate: fast
  annotations:
    summary: "VL error-rate: ≥14.4× от SLO за 1ч — бюджет сгорит за 2 суток"
    runbook: "docs/plans/11-sre.md#R-4"

- alert: VLQueryErrorBudgetBurnSlow
  expr: |
    (
      sum(rate(vl_query_errors_total[6h])) / sum(rate(vl_query_total[6h])) > (1 - 0.99) * 6
    ) and (
      sum(rate(vl_query_errors_total[30m])) / sum(rate(vl_query_total[30m])) > (1 - 0.99) * 6
    )
  for: 15m
  labels:
    severity: warning
    slo: vl_query
    burn_rate: slow

# TG send error budget burn (те же коэффициенты, SLO 99.5% → (1-0.995)=0.005)
- alert: TGSendErrorBudgetBurnFast
  expr: |
    (
      sum(rate(tg_send_errors_total{retried!="true"}[1h])) / sum(rate(tg_send_total[1h])) > 0.005 * 14.4
    ) and (
      sum(rate(tg_send_errors_total{retried!="true"}[5m])) / sum(rate(tg_send_total[5m])) > 0.005 * 14.4
    )
  for: 2m
  labels:
    severity: critical
    slo: tg_send
    burn_rate: fast
```

**Коэффициенты burn-rate** (стандарт из SRE workbook):

| Burn multiplier | Long window | Short window | For | Alert severity |
|---|---|---|---|---|
| 14.4× | 1h | 5m | 2m | critical (page) |
| 6× | 6h | 30m | 15m | warning (ticket) |
| 3× | 24h | 2h | 1h | warning (ticket, долгое тление) |
| 1× | 72h | 6h | 3h | info (только в рабочее время) |

В MVP v0.1 реализуем **только 14.4× и 6×** для SLO-4 и SLO-5. Остальные — v0.2.

---

## 5. Метрики — полный каталог

### 5.1. Требования к cardinality

- Total active series ≤ **200** в MVP. На 5 хостов, 2 уровня, ~10 статусов — с запасом.
- Label `host` — closed set (5 значений). `app` в метрики **не** кладём (это высокая cardinality, рендерим только в файл).
- Label `fingerprint` — **никогда** в Prometheus-метрики.
- Label `run_id` — **никогда** в Prometheus-метрики (только в логах).

### 5.2. Полный список

| Метрика | Тип | Labels | Описание | Cardinality | Риск |
|---|---|---|---|---|---|
| `digest_cycle_total` | counter | `status={ok,failed,partial,skipped}` | Общее число digest-циклов по исходу. | 4 | низкий |
| `digest_cycle_duration_seconds` | histogram | `status={ok,failed,partial}` | Длительность полного цикла от collect до последнего TG ack. **Buckets:** `[30, 60, 120, 180, 300, 600, 900, 1200, 1800, 3600]` сек. | 4×11=44 | низкий |
| `digest_phase_duration_seconds` | histogram | `phase={collect,dedup,render,deliver}` | Длительность каждой фазы. **Buckets:** `[1, 5, 15, 30, 60, 120, 300, 600]` сек. | 4×9=36 | низкий |
| `vl_query_total` | counter | `endpoint={query,hits,stats},host` | Общее число запросов к VL. | 3×5=15 | низкий |
| `vl_query_errors_total` | counter | `endpoint,host,reason={timeout,5xx,4xx,network,parse}` | Ошибки VL-запросов. | 3×5×5=75 | средний — контролировать `reason` enum |
| `vl_query_duration_seconds` | histogram | `endpoint,host` | Длительность одного VL-запроса. **Buckets:** `[0.1, 0.5, 1, 2, 5, 10, 30, 60, 120]` сек. | 3×5×10=150 | средний |
| `vl_query_rows_returned` | histogram | `host` | Сколько строк вернул VL за один вызов. **Buckets:** `[100, 1e3, 1e4, 1e5, 1e6, 5e6]`. | 5×7=35 | низкий |
| `tg_send_total` | counter | `method={sendMessage,sendDocument,sendMediaGroup,getMe}` | Общее число TG-вызовов. | 4 | низкий |
| `tg_send_errors_total` | counter | `method,reason={429,4xx,5xx,timeout,network},retried={true,false}` | Ошибки TG-вызовов. `retried=true` — та, после которой была успешная попытка. | 4×5×2=40 | средний |
| `tg_send_duration_seconds` | histogram | `method` | Длительность TG-вызова. **Buckets:** `[0.1, 0.5, 1, 2, 5, 10, 30, 60]` сек. | 4×9=36 | низкий |
| `tg_send_retry_total` | counter | `method,reason` | Счётчик re-попыток TG (отдельно от errors). | 4×5=20 | низкий |
| `tg_rate_limited_total` | counter | — | 429 ответы от TG (дубликат подмножества `errors`, но удобно для алертов). | 1 | низкий |
| `incidents_rendered_total` | counter | `host,level={error,critical}` | Сколько инцидентов (уникальных fingerprint'ов) попало в отчёт. | 5×2=10 | низкий |
| `records_scanned_total` | counter | `host` | Сколько сырых записей прошло через dedup. | 5 | низкий |
| `dead_letter_queue_size` | gauge | — | Размер DLQ (неотправленные payload'ы). | 1 | низкий. **Алерт:** > 0 дольше 10 мин. |
| `last_successful_delivery_timestamp_seconds` | gauge | — | Unix-timestamp последней успешной доставки (cover+≥4 файла). | 1 | низкий. **Алерт:** `time() - metric > 26*3600`. |
| `last_cycle_start_timestamp_seconds` | gauge | — | Unix-timestamp старта последнего цикла. | 1 | низкий |
| `fingerprint_cache_size` | gauge | — | Текущий размер in-memory кэша fingerprint'ов. | 1 | низкий |
| `fingerprint_cache_evictions_total` | counter | `reason={ttl,size}` | Эвикции из кэша. | 2 | низкий |
| `state_db_size_bytes` | gauge | — | Размер SQLite-файла. | 1 | низкий. **Алерт:** > 1 GB. |
| `scheduler_tick_total` | counter | `outcome={fired,skipped_locked,skipped_already_done}` | Сработка cron-тикера. | 3 | низкий |
| `scheduler_next_fire_timestamp_seconds` | gauge | — | Когда scheduler планирует сработать в следующий раз. | 1 | низкий |
| `build_info` | gauge | `version,commit,go_version,built_at` | Информация о сборке. Всегда `1`. | 1 (фиксирован на build) | низкий |
| `go_*`, `process_*` | — | — | Стандартные collector'ы из `prometheus/client_golang`. | ~40 | низкий |

> **Итого кастомных series:** ~500 в worst case (с учётом histogram buckets). Стандартные collector'ы добавляют ~40. Overhead `/metrics` — <10 KB, scrape не влияет на цикл.

### 5.3. Обоснование buckets для `digest_cycle_duration_seconds`

- **NFR-P2** из analysis: «digest cycle ≤ 10 мин». Нужен bucket точно на 600с для p95.
- Нижняя граница: 30с (быстрый цикл при почти пустых логах).
- Верхняя: 3600с (1ч) — всё что дольше, скорее всего вечный зависон, отдельный алерт.
- Bucket на 1200с (20 мин) — порог «цикл явно сломан, но ещё не вечен».
- Шаг подобран так, чтобы p95 при цели 600с попадал в явный bucket, а не в середину интервала.

**Buckets:** `[30, 60, 120, 180, 300, 600, 900, 1200, 1800, 3600]` сек + `+Inf`.

### 5.4. Что НЕ метрика (умышленно)

- **Сами ошибки логов** торговых серверов — это бизнес-данные, они идут в файл отчёта, но НЕ в Prometheus. Кардинальность убьёт сервер.
- **Grafana-deeplinks** — не метрика.
- **Содержимое cover** — не метрика, валидация через QA.

---

## 6. Логи

### 6.1. Формат

- **Logger:** `log/slog` из stdlib (зафиксировано в `CLAUDE.md §2`).
- **Формат вывода:** JSON (`slog.NewJSONHandler`).
- **Уровень по умолчанию:** `INFO`; в dev-контейнере `DEBUG`.
- **Stderr** для всех логов, stdout — зарезервирован для возможного CLI-вывода.

### 6.2. Обязательные поля (на каждой записи)

| Поле | Тип | Источник | Пример |
|---|---|---|---|
| `time` | RFC3339Nano | slog | `2026-04-23T08:00:00.123456+03:00` |
| `level` | string | slog | `INFO` / `WARN` / `ERROR` |
| `msg` | string | разработчик | `digest cycle completed` |
| `service` | string | build-time | `log_analyser` |
| `version` | string | build-time | `0.1.0` |
| `run_id` | UUIDv7 | на старте цикла | `01JBXYZ...` |
| `host` | string | имя хоста daemon (НЕ торгового) | `k8s-worker-03` |
| `phase` | enum | текущая фаза цикла | `collect` / `dedup` / `render` / `deliver` / `cleanup` |
| `target_host` | string | торговый хост, если применимо | `t5` |
| `duration_ms` | int64 | при завершении операции | `8142` |

### 6.3. Что логировать на каком уровне

| Событие | Level | Поля помимо обязательных |
|---|---|---|
| Старт daemon | INFO | `build_info`, все важные конфиг-значения с маской секретов |
| Scheduler зафайрил digest | INFO | `window_from`, `window_to`, `targets` |
| Scheduler tick пропущен | WARN | `reason={already_delivered,lock_held}` |
| Старт/конец фазы | INFO | `phase`, `duration_ms` (только на конце) |
| VL запрос успешный | DEBUG | `endpoint`, `rows`, `bytes`, `duration_ms` |
| VL ошибка retriable | WARN | `endpoint`, `attempt`, `error`, `retry_after_ms` |
| VL ошибка fatal (исчерпан retry) | ERROR | `endpoint`, `error`, `last_attempt` |
| TG запрос | DEBUG | `method`, `payload_bytes`, `duration_ms` |
| TG 429 | WARN | `method`, `retry_after`, `attempt` |
| TG ошибка fatal | ERROR | `method`, `error`, `payload_saved_to_dlq={true,false}` |
| Фингерпринт-нормализация выдала пустую строку | WARN | `host`, `app`, `msg_sample` |
| Digest success (cover+5 files OK) | INFO | `run_id`, `tg_cover_message_id`, `total_duration_ms` |
| Digest partial | WARN | `run_id`, `failed_hosts`, `total_duration_ms` |
| Digest failed | ERROR | `run_id`, `error`, `phase_at_failure`, `dlq_added={true,false}` |
| DLQ drained | INFO | `drained_count`, `remaining` |
| DLQ stuck | WARN | `oldest_age_seconds`, `size` |
| State DB migration | INFO | `from_version`, `to_version` |
| Health probe failed | WARN | `probe={healthz,readyz}`, `check_name`, `error` |
| Неизвестная ошибка (panic recovered) | ERROR | `stacktrace`, `run_id` |
| Shutdown signal | INFO | `signal`, `uptime_seconds` |

### 6.4. Примеры log-line

```json
{"time":"2026-04-23T08:00:00.000+03:00","level":"INFO","msg":"digest cycle started","service":"log_analyser","version":"0.1.0","run_id":"01JBX6G3F...","host":"k8s-worker-03","phase":"init","window_from":"2026-04-22T08:00:00+03:00","window_to":"2026-04-23T08:00:00+03:00","targets":["t1","ali-t1","t2","aws-t3","t5"]}

{"time":"2026-04-23T08:00:01.340+03:00","level":"DEBUG","msg":"vl query ok","service":"log_analyser","version":"0.1.0","run_id":"01JBX6G3F...","phase":"collect","target_host":"t5","endpoint":"query","rows":12450,"bytes":2841929,"duration_ms":1340}

{"time":"2026-04-23T08:02:15.891+03:00","level":"WARN","msg":"tg rate limited, backing off","service":"log_analyser","version":"0.1.0","run_id":"01JBX6G3F...","phase":"deliver","method":"sendDocument","retry_after":3,"attempt":1}

{"time":"2026-04-23T08:02:41.217+03:00","level":"INFO","msg":"digest cycle completed","service":"log_analyser","version":"0.1.0","run_id":"01JBX6G3F...","phase":"deliver","total_duration_ms":161217,"files_sent":5,"cover_message_id":84271}

{"time":"2026-04-23T08:09:12.004+03:00","level":"ERROR","msg":"digest cycle failed","service":"log_analyser","version":"0.1.0","run_id":"01JBX6G3F...","phase":"deliver","error":"telegram: 400 Bad Request: chat not found","phase_at_failure":"deliver","dlq_added":true}
```

### 6.5. Маскирование секретов

- В HTTP middleware (и для VL, и для TG) — redact `Authorization`, `token=...` query-param, `&api_key=...`.
- URL с токеном бота (`https://api.telegram.org/bot<TOKEN>/...`) логируем как `https://api.telegram.org/bot***/sendMessage`.
- В логах **никогда** не должно появиться: `TG_BOT_TOKEN`, VL-basic-auth, содержимое `_msg` (это бизнес-данные; если нужно — только первые 120 символов с маркером `truncated`).

---

## 7. Tracing (v0.2, nice-to-have)

**Library:** OpenTelemetry Go SDK (`go.opentelemetry.io/otel`). Экспорт через OTLP/HTTP в коллектор.

**Spans:**

```
digest_cycle (attrs: run_id, window_from, window_to)
├── collect (attrs: targets_count)
│   ├── vl.query (attrs: target_host="t5", endpoint, rows, bytes)
│   ├── vl.query (target_host="t1", ...)
│   └── ... (parallel × 5)
├── dedup (attrs: total_records, unique_fingerprints)
├── render (attrs: files_count)
│   ├── render.host (target_host="t5", incidents, patterns, bytes)
│   └── ... (× 5)
└── deliver
    ├── tg.sendMessage (attrs: cover_bytes)
    └── tg.sendMediaGroup (attrs: files_count, total_bytes)
```

**Sampling:** в v0.2 — 100% (цикл раз в сутки, стоимость мизер). В realtime-режиме (FR-13) — head sampling 10% для штатных событий, 100% для критичных алертов.

**Correlation:** `run_id` из slog-лога совпадает с `trace_id` (инжектим явно через `trace.WithAttributes`).

> В v0.1 — tracing не делаем. Логи + метрики покрывают 95% SRE-задач.

---

## 8. Healthchecks

### 8.1. `/healthz` — liveness

**Цель:** «процесс жив, не зависший». Должен быть **быстрым и дешёвым**, без внешних вызовов. Нужен для k8s `livenessProbe` / docker `HEALTHCHECK`, чтобы рестартовать контейнер при hang.

**Контракт:** `GET /healthz` → `200 OK` с `{"status":"ok","uptime_sec":...}` или `503` если сломано.

**Чек-лист внутри `/healthz`:**

| Check | Timeout | Failure → | Почему |
|---|---|---|---|
| Main goroutine жива (канал-heartbeat тикает раз в 5 сек) | 1s | 503 | Ловим deadlock в main loop |
| Scheduler goroutine жива (метрика `scheduler_next_fire_timestamp_seconds` обновлялась ≤ 60 сек назад или в будущем) | 1s | 503 | Ловим мёртвый scheduler |
| Не в panic-recovery loop (счётчик panic за последний час ≤ 3) | 1ms | 503 | Ловим crash-loop |

**Что НЕ проверяем:** VL, TG, SQLite disk — это readyz-темы.

**Частота опроса:** раз в 10 сек (k8s `livenessProbe.periodSeconds=10`, `failureThreshold=3`). Умирание =  3 подряд fail = рестарт.

### 8.2. `/readyz` — readiness

**Цель:** «сервис готов принять работу / обслужить следующий цикл». Внешние зависимости проверяются. Нужен для k8s `readinessProbe` — исключение из балансировки (для нашего daemon'а балансировки нет, но если добавим multiple instances / leader election — будет актуально).

**Контракт:** `GET /readyz` → `200 OK` с деталями проверок или `503` с описанием провалившихся.

**Response example:**

```json
{
  "status": "ok",
  "checks": {
    "vl_reachable": {"status": "ok", "latency_ms": 42, "url": "https://vl.example.com"},
    "tg_getme": {"status": "ok", "latency_ms": 180, "bot_username": "log_analyser_bot"},
    "state_db_writable": {"status": "ok", "latency_ms": 1},
    "reports_dir_writable": {"status": "ok", "latency_ms": 1},
    "disk_space_reports": {"status": "ok", "available_gb": 42.3, "threshold_gb": 1.0}
  },
  "version": "0.1.0",
  "uptime_sec": 86400
}
```

**Чек-лист внутри `/readyz`:**

| Check | Действие | Timeout | Cache TTL | Failure → |
|---|---|---|---|---|
| `vl_reachable` | `GET {VL_URL}/select/logsql/query?query=*&limit=1&_time=5m` | 5s | 30s | 503 |
| `tg_getme` | `GET {TG_API}/bot{TOKEN}/getMe` | 5s | 60s | 503 |
| `state_db_writable` | `BEGIN; INSERT INTO readyz_probe(ts) VALUES(?); ROLLBACK;` | 1s | 10s | 503 |
| `reports_dir_writable` | создать и удалить tempfile в `REPORTS_DIR` | 500ms | 30s | 503 |
| `disk_space_reports` | `statfs(REPORTS_DIR)`, fail если < 1 GB free | 100ms | 60s | 503 |

> **Кэширование важно:** /readyz скрейпается blackbox'ом раз в 30 сек; без кэша получаем 30+ запросов в минуту на VL и TG-getMe. С кэшом — ~2 запроса/мин.

> **Критично:** `TG_BOT_TOKEN` в URL getMe **нельзя** логировать. HTTP middleware уже чистит URL от токена.

### 8.3. `/metrics`

- Экспортируется на том же порту, что healthz (предлагаю **отдельный порт** `9090` для internal, `8080` для `/healthz`+`/readyz` — см. §15).
- Scrape interval Prometheus: 30s.
- Без auth в v0.1 (internal network). В v0.2 — optional `-metricsAuthKey` как в VictoriaMetrics.

---

## 9. Алерты Prometheus — rule'ы в YAML

Файл: `deploy/prometheus/rules/log-analyser.yaml`.

```yaml
groups:
- name: log_analyser.slo.digest_delivery
  interval: 1m
  rules:
  # ==== P0: главный KPI ====
  - alert: DigestOverdue
    expr: (time() - last_successful_delivery_timestamp_seconds) > 26 * 3600
    for: 5m
    labels:
      severity: critical
      slo: digest_delivery
      team: sre
    annotations:
      summary: "Digest не доставлен > 26ч (главный KPI)"
      description: |
        last_successful_delivery_timestamp_seconds = {{ $value | humanizeTimestamp }}.
        Сейчас: {{ with query "time()" }}{{ . | first | value | humanizeTimestamp }}{{ end }}.
        Это означает, что ежедневный TG-отчёт не ушёл. Разработчики не увидят утренний digest.
      runbook_url: "https://github.com/QCoreTech/log_analyser/blob/master/docs/plans/11-sre.md#runbook-R-1"

  - alert: DigestFailureHigh
    expr: |
      sum(increase(digest_cycle_total{status=~"failed"}[7d]))
      / clamp_min(sum(increase(digest_cycle_total[7d])), 1) > 2/7
    for: 1h
    labels:
      severity: critical
      slo: digest_delivery
    annotations:
      summary: "Digest: ≥2 провала за 7 дней (сгорает SLO 28d)"
      runbook_url: "https://github.com/QCoreTech/log_analyser/blob/master/docs/plans/11-sre.md#runbook-R-2"

  - alert: DigestCycleTooLong
    expr: |
      max_over_time(digest_cycle_duration_seconds_bucket{le="600"}[2h])
        / clamp_min(max_over_time(digest_cycle_duration_seconds_count[2h]), 1) < 0.95
    for: 10m
    labels:
      severity: warning
      slo: digest_latency
    annotations:
      summary: "Digest cycle p95 > 10 мин (SLO-2)"
      runbook_url: "https://github.com/QCoreTech/log_analyser/blob/master/docs/plans/11-sre.md#runbook-R-3"

- name: log_analyser.slo.dependencies
  interval: 30s
  rules:
  - alert: VLUnreachable
    expr: |
      (
        sum(rate(vl_query_errors_total{reason=~"timeout|network|5xx"}[5m]))
        / clamp_min(sum(rate(vl_query_total[5m])), 1)
      ) > 0.5
    for: 3m
    labels:
      severity: critical
      dependency: victorialogs
    annotations:
      summary: "VictoriaLogs недоступна или деградирует (>50% fail 5m)"
      runbook_url: "https://github.com/QCoreTech/log_analyser/blob/master/docs/plans/11-sre.md#runbook-R-4"

  - alert: VLQueryErrorBudgetBurnFast
    expr: |
      (
        sum(rate(vl_query_errors_total[1h])) / clamp_min(sum(rate(vl_query_total[1h])), 1) > 0.144
      ) and (
        sum(rate(vl_query_errors_total[5m])) / clamp_min(sum(rate(vl_query_total[5m])), 1) > 0.144
      )
    for: 2m
    labels:
      severity: critical
      slo: vl_query
      burn_rate: fast

  - alert: VLQueryErrorBudgetBurnSlow
    expr: |
      (
        sum(rate(vl_query_errors_total[6h])) / clamp_min(sum(rate(vl_query_total[6h])), 1) > 0.06
      ) and (
        sum(rate(vl_query_errors_total[30m])) / clamp_min(sum(rate(vl_query_total[30m])), 1) > 0.06
      )
    for: 15m
    labels:
      severity: warning
      slo: vl_query
      burn_rate: slow

  - alert: TGRateLimited
    expr: increase(tg_rate_limited_total[10m]) > 5
    for: 2m
    labels:
      severity: warning
      dependency: telegram
    annotations:
      summary: "Telegram 429 > 5 за 10 мин"
      description: "Rate-limit сработал. Проверить: не сломан ли backoff, не добавили ли случайно ещё один бот в тот же токен."
      runbook_url: "https://github.com/QCoreTech/log_analyser/blob/master/docs/plans/11-sre.md#runbook-R-5"

  - alert: TGSendErrorBudgetBurnFast
    expr: |
      (
        sum(rate(tg_send_errors_total{retried!="true"}[1h])) / clamp_min(sum(rate(tg_send_total[1h])), 1) > 0.072
      ) and (
        sum(rate(tg_send_errors_total{retried!="true"}[5m])) / clamp_min(sum(rate(tg_send_total[5m])), 1) > 0.072
      )
    for: 2m
    labels:
      severity: critical
      slo: tg_send
      burn_rate: fast

- name: log_analyser.operational
  interval: 1m
  rules:
  - alert: DLQNotDrained
    expr: dead_letter_queue_size > 0
    for: 30m
    labels:
      severity: warning
    annotations:
      summary: "DLQ не пуст > 30 мин (size={{ $value }})"
      runbook_url: "https://github.com/QCoreTech/log_analyser/blob/master/docs/plans/11-sre.md#runbook-R-6"

  - alert: DaemonRestartingTooOften
    expr: |
      changes(process_start_time_seconds{job="log-analyser"}[1h]) > 3
    for: 5m
    labels:
      severity: critical
    annotations:
      summary: "Daemon рестартовал > 3 раз за час (crash-loop)"
      runbook_url: "https://github.com/QCoreTech/log_analyser/blob/master/docs/plans/11-sre.md#runbook-R-7"

  - alert: StateDBGrowingTooFast
    expr: |
      predict_linear(state_db_size_bytes[6h], 7 * 24 * 3600) > 10 * 1024 * 1024 * 1024
    for: 1h
    labels:
      severity: warning
    annotations:
      summary: "State DB через неделю превысит 10 GB (retention protected?)"

  - alert: HealthzDown
    expr: probe_success{job="log-analyser-healthz"} == 0
    for: 2m
    labels:
      severity: critical
    annotations:
      summary: "/healthz недоступен 2+ минуты"
      runbook_url: "https://github.com/QCoreTech/log_analyser/blob/master/docs/plans/11-sre.md#runbook-R-8"

  - alert: ReadyzDown
    expr: probe_success{job="log-analyser-readyz"} == 0
    for: 10m
    labels:
      severity: warning
    annotations:
      summary: "/readyz недоступен 10+ мин (деградация зависимостей)"

  - alert: CertificateExpiring
    expr: probe_ssl_earliest_cert_expiry{job=~"log-analyser-.*"} - time() < 14 * 86400
    for: 1h
    labels:
      severity: warning
    annotations:
      summary: "TLS сертификат (TG proxy / Grafana / VL) истекает через < 14 дней"
      runbook_url: "https://github.com/QCoreTech/log_analyser/blob/master/docs/plans/11-sre.md#runbook-R-9"

  - alert: DeadManSwitchMissing
    expr: |
      (time() - push_heartbeat_timestamp_seconds{job="log-analyser-deadman"}) > 26 * 3600
    for: 5m
    labels:
      severity: critical
    annotations:
      summary: "Dead-man switch: heartbeat не приходил > 26ч"
      description: "Даже если Prometheus не видит сам daemon (Prometheus лёг, scrape сломался) — dead-man должен сработать."
      runbook_url: "https://github.com/QCoreTech/log_analyser/blob/master/docs/plans/11-sre.md#runbook-R-10"
```

**Обязательный минимум для prod v0.1 (3 алерта):**
1. `DigestOverdue` — без него сервис бесполезен.
2. `VLUnreachable` — причина #1 пропуска digest.
3. `DeadManSwitchMissing` — последняя линия обороны, когда всё остальное тоже сломано.

Остальные — желательны, но запуск допустим и без них (настраиваются в первые 2 недели после деплоя).

---

## 10. Runbook

Общая структура ответа на алерт: **ack (в течение 15 мин)** → **triage (30 мин)** → **mitigate (≤ 2 ч)** → **postmortem (в течение 48 ч, если SEV≤2)**.

Эскалация по умолчанию: on-call SRE → TeamLead → Lead DevOps.

### R-1. DigestOverdue

**Триггер:** `time() - last_successful_delivery_timestamp_seconds > 26h`.

**Диагностика (по порядку):**
1. `kubectl logs deploy/log-analyser -n observability --tail=500 --timestamps` — найти ERROR/WARN за последние 24ч.
2. Проверить `kubectl describe pod` — не рестартует ли, есть ли OOMKill.
3. `curl -sS http://log-analyser:8080/readyz` — все ли зависимости здоровы.
4. `curl -sS http://log-analyser:9090/metrics | grep -E 'digest_cycle_total|dead_letter'` — где остановился последний цикл.
5. `kubectl exec deploy/log-analyser -- ls -la /var/lib/log_analyser/reports/` — есть ли отчёт за сегодня на диске?
   - Если есть, но не отправлен → проблема delivery, см. R-5.
   - Если нет → проблема collect/render, см. R-4.

**Шаги митигации:**

| Ситуация | Действие |
|---|---|
| Pod не запущен | `kubectl rollout restart deploy/log-analyser` |
| Pod в CrashLoopBackOff | см. R-7 |
| Зависимость недоступна | проверить VL (см. R-4) и TG (см. R-5) |
| Daemon живой, но scheduler мёртв | рестарт pod'а, тикет на root-cause analysis |
| Digest в DLQ | `kubectl exec deploy/log-analyser -- /usr/local/bin/log-analyser dlq drain` (если есть CLI) или рестарт (автодрейн на старте) |
| Ручная доставка за пропущенные сутки | `kubectl exec deploy/log-analyser -- /usr/local/bin/log-analyser once --date=YYYY-MM-DD` |

**Эскалация:** если не восстановлено за 2 часа → TeamLead + разработчик-автор, pinned-notice в приватный канал команды.

### R-2. DigestFailureRateHighSlow

**Триггер:** 2+ провала за 7 дней.

**Диагностика:**
1. Сравнить список failed run'ов: `grep '"msg":"digest cycle failed"' logs | jq .error`.
2. Группировать по `phase_at_failure`: что чаще ломается — collect, render, deliver.
3. Проверить, нет ли паттерна по времени суток / дню недели.

**Шаги митигации:**
1. Завести **инцидент** в `docs/incidents/` (обязательно при 2+ провалах — съедает SLO целиком).
2. Если причина — новая фича (checking commit history за 7 дней) → rollback до предыдущего тега.
3. Если причина — изменение во внешних системах (VL обновили, Grafana UID сменился) → action item «подписаться на release notes».
4. Feature freeze до восстановления бюджета.

### R-3. DigestCycleTooLong

**Триггер:** p95 > 10 мин за последние 2 часа.

**Диагностика:**
1. `sum by (phase) (rate(digest_phase_duration_seconds_sum[1h])) / sum by (phase) (rate(digest_phase_duration_seconds_count[1h]))` — узнать, какая фаза тормозит.
2. Проверить `vl_query_duration_seconds` по host — не тормозит ли один хост.
3. `vl_query_rows_returned` — не стало ли строк на порядок больше (NFR-P1 перегружен).

**Шаги митигации:**
1. **Tormoz в collect, конкретный хост:** попросить SRE торгсервера проверить, не сломали ли log-level, не тёчет ли loop.
2. **Tormoz в dedup:** скорее всего раздулся `fingerprint_cache_size` → уменьшить retention fingerprints (NFR-Ret2, сейчас 90 дней, можно временно 30).
3. **Tormoz в render:** проверить размер файла, ли не уткнулись в truncation.
4. **Tormoz в deliver:** TG 429, см. R-5.
5. Если в целом «выросла нагрузка» → chunked fetch по часам (NFR-P1, R-11 из рисков).

### R-4. VLUnreachable / VL degraded

**Триггер:** `vl_query_errors_total` рост, `VLUnreachable` фаерит.

**Диагностика:**
1. `curl -sS -m 5 $VL_URL/select/logsql/query -d 'query=*' -d 'limit=1' -d 'timeout=3s'` — ответит ли VL вообще.
2. Проверить VL со стороны инфры: на его Prometheus-endpoint смотрим `vl_http_requests_total{path="/select/logsql/query"}` (метрика подтверждена Context7 — присутствует в VictoriaLogs docs).
3. Сеть: trace route, nc -zv к VL порту.

**Шаги митигации:**
1. **VL лежит:** эскалация SRE VictoriaLogs-кластера. Параллельно — увеличить `VL_QUERY_TIMEOUT` env от дефолта (30s) если видно что медленно отвечает. Осторожно — не превысить `-search.maxQueryDuration` (подтверждено Context7, default 30s).
2. **Сетевой проблем:** подключить bastion/прокси если OQ-2 не закрыт.
3. **Auth протух:** проверить креды VL, перевыпустить.
4. **Перегружен queue** (`-search.maxQueueDuration` exceeded) → уменьшить concurrency в daemon (`VL_MAX_CONCURRENT`), сейчас 5 (по хосту), можно 2.

### R-5. TGRateLimited

**Триггер:** > 5 событий 429 за 10 мин.

**Диагностика:**
1. Проверить, что `retry_after` уважается в коде (grep логов `"retry_after"`).
2. Проверить, не запущен ли случайно второй instance daemon'а с тем же токеном (**это как раз R-7 из analysis, а R-2 из рисков — критично**): `kubectl get pods -l app=log-analyser` — должен быть 1.
3. Проверить, не запущены ли ручные CLI-прогоны параллельно.

**Шаги митигации:**
1. Убить параллельный instance.
2. Если rate-limit из-за реально высокой нагрузки (v0.2 realtime) — **token bucket обязательно**: 25 msg/s (с запасом от 30/s лимита Telegram Bot API, подтверждено Context7: «30 messages per second» global, «1 message / second / group» per group).
3. Если TG в депрессии — ждать: TG отдаёт `retry_after` в секундах, блокировать sender на эту сумму +10%.

### R-6. DLQNotDrained

**Триггер:** `dead_letter_queue_size > 0` дольше 30 мин.

**Диагностика:**
1. Посмотреть содержимое DLQ: `kubectl exec ... -- /usr/local/bin/log-analyser dlq list` (если CLI есть) или `sqlite3 state.db "SELECT * FROM dead_letter LIMIT 10"`.
2. Проверить, почему auto-drain не работает: нет ли в логах `dlq drain skipped` с причиной.

**Шаги митигации:**
1. Если TG дошла до нормы — auto-drain запустится на следующем scheduler tick.
2. Принудительный drain: `kubectl exec ... -- /usr/local/bin/log-analyser dlq drain --force`.
3. Если payload «несовместимый» (например, chat_id поменялся) — либо rewrite payload вручную, либо drop с sign-off TeamLead'a.

### R-7. DaemonRestartingTooOften

**Триггер:** рестарт > 3 раз/час.

**Диагностика:**
1. `kubectl logs --previous deploy/log-analyser` — причина предыдущего рестарта.
2. `kubectl describe pod` — OOMKill? livenessProbe fail? сигнал извне?
3. `go_memstats_alloc_bytes`, `process_resident_memory_bytes` перед рестартом.

**Шаги митигации:**
1. **OOMKill:** поднять memory limit до 1 GiB (если 512 недостаточно — значит чанкуемся по часам в collect).
2. **Panic:** по stacktrace — патч, hotfix ветка, expedited release.
3. **liveness false positive:** увеличить `periodSeconds` или `failureThreshold`.
4. Временная мера: `kubectl scale deploy/log-analyser --replicas=0` чтобы остановить crash-loop, если чинится > 1ч.

### R-8. HealthzDown

**Триггер:** blackbox-exporter не может достучаться до /healthz 2 мин.

**Диагностика:**
1. Прямой `curl` из сети мониторинга.
2. Проверить, слушает ли процесс на порту внутри контейнера: `kubectl exec ... -- ss -tln`.
3. `kubectl logs` — может, deadlock (healthz сам не отвечает).

**Шаги митигации:** рестарт pod'а (liveness в итоге сам это сделает после 3 fail по 10с = 30с).

### R-9. CertificateExpiring

**Триггер:** сертификат Grafana / TG-прокси / VL истекает < 14 дней.

**Диагностика:** `openssl s_client -connect host:443 -servername host | openssl x509 -noout -dates`.

**Шаги митигации:** эскалация владельцу сертификата (в нашем контексте мы только клиент), параллельно готовим pin новых CA, если приватный CA.

### R-10. DeadManSwitchMissing

**Триггер:** heartbeat от daemon'а не приходит во внешний watcher > 26 часов.

**Диагностика:** либо daemon умер, либо Prometheus лежит, либо сеть между ними сломана. В любом случае — проверять вручную.

**Шаги митигации:**
1. Прямо (не через мониторинг!) зайти на хост / pod: `kubectl get pod -l app=log-analyser`.
2. Если нет пода — рестарт deployment'а.
3. Если под есть но не шлёт heartbeat — см. R-1.
4. Если daemon жив, а Prometheus лежит — это отдельный infra-инцидент.

---

## 11. Capacity planning

### 11.1. Ресурсы

**MVP baseline (подтверждено в analysis §13.3):**

| Ресурс | Requests | Limits | Обоснование |
|---|---|---|---|
| CPU | 100m | 500m | Digest cycle даёт пик ~300m на 2-3 мин × 1 раз в сутки, остальное время — почти idle |
| Memory | 128Mi | 512Mi | Стриминг JSON-lines из VL, никогда не держим всё в памяти. 512Mi — запас на piked file rendering |
| Ephemeral storage | 2Gi | 5Gi | Временные рендеры |
| Volume `/var/lib/log_analyser/state.db` | — | 10Gi PV | SQLite + WAL, 90-day fingerprints |
| Volume `/var/lib/log_analyser/reports/` | — | 30Gi PV | 30 дней × 5 файлов × ≤ 50Mi (edge) = 7.5 GiB worst case, +запас |

### 11.2. Triggers масштабирования

| Симптом | Действие |
|---|---|
| `digest_cycle_duration_seconds` p95 > 8 мин (уже близко к SLO-2) | Chunked fetch по часам в `collect` (24 чанка вместо 1 на хост) |
| `vl_query_rows_returned` p95 > 1e6 | Chunked fetch по `_time` (pipe `| limit 100000` + cursor) |
| `records_scanned_total` > 25M/сутки (NFR-P1 2.5× превышен) | Горизонтальный sharding: 1 instance на N хостов, раздельные state.db, общий TG-chat |
| OOM | Поднять limits до 1Gi, параллельно — chunked fetch |
| DLQ растёт постоянно | Отдельный DLQ-drainer goroutine с retry-ограничением, metric с age |

### 11.3. Scale-out дизайн (v0.2+)

Если 1 instance перестанет справляться (маловероятно для MVP):
- Leader election через SQLite advisory lock → etcd/Consul → k8s Lease.
- Разделение hosts: instance-A = `{t1, ali-t1}`, instance-B = `{t2, aws-t3, t5}`.
- **Одна** сущность формирует cover и отправляет его (leader), followers посылают свои файлы.
- Проблема: `sendMediaGroup` — атомарный, не склеится между инстансами. Решение: leader собирает все файлы с followers через shared volume / S3 и шлёт одним media group.

> В MVP не нужно. Оценка ресурсов говорит, что 1 instance потянет 25M/сутки.

### 11.4. Disk growth

- **state.db:** ~100 байт на fingerprint × 100k fingerprints/90 дней ≈ 10 MB. Очень мало.
- **reports:** 5 файлов × среднее 2 MB × 30 дней = 300 MB. Worst case (шумный день) — 5 × 50 MB × 30 = 7.5 GiB.

**Политика retention** (подтверждено analysis NFR-Ret1/2):
- reports: 30 дней, cleanup goroutine раз в сутки.
- state.db: 90 дней для fingerprints, `VACUUM` раз в неделю.

---

## 12. Disaster recovery / backup

### 12.1. Что бэкапим

| Ресурс | RPO | RTO | Как |
|---|---|---|---|
| `state.db` (SQLite) | 24 ч | 30 мин | `sqlite3 state.db ".backup /backups/state-$(date +%F).db"` cronjob каждые 6 ч, retention 14 дней. Хранится в S3 / NFS (не на том же PV). |
| `reports/` | 24 ч | 1 ч | Нестрогий бэкап. Можно пересоздать CLI `once --date=...`. Бэкап только «для аудита» — 30 дней в S3. |
| Grafana dashboards (наши, не всей компании) | 1 неделя | 1 ч | Provisioned as code в git `deploy/grafana/`. Это и есть бэкап. |
| Prometheus rules | git | 5 мин | git `deploy/prometheus/rules/` |

### 12.2. Что НЕ бэкапим

- Сами логи торговых серверов — это VL, не наш scope.
- TG-сообщения — уже в TG (TG же наш вывод).
- Метрики — Prometheus политика (external).

### 12.3. Сценарии восстановления

**Сценарий DR-1: удалили PV со state.db.**

Последствия: теряется дедуп-база fingerprint'ов за 90 дней. Все fingerprint'ы будут «новыми» первые 7 дней.

RTO: 30 мин (создать новый PV, восстановить из S3-бэкапа).

Рецепт:
```bash
# 1. Создать новый PV (terraform / k8s)
# 2. Восстановить state.db
aws s3 cp s3://backups/log-analyser/state-2026-04-22.db /mnt/pv/state.db
chmod 600 /mnt/pv/state.db
# 3. Рестарт pod
kubectl rollout restart deploy/log-analyser
```

**Сценарий DR-2: весь кластер k8s потерян.**

Последствия: digest пропустит 1-2 дня.

RTO: 4 часа (инфра), затем 5 мин чтобы поднять сервис.

Рецепт: применить манифесты из git, восстановить state из S3 (optional — без него работать будет, но первые 7 дней будет больше «новых» паттернов).

**Сценарий DR-3: потеряли Docker image (GHCR уронили, тэг удалён).**

Рецепт: `goreleaser release --snapshot --rm-dist` локально, или пересборка из git-тэга в CI.

### 12.4. DR-тестирование

- Раз в квартал: restore-drill из S3 в staging.
- Раз в квартал: `chaos: kill -9` продакшн pod'а, проверить auto-recovery + идемпотентность (FR-12).

---

## 13. Dead-man switch

**Проблема:** если daemon тихо завис (не крашится, не рестартует, просто `scheduler` goroutine мертва), а Prometheus scrape по какой-то причине отдаёт stale-but-valid metrics — `DigestOverdue` сработает только через 26 часов **после последней успешной доставки**. Значит потерим минимум 1 digest.

**Решение: внешний dead-man.**

Daemon после каждого успешного digest-цикла делает **push на внешний endpoint**: Pushgateway / healthchecks.io / внутренний мини-сервис.

**Архитектура:**

```
log-analyser (MSK 08:00)
   │ sendMediaGroup OK
   ▼
http POST https://hc.example.com/ping/log-analyser-daily
   │
   ▼
healthchecks.io (внешний, в другом дата-центре / SaaS)
   │ следит: «ping не приходил > 26 ч»
   ▼ (если тишина)
https://alertmanager.internal/api/v2/alerts
   │ (или прямо в TG/PagerDuty)
   ▼
on-call SRE получает алерт
```

**Рекомендация:** **healthchecks.io** (SaaS, 20 проверок free, Telegram/Slack/PagerDuty интеграции). Простой HTTP-эндпоинт на `ping` — идеально для dead-man.

Альтернатива если нельзя SaaS: Pushgateway **в другом Prometheus-инстансе** (не тот, что мониторит сам daemon) + алерт на этом Prometheus. Иначе dead-man и daemon упадут вместе.

**Реализация в daemon:**

```go
// После успешного sendMediaGroup
if cfg.DeadManURL != "" {
    go func() {
        ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer cancel()
        _, _ = httpClient.Post(cfg.DeadManURL+"?run_id="+runID, ...)
    }()
}
```

**Config:** `DEADMAN_URL` (optional env). Fail-open: если push не удался, это не фейлит digest.

**Дополнительно:** heartbeat в отдельный TG-topic «infra-status» раз в сутки с сообщением «daemon alive, last digest at ...». Это user-visible dead-man — команда сразу увидит если заткнёт.

---

## 14. Chaos engineering (v0.2+)

Не внедряем в MVP, но дизайн учитываем.

### 14.1. Fault injection scenarios

| # | Сценарий | Ожидаемое поведение | Что тестируем |
|---|---|---|---|
| CH-1 | VL отвечает 503 на все запросы в течение 10 мин | Retry exhausted → skip cycle → `[delivery-failed]` в TG | NFR-R1 |
| CH-2 | VL отвечает очень медленно (latency 25s при timeout 30s) | Уместится в digest, но p95 поднимется → DigestCycleTooLong | SLO-2 |
| CH-3 | TG возвращает 429 с `retry_after=60` | Backoff, ретрай после 66 сек (60 + jitter 10%) | FR-7, TGRateLimited NOT firing false-positive |
| CH-4 | TG возвращает 500 на `sendMediaGroup` | Fallback на 5 × sendDocument (FR-7) | FR-7 |
| CH-5 | Disk full в `reports/` | Render фаза падает с понятной ошибкой, render isolated per host (NFR-R4) | NFR-R4 |
| CH-6 | Clock skew на +5 минут / −5 минут | Окно рассчитывается корректно (использовать монотонные часы для measure, wall для window) | NFR-TZ1 |
| CH-7 | Kill -9 в середине `sendMediaGroup` | При рестарте: проверка по pre-commit marker → дубля нет (FR-12) | FR-12, R-7 |
| CH-8 | Network partition между daemon и VL | 30s timeout → skip hosts, partial report, partial marker в cover | NFR-R4 |
| CH-9 | State DB corrupted | daemon стартует → detect → алерт → восстановление из бэкапа | DR-1 |
| CH-10 | Два daemon'а с одним токеном (случайно запустили two replicas) | File-lock на state.db → второй exit-2 с понятным сообщением | NFR-I2 |

### 14.2. Tooling

- **toxiproxy** перед VL / TG mock сервером.
- **litmuschaos** / **chaos-mesh** в k8s для podkill, networkloss.
- **Самописный** chaos CLI для dev: `log-analyser chaos inject vl --delay=20s --duration=5m`.

Запуск — в staging, раз в месяц. В prod — никогда (это не RPS-сервис, чаотить тут особо не нужно).

---

## 15. Security SRE

### 15.1. `/metrics` endpoint

**Принцип:** `/metrics` содержит **только operational**, не бизнес-данные. Но `bot_username` в `tg_bot_info` — это уже identifier. Поэтому:

- **v0.1:** `/metrics` слушает на **internal-only** порту `:9090`, bind `0.0.0.0` но через k8s NetworkPolicy ограничен на namespace Prometheus.
- **v0.2:** optional auth key (как в VictoriaMetrics: `-metricsAuthKey=file:///run/secrets/metrics_key`, подтверждено Context7 в выдаче VL-документации).
- Отдельный `/healthz` и `/readyz` — на порту `:8080`, этот порт открыт шире (нужен для k8s probes).

### 15.2. Rate-limit на HTTP endpoints

- `/metrics`: limit 10 RPS per source IP (token bucket). Защита от случайного Prometheus-scrape storm.
- `/healthz`: без rate-limit (lightweight).
- `/readyz`: limit 2 RPS per source IP (внешние вызовы дорогие, кэш на 30-60 с).

### 15.3. Поведение при попытке доступа без auth

- В v0.1 auth нет → ничего особенного.
- В v0.2 при invalid authKey: `401 Unauthorized` + log WARN (без утечки ожидаемого ключа). Не `403` — не даём понять, что endpoint существует, но закрыт.

### 15.4. Utility endpoints — что явно запрещено

- **НЕТ** `/debug/pprof` в prod-сборке (compile flag). В staging — включить за auth-key.
- **НЕТ** эндпоинтов, возвращающих последние N логов / reports — это бизнес-данные, доступ только через k8s exec + RBAC.
- **НЕТ** admin-API типа `/admin/drain-dlq` без auth. Управление — только CLI через `kubectl exec`.

### 15.5. Secrets handling (дополнение к NFR-S1..S4)

- `TG_BOT_TOKEN`, `VL_BASIC_AUTH` — только из env, заполняются из k8s Secret.
- При старте daemon проверяет: `len(TG_BOT_TOKEN) > 40` — fail-fast если пусто или токен усечён.
- В логах — всегда redact через HTTP middleware. Unit-test `TestNoTokenInLogs` (R-8 из analysis).
- При `getMe` запросе (в healthz) — не логируем full URL, только `getMe ok, bot_username=...`.

### 15.6. Audit log

Важные операции (manual `once`, DLQ drain, config reload) — в отдельный audit-log с `actor` (из `kubectl auth can-i` → k8s SA → human через impersonation).

---

## 16. Расхождения с аналитиком

Аналитик в §13.2 дал **черновик** SLO — он адекватный стартовый набор, но для prod-готовности требует уточнений. Вот где SRE считает иначе:

| # | Позиция аналитика | Позиция SRE | Почему |
|---|---|---|---|
| **D-1** | SLO «Успешность доставки ≥ 99.0%, error budget 1 run/мес» | **≥ 96.4% на 28d** (1 пропуск). Стройная матч. формулировка: `≥ ⌊28−1⌋ / 28`. Плюс явно добавлен `partial` как success. | 99.0% × 30 дней = 0.3 run бюджета — неоперируемо. 28d rolling стабильнее. `partial` (4/5 файлов доставлены) — приемлемо. |
| **D-2** | SLO «`digest_cycle_duration_seconds` p95 ≤ 10 мин» — без error budget | Добавлен явный error budget (1.4 цикла/28d) и **histogram buckets** `[30,60,120,180,300,600,900,1200,1800,3600]` | Без конкретных buckets p95 не посчитать корректно. Google SRE рекомендует bucket ровно на целевом значении. |
| **D-3** | Нет burn-rate alert'ов | Добавлены **2× (1h/5m)** и **6× (6h/30m)** для SLO-4, SLO-5. Для SLO-1 **не** используются burn-rate — заменены на absolute thresholds | Multi-burn-rate спроектирован для RPS-сервисов. На 1 событии/сутки формула даёт шумные / бессмысленные алерты. Лучше `DigestOverdue` + `DigestFailureHigh` с явными порогами. |
| **D-4** | Отсутствует dead-man switch | **Обязательный dead-man** (healthchecks.io или в отдельном Prom), иначе scheduler-driven сервис не увидит собственного зависания | Это не academic — scheduler-driven сервисы особенно уязвимы к silent failure. |
| **D-5** | Алерт `last_successful_delivery_timestamp_seconds > 26h` упомянут, но без deadman + runbook | Runbook **R-1** сделан главным, плюс dead-man дублирующий | Если Prometheus лежит — алерт не сработает. Нужен внешний наблюдатель. |
| **D-6** | Метрика `dedup_cache_size` — упомянута | Переименована в `fingerprint_cache_size` + добавлены `fingerprint_cache_evictions_total` | Терминологическая согласованность с §2 analysis (там «fingerprint»). |
| **D-7** | Список метрик без histogram buckets | **Все buckets явно прописаны** в §5.2 с обоснованием в §5.3 | Без buckets histogram бесполезен. |
| **D-8** | `/readyz` — «VL reachable + TG bot getMe OK» | **Расширен до 5 проверок** (VL, TG, state.db writable, reports dir, disk space) с явным кэшированием 30-60с | Без кэша blackbox-probe каждые 30с выдаст 30+ запросов/мин на VL, что само по себе нагрузка. |
| **D-9** | Capacity: 100m/128Mi req, 500m/512Mi limit | Согласен, но добавлены **triggers масштабирования** и **plan chunked fetch** | Без плана как чинить NFR-P1 при 25M rows / сутки — требование «пустое». |
| **D-10** | DR / backup отсутствуют | Добавлен RPO/RTO по state.db, reports, manifests | Без этого сервис непоставим в prod. |
| **D-11** | Chaos engineering отсутствует | Добавлено 10 сценариев в §14 для v0.2 | Не блокер MVP, но дизайн должен это учитывать с начала. |
| **D-12** | `/metrics` без auth (внутри internal network) | Согласен для v0.1; для v0.2 рекомендую `metricsAuthKey` как в VictoriaMetrics | Нужно зафиксировать траекторию. |
| **D-13** | Нет явного разделения SLO «full delivery (5/5)» vs «partial (≥4/5)» | Введён `partial` как допустимый success для SLO-1 | По NFR-R4 «сбой одного хоста → отчёт по остальным 4 уходит + [partial-report] маркер». Значит partial — это прямо проектное решение, а не провал. Счёт успехов должен это учитывать, иначе SLO противоречит NFR-R4. |
| **D-14** | `tg_send_errors_total{method}` без дальнейшей структуризации | Добавлен label `retried={true,false}` и `reason={429,4xx,5xx,timeout,network}` | Без `retried` нельзя отличить «временную ошибку, решённую retry» от «штатного fail». SLO-5 это требует. |

**Где SRE согласен с аналитиком (без изменений):**
- Набор метрик из §13.2 берётся as-is (с уточнениями labels и buckets).
- `/healthz` и `/readyz` как контракт — согласен.
- Runbook-points как draft — развёрнуты в полноценный §10.
- NFR-R1..R4, NFR-P2, NFR-O1..O3 — все поддержаны без претензий.
- SQLite + WAL как state (§13.1 ADR-01) — согласен, бэкап простой.
- Single-instance MVP, scale-out только в v0.2 — согласен.

---

## 17. Чек-лист «ready for prod» (DoR SRE)

До разрешения на запуск v0.1 в prod:

- [ ] Prometheus rules из §9 задеплоены, алерты `DigestOverdue`, `VLUnreachable`, `DeadManSwitchMissing` активны и протестированы (вручную триггер).
- [ ] Dead-man switch (healthchecks.io или equivalent) — зарегистрирован, pinged хотя бы 1 раз.
- [ ] `/healthz` и `/readyz` — проверены blackbox-exporter'ом.
- [ ] Grafana dashboard с панелями: `digest_cycle_duration_seconds` p50/p95/p99, `vl_query_*`, `tg_send_*`, `dead_letter_queue_size`, `last_successful_delivery_timestamp_seconds`.
- [ ] Runbook линки в аннотациях алертов кликабельны (GitHub public / internal wiki).
- [ ] Бэкап state.db — первый успешный запуск cronjob в S3.
- [ ] On-call rotation назначен в PagerDuty / Opsgenie.
- [ ] Postmortem template в `docs/incidents/TEMPLATE.md`.
- [ ] В TG — приватный канал `#log-analyser-alerts` для infra-алертов (отдельно от бизнес-digest).
- [ ] Drill: «убить pod во время digest cycle» — выполнен в staging, дубля в TG нет.
- [ ] Первые 7 дней prod — daily SRE review метрик (без автопилота).

---

**Автор SRE-плана:** Claude (senior SRE mode).
**Следующий шаг:** передать DevOps'у (`docs/plans/12-devops.md`) для CI/CD и Dockerfile под эти требования, архитектору (`docs/plans/10-architecture.md`) для интеграции healthcheck-контрактов и метрик в код.
