// Command log-analyser — точка входа daemon'а и CLI-утилиты.
// Подкоманды (FR-19):
//
//	run                 — запуск daemon с cron-планировщиком (основной режим).
//	once --date=DATE    — ручной прогон digest за указанные сутки (формат YYYY-MM-DD в TZ).
//	health              — проверить reachability VL и Telegram, вывести диагностику.
//	version             — вывести build-info и выйти.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/QCoreTech/log_analyser/internal/collector"
	"github.com/QCoreTech/log_analyser/internal/config"
	"github.com/QCoreTech/log_analyser/internal/dedup"
	"github.com/QCoreTech/log_analyser/internal/grafana"
	"github.com/QCoreTech/log_analyser/internal/httpclient"
	"github.com/QCoreTech/log_analyser/internal/observability/logging"
	"github.com/QCoreTech/log_analyser/internal/pipeline"
	"github.com/QCoreTech/log_analyser/internal/render"
	"github.com/QCoreTech/log_analyser/internal/state"
	"github.com/QCoreTech/log_analyser/internal/telegram"
	"github.com/QCoreTech/log_analyser/internal/version"
)

const (
	exitOK        = 0
	exitUsage     = 2
	exitConfig    = 3
	exitRuntime   = 4
	exitInterrupt = 130
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run — testable entrypoint. Возвращает exit-code, не вызывает os.Exit сам.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return exitUsage
	}

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, version.Current().String())
		return exitOK

	case "help", "--help", "-h":
		printUsage(stdout)
		return exitOK

	case "run":
		return cmdRun(rest, stdout, stderr)

	case "once":
		return cmdOnce(rest, stdout, stderr)

	case "health":
		return cmdHealth(rest, stdout, stderr)

	default:
		fmt.Fprintf(stderr, "неизвестная команда %q\n\n", cmd)
		printUsage(stderr)
		return exitUsage
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintf(w, `log_analyser — ежедневный Telegram-отчёт об ошибках торговых серверов

Использование:
  log-analyser <команда> [флаги]

Команды:
  run                  запустить daemon с cron-планировщиком
  once --date=YYYY-MM-DD
                       ручной прогон digest за указанные сутки
  health               проверка готовности (VL + TG getMe)
  version              версия и build-info
  help                 эта справка

Конфигурация — через env (см. .env.example) и опциональный YAML (CONFIG_FILE).

Документация:
  CLAUDE.md, docs/plans/*.md, docs/adr/*.md
  Issue: https://github.com/QCoreTech/awesome/issues/908
`)
}

func cmdRun(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, logger, code := loadConfigAndLogger(stdout, stderr)
	if code != exitOK {
		return code
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("daemon starting",
		slog.String("version", version.Version),
		slog.String("commit", version.Commit),
		slog.String("schedule", cfg.ScheduleCron),
		slog.String("tz", cfg.TZ),
		slog.String("hosts", strings.Join(cfg.Hosts, ",")),
	)

	// TODO(feat/scheduler): подключить cron + pipeline.Run().
	// Сейчас daemon просто ждёт SIGINT/SIGTERM — проверяется graceful shutdown.
	<-ctx.Done()
	logger.Info("daemon stopped", slog.String("reason", ctx.Err().Error()))
	return exitOK
}

func cmdOnce(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("once", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var date string
	fs.StringVar(&date, "date", "", "дата отчёта YYYY-MM-DD (в TZ); обязательно")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if date == "" {
		fmt.Fprintln(stderr, "once: --date обязателен (формат YYYY-MM-DD)")
		return exitUsage
	}

	cfg, logger, code := loadConfigAndLogger(stdout, stderr)
	if code != exitOK {
		return code
	}

	loc, err := time.LoadLocation(cfg.TZ)
	if err != nil {
		fmt.Fprintf(stderr, "TZ: %v\n", err)
		return exitConfig
	}
	// Окно digest'а: [prev_day 08:00 TZ, date 08:00 TZ).
	to, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		fmt.Fprintf(stderr, "once --date: %v\n", err)
		return exitUsage
	}
	// Нормируем на 08:00 локального времени.
	to = time.Date(to.Year(), to.Month(), to.Day(), 8, 0, 0, 0, loc)
	from := to.Add(-24 * time.Hour)

	logger.Info("once start",
		slog.String("date", date),
		slog.Time("from", from),
		slog.Time("to", to),
		slog.String("hosts", strings.Join(cfg.Hosts, ",")),
	)

	pipe, st, err := buildPipeline(cfg, loc, logger)
	if err != nil {
		fmt.Fprintf(stderr, "init pipeline: %v\n", err)
		return exitRuntime
	}
	if st != nil {
		defer st.Close()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	res, err := pipe.Run(ctx, pipeline.Window{From: from, To: to})
	if err != nil {
		fmt.Fprintf(stderr, "once failed: %v\n", err)
		return exitRuntime
	}

	for _, host := range cfg.Hosts {
		h := res.PerHost[host]
		status := "OK"
		if h.Err != nil {
			status = "ERR: " + h.Err.Error()
		}
		logger.Info("host result",
			slog.String("host", host),
			slog.String("file", h.FilePath),
			slog.Uint64("records", h.TotalRecords),
			slog.Int("incidents", h.Incidents),
			slog.String("status", status),
		)
	}
	fmt.Fprintf(stdout, "ok: cover=%d, files=%d, partial_errors=%d\n",
		res.CoverMsgID, len(res.MediaMsgIDs), len(res.Errors))
	return exitOK
}

func cmdHealth(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("health", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, logger, code := loadConfigAndLogger(stdout, stderr)
	if code != exitOK {
		return code
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	vl, tg, err := buildClients(cfg, logger)
	if err != nil {
		fmt.Fprintf(stderr, "health: %v\n", err)
		return exitRuntime
	}

	if err := vl.Ping(ctx); err != nil {
		fmt.Fprintf(stderr, "health: VL ping failed: %v\n", err)
		return exitRuntime
	}
	user, err := tg.GetMe(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "health: TG getMe failed: %v\n", err)
		return exitRuntime
	}
	logger.Info("health ok", slog.String("bot", user.Username), slog.Int64("bot_id", user.ID))
	fmt.Fprintf(stdout, "ok: VL reachable, TG bot=@%s\n", user.Username)
	return exitOK
}

// buildClients строит VL + TG клиенты из конфига. Используется health и once.
func buildClients(cfg *config.Config, logger *slog.Logger) (*collector.Client, *telegram.Client, error) {
	hc := httpclient.New(httpclient.Config{
		Timeout:         cfg.VLTimeout,
		MaxRetries:      cfg.VLMaxRetries,
		SensitiveValues: cfg.SensitiveValues(),
	}, logger)
	vl, err := collector.New(collector.Config{
		BaseURL:      cfg.VLURL,
		Username:     cfg.VLBasicUser,
		Password:     cfg.VLBasicPass,
		QueryTimeout: 5 * time.Minute,
	}, hc, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("VL: %w", err)
	}
	tgHTTP := &http.Client{
		Timeout:   60 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyFromEnvironment},
	}
	tg, err := telegram.New(telegram.Config{
		Token:         cfg.TGBotToken,
		HTTPTimeout:   60 * time.Second,
		MaxRetries:    2,
		MaxRetryAfter: 60 * time.Second,
	}, tgHTTP, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("TG: %w", err)
	}
	return vl, tg, nil
}

func buildPipeline(cfg *config.Config, loc *time.Location, logger *slog.Logger) (*pipeline.Pipeline, *state.Store, error) {
	vl, tg, err := buildClients(cfg, logger)
	if err != nil {
		return nil, nil, err
	}
	rdr, err := render.New(cfg.TZ)
	if err != nil {
		return nil, nil, fmt.Errorf("renderer: %w", err)
	}

	// Компиляция fingerprint-правил из YAML overlay или дефолтный набор.
	var rules []dedup.Rule
	if overlay := cfg.Overlay.Fingerprint.Normalizers; len(overlay) > 0 {
		raw := make([]dedup.RawRule, len(overlay))
		for i, n := range overlay {
			raw[i] = dedup.RawRule{Name: n.Name, Pattern: n.Pattern, Replace: n.Replace}
		}
		rules, err = dedup.Compile(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("fingerprint profile: %w", err)
		}
		logger.Info("загружен fingerprint-профиль из YAML", slog.Int("rules", len(rules)))
	} else {
		rules = dedup.DefaultRules()
		logger.Info("используется встроенный fingerprint-профиль", slog.Int("rules", len(rules)))
	}

	// State — опциональный. Если путь задан и директория доступна,
	// подключаем SQLite для FR-12 идемпотентности.
	var st *state.Store
	if cfg.StateDBPath != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.StateDBPath), 0o755); err != nil {
			return nil, nil, fmt.Errorf("mkdir state dir: %w", err)
		}
		st, err = state.Open(state.Config{Path: cfg.StateDBPath, Timezone: "UTC"}, logger)
		if err != nil {
			return nil, nil, fmt.Errorf("state.Open: %w", err)
		}
		logger.Info("state открыт", slog.String("path", cfg.StateDBPath))
	} else {
		logger.Warn("STATE_DB_PATH не задан — идемпотентность отключена")
	}

	pipe, err := pipeline.New(pipeline.Config{
		Hosts:            cfg.Hosts,
		Levels:           cfg.Levels,
		NoiseK:           cfg.NoiseK,
		TopN:             cfg.TopN,
		MaxExamples:      3,
		ReportsDir:       cfg.ReportsDir,
		ReportExt:        string(cfg.ReportFormat),
		TZ:               loc,
		HostLabels:       cfg.Overlay.HostLabels,
		ChatID:           cfg.TGChatID,
		ParseMode:        cfg.TGParseMode,
		FingerprintRules: rules,
	}, pipeline.Dependencies{
		VL:       vl,
		TG:       tg,
		Renderer: rdr,
		State:    st,
		Grafana: grafana.Config{
			BaseURL: cfg.GrafanaURL,
			OrgID:   cfg.GrafanaOrgID,
			DSUID:   cfg.GrafanaVLDSUID,
			DSType:  cfg.GrafanaVLDSType,
		},
		Logger: logger,
	})
	if err != nil {
		if st != nil {
			_ = st.Close()
		}
		return nil, nil, err
	}
	return pipe, st, nil
}

// loadConfigAndLogger — общая инициализация для всех команд, кроме version/help.
// При ошибке печатает её в stderr и возвращает exitConfig.
func loadConfigAndLogger(_, stderr io.Writer) (*config.Config, *slog.Logger, int) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "config: %v\n", err)
		return nil, nil, exitConfig
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(stderr, "config validation: %v\n", err)
		return nil, nil, exitConfig
	}

	lvl, err := logging.ParseLevel(cfg.LogLevel)
	if err != nil {
		fmt.Fprintf(stderr, "config: %v\n", err)
		return nil, nil, exitConfig
	}
	lf, err := logging.ParseFormat(cfg.LogFormat)
	if err != nil {
		fmt.Fprintf(stderr, "config: %v\n", err)
		return nil, nil, exitConfig
	}
	logger := logging.New(logging.Options{
		Writer:  stderr,
		Level:   lvl,
		Format:  lf,
		Secrets: cfg.SensitiveValues(),
	})
	return cfg, logger, exitOK
}
