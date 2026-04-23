# ADR-0009: Dedup fingerprint — sha1 + нормализация по YAML regex-профилю

- **Статус:** Accepted
- **Дата:** 2026-04-23
- **Автор:** architect
- **Связанные ADR:** [ADR-0002](0002-state-store-sqlite-wal.md), [ADR-0011](0011-config-envconfig-with-yaml-overlay.md).

## Контекст

FR-10: группировать одинаковые ошибки в один «инцидент» для читаемости отчёта. «Одинаковые» = после нормализации (убрать IDs, timestamps, hex, UUID, IP). Без нормализации 1000 строк `error trade #123...#999` станут 1000 инцидентами — отчёт нечитаем.

Требования:
- нормализация **конфигурируемая** (новые IDs-форматы появляются);
- профиль диффабелен в PR (ревью регексов — must);
- fingerprint стабилен между рестартами daemon'а;
- коллизии crypto-strength не нужны, важна скорость + распределение.

## Решение

**`sha1(host + \x00 + app + \x00 + normalize(msg))` → hex** как fingerprint.

### Нормализация

Профиль — в `configs/normalize.yaml`:
```yaml
version: 1                    # совместим с fp-history в state
patterns:
  - name: iso_timestamp
    regex: '\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})'
    replace: '<ts>'
  - name: uuid
    regex: '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}'
    replace: '<uuid>'
  - name: ipv4
    regex: '\b(?:\d{1,3}\.){3}\d{1,3}(?::\d+)?\b'
    replace: '<ip>'
  - name: hex_token
    regex: '\b[0-9a-f]{8,}\b'
    replace: '<hex>'
  - name: trade_id
    regex: '(?i)(trade|order|deal)[ _#-]?\d+'
    replace: '$1_<id>'
  - name: numeric
    regex: '\b\d{2,}\b'
    replace: '<n>'
  - name: trailing_whitespace
    regex: '\s+$'
    replace: ''
```

Применение — **по порядку** (UUID до hex, timestamp до numeric); порядок — часть контракта профиля, проверяется тестом.

### Версионирование профиля

Поле `version`. При смене профиля (например, `version: 2`) на старте daemon:
- сверяет `schema_version.dedup_profile_version` vs `configs/normalize.yaml#version`;
- если разные — truncate `fingerprints`, пишет warning в лог, `IsNew7d` первые 7 дней будет возвращать true чаще.

### Почему SHA-1

- 40 hex, 160 бит — с запасом для наших объёмов (< 10⁶ fingerprint'ов за всю жизнь).
- Быстрее SHA-256, быстрее MD5 в go (`crypto/sha1`).
- Коллизии для non-adversarial input пренебрежимы.

Альтернатива — `xxhash`/`fnv`: быстрее, но добавляет зависимость. Stdlib `crypto/sha1` — ноль deps.

## Последствия

**Плюсы**
- Профиль diffabel в PR, ревьюить regex'ы можно глазами.
- Версионирование профиля предотвращает незаметное искажение истории fingerprint'ов.
- SHA-1 stdlib — zero deps.
- Регексы компилируются один раз на старте, кэшируются — perf ок (O(n*p) на событие, где p = кол-во паттернов ~10).

**Минусы / trade-offs**
- Регексы медленные для гигантских выборок. Для 5 хостов × 2.5 млн error/сутки на хост → 5 × 500k event/cycle × 10 regex = 25М regex-run. Это ~1-3 сек в Go — приемлемо (NFR-P2 = 10 мин).
- False-dedup/false-split (R-4): митигация ретроспективой, в первую неделю v0.1 — ручная сверка.
- Смена профиля = потеря истории. Принято осознанно: версия в схеме + warning.

## Альтернативы

1. **MinHash / LSH.** Отвергнуто: нужен для клустеризации почти-похожих, а не exact-match после нормализации. Overkill + новая зависимость.
2. **Jaccard на токенах.** Отвергнуто: непредсказуемые склейки, сложно объяснять пользователю «почему эти две строки — один инцидент».
3. **MD5.** Отвергнуто: security-смысла нет, SHA-1 быстрее в Go (ассемблерная реализация).
4. **SHA-256.** Отвергнуто: избыточно, SHA-1 хватает.
5. **Хардкод regex в Go-коде.** Отвергнуто: не diffabel, изменение требует передеплоя. YAML — reload on restart, в v0.3 — hot-reload.
6. **ML / clustering (DBSCAN).** Out-of-scope (см. 00-analysis.md §7.2).

## Ссылки

- `pkg.go.dev/regexp` (Go regex — RE2, без backreferences).
- `docs/plans/10-architecture.md §3 (Шаг 2, 3)`, §5.3, §8.2.
