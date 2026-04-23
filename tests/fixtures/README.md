# tests/fixtures/

Фикстуры для unit/integration-тестов. Формат совместим с VictoriaLogs HTTP API (`/select/logsql/query` stream JSON-lines).

См. спецификацию в [`docs/plans/13-qa.md §7`](../../docs/plans/13-qa.md#7-тестовые-фикстуры).

## Формат файла

Каждый `.jsonl` — одна строка = один лог-эвент:

```jsonl
{"_time":"2026-04-22T08:14:03.123Z","_stream":"{host=\"t5\",app=\"order-svc\"}","_msg":"connection refused to 10.0.1.45:8443","host":"t5","app":"order-svc","level":"error"}
{"_time":"2026-04-22T08:14:04.501Z","_stream":"{host=\"t5\",app=\"order-svc\"}","_msg":"connection refused to 10.0.1.45:8443","host":"t5","app":"order-svc","level":"error"}
```

**Обязательные поля:**
- `_time` — ISO-8601 UTC с миллисекундами (`Z`).
- `_stream` — строка селектора стрима в формате VL.
- `_msg` — текст события.
- `host`, `app`, `level` — поля, по которым идёт фильтрация / группировка (см. OQ-5 в analysis.md: являются ли они полями стрима — подтверждается preflight-чеком daemon'а).

**Допустимые `level`:** `debug`, `info`, `warn`, `error`, `critical`. Daemon фильтрует только `error` + `critical`.

## Каталог фикстур для v0.1

### `logs/t5_normal.jsonl` — канонический happy-path
- **Размер:** 1000 строк.
- **Распределение:** 90% `error`, 10% `critical`; 5 уникальных app; 20 уникальных fingerprints.
- **Паттерны для dedup:** `connection refused to <ip>:<port>`, `trade #<id> failed for user <uuid>`, `timeout after <num>ms`.
- **Используется в:** T-1, T-5, T-8, snapshot-тестах рендера.

### `logs/t2_noisy.jsonl` — шумный сервер
- **Размер:** 100 000 строк.
- **Распределение:** 10 уникальных паттернов × 10000 повторов (с разными trade-ID / timestamp).
- **Используется в:** T-8 (dedup), T-24 (streaming memory bound), perf-бенчи.

### `logs/ali-t1_empty.jsonl` — пустой хост
- **Размер:** 0 строк (литерально пустой файл).
- **Используется в:** T-2 (файл всё равно создан с «Ошибок не обнаружено»).

### `logs/aws-t3_mixed_levels.jsonl` — фильтрация по level
- **Размер:** 500 строк.
- **Распределение:** 80% `warn`, 18% `error`, 2% `critical`.
- **Используется в:** `TestCollector_FiltersNonErrorLevels` — проверка, что warn/info отфильтрованы на LogsQL-уровне.

### `logs/t1_with_critical.jsonl` — для realtime / паттернов
- **Размер:** 200 строк.
- **Распределение:** 5 уникальных `critical`-fingerprints + 100 `error`.
- **Используется в:** T-30, FR-13 (v0.2 realtime alerts).

### `logs/t5_pii.jsonl` — нормализация персональных данных
- **Размер:** 50 строк.
- **Содержит:** UUID v4, IPv4/IPv6, email, trade-ID, order-ID, hex request-ID.
- **Используется в:** T-13..T-17 (каждый regex нормализации).

### `logs/t2_malformed.jsonl` — устойчивость парсера
- **Размер:** 50 строк, из них 5 с битым JSON (usually оборванный объект, BOM, NUL-байт).
- **Используется в:** `TestCollector_ParseJSONLines_SkipsMalformed`.

### `logs/t5_large_msg.jsonl` — усечение больших сообщений
- **Размер:** 100 строк, `_msg` у каждой > 10 KB (stack-trace).
- **Используется в:** `TestRender_TruncatesLargeMsg`.

## Генерация фикстур

Большие фикстуры (`t2_noisy.jsonl`, `t5_pii.jsonl`) — генерятся скриптом `dev/gen_fixtures.go`:

```bash
go run ./dev/gen_fixtures -out tests/fixtures/logs -case t2_noisy
```

Это даёт:
- детерминированный вывод (фиксированный seed),
- простое обновление при изменении схемы полей,
- diff-friendly (генератор стабилен между коммитами).

**Не делаем фикстуры вручную > 1000 строк** — слишком хрупко.

## Checksum'ы

В `fixtures/CHECKSUMS.txt` хранится SHA256 каждого файла:
```
7f3a1b...  t5_normal.jsonl
...
```

CI-job `verify-fixtures` проверяет, что файлы не дрейфуют без обновления CHECKSUMS.

## Приоритет реализации

1. **`t5_normal.jsonl`** — блокирует большую часть unit/integration тестов.
2. **`t2_noisy.jsonl`** — блокирует dedup/perf.
3. **`ali-t1_empty.jsonl`** — блокирует T-2 (edge-case «пустой хост»).

Остальные — по мере закрытия P0-сценариев.
