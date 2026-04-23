// Package config грузит конфигурацию по ADR-0011:
//  1. envconfig.Process("", &cfg) — первичный источник (env).
//  2. Опциональный YAML overlay — только для структурных данных (host-overrides,
//     regex-профиль дедупа); секреты в YAML запрещены и отсеиваются на этапе
//     валидации.
//  3. Validate() — обязательные поля, согласованность флагов.
//
// Env-поля именуются ровно как в .env.example (envconfig-префикс "" отключает
// автоматическое префиксование). Список секретов, подлежащих маскированию
// в логах, отдаётся через SensitiveValues().
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kelseyhightower/envconfig"
	"gopkg.in/yaml.v3"
)

type Format string

const (
	FormatMarkdown Format = "md"
	FormatHTML     Format = "html"
	FormatText     Format = "txt"
)

type Config struct {
	// ---- Telegram ----
	TGBotToken  string `envconfig:"TG_BOT_TOKEN" required:"true"`
	TGChatID    int64  `envconfig:"TG_CHAT_ID" required:"true"`
	TGProxyURL  string `envconfig:"TG_PROXY_URL"`
	TGParseMode string `envconfig:"TG_PARSE_MODE" default:"HTML"`

	// ---- VictoriaLogs ----
	VLURL        string        `envconfig:"VL_URL" required:"true"`
	VLBasicUser  string        `envconfig:"VL_BASIC_USER"`
	VLBasicPass  string        `envconfig:"VL_BASIC_PASS"`
	VLProxyURL   string        `envconfig:"VL_PROXY_URL"`
	VLTimeout    time.Duration `envconfig:"VL_TIMEOUT" default:"30s"`
	VLMaxRetries int           `envconfig:"VL_MAX_RETRIES" default:"3"`

	// ---- Grafana ----
	GrafanaURL      string `envconfig:"GRAFANA_URL" required:"true"`
	GrafanaOrgID    int    `envconfig:"GRAFANA_ORG_ID" default:"1"`
	GrafanaVLDSUID  string `envconfig:"GRAFANA_VL_DS_UID" required:"true"`
	GrafanaVLDSType string `envconfig:"GRAFANA_VL_DS_TYPE" default:"victoriametrics-logs-datasource"`

	// ---- Расписание и окно ----
	Hosts         []string      `envconfig:"HOSTS" default:"t1,ali-t1,t2,aws-t3,t5"`
	ScheduleCron  string        `envconfig:"SCHEDULE_CRON" default:"0 8 * * *"`
	TZ            string        `envconfig:"TZ" default:"Europe/Moscow"`
	Levels        []string      `envconfig:"LEVELS" default:"error,critical"`
	WindowPadding time.Duration `envconfig:"WINDOW_PADDING" default:"0s"`

	// ---- Дедуп и шум ----
	NoiseK int `envconfig:"NOISE_K" default:"5"`
	TopN   int `envconfig:"TOP_N" default:"20"`

	// ---- Файлы ----
	ReportFormat         Format `envconfig:"REPORT_FORMAT" default:"md"`
	ReportsDir           string `envconfig:"REPORTS_DIR" default:"/var/lib/log_analyser/reports"`
	ReportsRetentionDays int    `envconfig:"REPORTS_RETENTION_DAYS" default:"30"`

	// ---- State ----
	StateDBPath string `envconfig:"STATE_DB_PATH" default:"/var/lib/log_analyser/state.db"`

	// ---- Наблюдаемость ----
	MetricsAddr string `envconfig:"METRICS_ADDR" default:":9090"`
	LogLevel    string `envconfig:"LOG_LEVEL" default:"info"`
	LogFormat   string `envconfig:"LOG_FORMAT" default:"json"`

	// ---- YAML overlay ----
	// Путь необязателен. Если файл отсутствует — работаем на одном env.
	ConfigFile string `envconfig:"CONFIG_FILE"`

	// ---- YAML-only поля, не читаются из env ----
	Overlay Overlay `ignored:"true"`
}

// Overlay — секция YAML, не читается из env. Сюда кладутся только
// структурные данные, которые в плоских env выражаются плохо:
// regex-профиль для дедупа (ADR-0009), host-overrides и т.п.
type Overlay struct {
	Fingerprint FingerprintProfile `yaml:"fingerprint"`
	HostLabels  map[string]string  `yaml:"host_labels"`
}

type FingerprintProfile struct {
	// Normalizers — список регэкспов, последовательно применяемых к _msg
	// перед хэшированием. Порядок матчит порядок в файле.
	Normalizers []Normalizer `yaml:"normalizers"`
}

type Normalizer struct {
	Name    string `yaml:"name"`
	Pattern string `yaml:"pattern"`
	Replace string `yaml:"replace"`
}

// Load читает env через envconfig и, если указан CONFIG_FILE, накладывает YAML.
// Валидация — на вызывающем; обычно через Validate.
func Load() (*Config, error) {
	var cfg Config
	if err := envconfig.Process("", &cfg); err != nil {
		return nil, fmt.Errorf("envconfig: %w", err)
	}
	if cfg.ConfigFile != "" {
		raw, err := os.ReadFile(cfg.ConfigFile)
		if err != nil {
			return nil, fmt.Errorf("чтение config-файла %q: %w", cfg.ConfigFile, err)
		}
		if err := applyOverlay(raw, &cfg); err != nil {
			return nil, fmt.Errorf("YAML overlay %q: %w", cfg.ConfigFile, err)
		}
	}
	return &cfg, nil
}

func applyOverlay(raw []byte, cfg *Config) error {
	// Strict mode: неизвестные поля — ошибка (ловим опечатки на старте).
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	var overlay Overlay
	if err := dec.Decode(&overlay); err != nil {
		return err
	}
	// ADR-0011: секреты в YAML запрещены. Ошибаемся громко, если кто-то
	// решил положить токен сюда "для удобства".
	if err := rejectSecretsInYAML(raw); err != nil {
		return err
	}
	cfg.Overlay = overlay
	return nil
}

// rejectSecretsInYAML — линтер: ищем ключи, внешне похожие на secret-носители,
// и отвергаем весь файл. Не заменяет code-review, но предотвращает типичный
// anti-pattern «закинули токен в config.yaml».
func rejectSecretsInYAML(raw []byte) error {
	banned := []string{
		"tg_bot_token",
		"tg_chat_id",
		"vl_basic_pass",
		"vl_basic_user",
		"grafana_api_key",
	}
	lower := strings.ToLower(string(raw))
	for _, key := range banned {
		if strings.Contains(lower, key+":") {
			return fmt.Errorf("в YAML-конфиге найден запрещённый ключ %q — секреты должны задаваться через env (ADR-0011)", key)
		}
	}
	return nil
}

// Validate проверяет согласованность и осмысленность значений.
// Env-required поля уже проверены envconfig, здесь — бизнес-правила.
func (c *Config) Validate() error {
	var errs []error

	if len(c.Hosts) == 0 {
		errs = append(errs, errors.New("HOSTS пуст — нечего анализировать"))
	}
	for _, h := range c.Hosts {
		if strings.TrimSpace(h) == "" {
			errs = append(errs, errors.New("HOSTS содержит пустое имя хоста"))
			break
		}
	}
	if len(c.Levels) == 0 {
		errs = append(errs, errors.New("LEVELS пуст — нечего фильтровать"))
	}
	switch c.ReportFormat {
	case FormatMarkdown, FormatHTML, FormatText:
	default:
		errs = append(errs, fmt.Errorf("REPORT_FORMAT=%q: ожидаются md|html|txt", c.ReportFormat))
	}
	if c.NoiseK < 1 {
		errs = append(errs, fmt.Errorf("NOISE_K=%d: должно быть ≥ 1", c.NoiseK))
	}
	if c.TopN < 1 {
		errs = append(errs, fmt.Errorf("TOP_N=%d: должно быть ≥ 1", c.TopN))
	}
	if c.ReportsRetentionDays < 1 {
		errs = append(errs, fmt.Errorf("REPORTS_RETENTION_DAYS=%d: должно быть ≥ 1", c.ReportsRetentionDays))
	}
	if c.VLMaxRetries < 0 {
		errs = append(errs, fmt.Errorf("VL_MAX_RETRIES=%d: должно быть ≥ 0", c.VLMaxRetries))
	}
	if c.GrafanaOrgID < 1 {
		errs = append(errs, fmt.Errorf("GRAFANA_ORG_ID=%d: должно быть ≥ 1", c.GrafanaOrgID))
	}
	if _, err := time.LoadLocation(c.TZ); err != nil {
		errs = append(errs, fmt.Errorf("TZ=%q: %w", c.TZ, err))
	}
	if !strings.HasPrefix(c.VLURL, "http://") && !strings.HasPrefix(c.VLURL, "https://") {
		errs = append(errs, fmt.Errorf("VL_URL=%q: требуется схема http:// или https://", c.VLURL))
	}
	if !strings.HasPrefix(c.GrafanaURL, "http://") && !strings.HasPrefix(c.GrafanaURL, "https://") {
		errs = append(errs, fmt.Errorf("GRAFANA_URL=%q: требуется схема http:// или https://", c.GrafanaURL))
	}
	return errors.Join(errs...)
}

// SensitiveValues — значения, которые нужно передать в logging.New(),
// чтобы они маскировались в JSON-логах (NFR-S2).
func (c *Config) SensitiveValues() []string {
	v := []string{c.TGBotToken}
	if c.VLBasicPass != "" {
		v = append(v, c.VLBasicPass)
	}
	if c.VLBasicUser != "" {
		v = append(v, c.VLBasicUser)
	}
	return v
}
