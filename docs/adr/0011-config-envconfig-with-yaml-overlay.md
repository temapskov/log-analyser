# ADR-0011: Конфигурация — `envconfig` + опциональный YAML overlay

- **Статус:** Accepted
- **Дата:** 2026-04-23
- **Автор:** architect
- **Связанные ADR:** [ADR-0009](0009-dedup-fingerprint-profile.md) (regex-профиль — YAML), [ADR-0010](0010-internal-module-structure.md).

## Контекст

FR-9: обязательные и опциональные параметры через env + опциональный file. Необходимо:
- fail-fast на старте, если missing required (TG_BOT_TOKEN и др.);
- defaults для опциональных;
- плоский тип для большинства — env;
- структурные данные (regex-профиль, host-overrides в будущем) — YAML;
- никаких секретов в YAML.

## Решение

**Двухуровневая конфигурация:**
1. **Env (primary)** через `github.com/kelseyhightower/envconfig` (Context7 ID `/kelseyhightower/envconfig`, High reputation, Score 87.4) — все скаляры, списки, секреты.
2. **YAML overlay (optional)** — только для структурированных не-секретных настроек: `configs/normalize.yaml` (обязательный, regex-профиль), `configs/overrides.yaml` (опциональный, host-specific).

При коллизии **env выигрывает** — всегда.

### Структура Config

```go
// internal/config/api.go
type Config struct {
    TG struct {
        BotToken  string `envconfig:"TG_BOT_TOKEN" required:"true"`
        ChatID    int64  `envconfig:"TG_CHAT_ID" required:"true"`
        ThreadID  int64  `envconfig:"TG_THREAD_ID"` // опц., для супергруппы с topics
        ProxyURL  string `envconfig:"TG_PROXY_URL"` // v0.2
    }
    VL struct {
        URL       string `envconfig:"VL_URL" required:"true"`
        BasicUser string `envconfig:"VL_BASIC_USER"`
        BasicPass string `envconfig:"VL_BASIC_PASS"`
        ProxyURL  string `envconfig:"VL_PROXY_URL"` // v0.2
    }
    Grafana struct {
        URL      string `envconfig:"GRAFANA_URL" required:"true"`
        OrgID    int    `envconfig:"GRAFANA_ORG_ID" default:"1"`
        DSUID    string `envconfig:"GRAFANA_VL_DS_UID" required:"true"`
        DSType   string `envconfig:"GRAFANA_VL_DS_TYPE" default:"victoriametrics-logs-datasource"`
    }
    Hosts        []string      `envconfig:"HOSTS" default:"t1,ali-t1,t2,aws-t3,t5"`
    ScheduleCron string        `envconfig:"SCHEDULE_CRON" default:"0 8 * * *"`
    TZ           string        `envconfig:"TZ" default:"Europe/Moscow"`
    Levels       []string      `envconfig:"LEVELS" default:"error,critical"`
    NoiseK       int           `envconfig:"NOISE_K" default:"3"`
    TopN         int           `envconfig:"TOP_N" default:"20"`
    ReportFormat string        `envconfig:"REPORT_FORMAT" default:"md"`
    ReportsDir   string        `envconfig:"REPORTS_DIR" default:"/var/lib/log_analyser/reports"`
    StateDBPath  string        `envconfig:"STATE_DB_PATH" default:"/var/lib/log_analyser/state.db"`
    MetricsAddr  string        `envconfig:"METRICS_ADDR" default:":9090"`
    LogLevel     string        `envconfig:"LOG_LEVEL" default:"info"`
    LogFormat    string        `envconfig:"LOG_FORMAT" default:"json"`
    ConfigPath   string        `envconfig:"CONFIG_PATH" default:"/etc/log_analyser/overrides.yaml"`
    NormalizeProfilePath string `envconfig:"NORMALIZE_PROFILE_PATH" default:"/etc/log_analyser/normalize.yaml"`
    RetentionDays int          `envconfig:"REPORTS_RETENTION_DAYS" default:"30"`
}
```

### YAML overlay пример (`configs/example.yaml`)

```yaml
# Optional overrides; ENV wins on conflict.
hosts_overrides:
  t2:
    top_n: 40
    pattern_threshold: 20
```

### Load sequence

1. `envconfig.Process("", &cfg)` — fail если `required` пусто.
2. Если `CONFIG_PATH` существует — парсим YAML, применяем только непустые поля, **не** затрагивая зашедшее из env.
3. Валидация (`validate()`): `NoiseK >= 1`, `TopN >= 1`, `len(Hosts) >= 1`, cron корректен, TZ парсится, URL'ы — валидные.

## Последствия

**Плюсы**
- `envconfig` — zero-magic, доки короткие, Context7 покрывает всё (required, default, split_words, custom decoders).
- Env-first — 12-factor compliant, дружит с k8s Secret, Vault agent, docker `--env-file`.
- YAML-только-для-структур даёт diffability регекс-профилю (ревью в PR).
- Fail-fast при старте (required missing → exit 2 с понятным stderr, T-12).

**Минусы / trade-offs**
- Два источника истины (env + yaml) требуют дисциплины «что куда». Правило зафиксировано: секреты — только env, структуры — yaml.
- `envconfig` не поддерживает hot-reload; при изменении env нужен restart. Для нас ок — daemon рестартится легко.
- Custom types (например, `cron.Schedule`) требуют `Decoder` interface — не сложно.

## Альтернативы

1. **`spf13/viper`.** Отвергнуто: слишком «магический» (auto-bind env/flag/yaml/json/consul/...), больше surface → больше bugs, history показывает неожиданные поведения при precedence. Envconfig явнее.
2. **Только env, без YAML.** Отвергнуто: regex-профиль в env — нечитаемо, не diffabel (см. `10-architecture.md §12.4`).
3. **Только YAML, без env.** Отвергнуто: секреты в YAML → plaintext в volume / git → no-go (NFR-S1).
4. **TOML.** Отвергнуто: команда привыкла к YAML, и `golang-migrate`-style tools тоже yaml.
5. **`flag` stdlib + ручной парс.** Отвергнуто: много шаблонного кода, нет required-валидации.

## Ссылки

- Context7: `/kelseyhightower/envconfig` — Struct tags (`default`, `required`, `split_words`), custom `Decoder`.
- `docs/plans/10-architecture.md §12.4`.
- `.env.example` в корне проекта.
