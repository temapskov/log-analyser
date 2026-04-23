// Command log-analyser — точка входа daemon'а и CLI-утилиты.
// Подкоманды (FR-19):
//
//	run                 — запуск daemon с cron-планировщиком (основной режим).
//	once --date=DATE    — ручной прогон digest за указанные сутки (формат YYYY-MM-DD в TZ).
//	health              — проверить reachability VL и Telegram, вывести диагностику.
//	version             — вывести build-info и выйти.
//
// Реализация каждой подкоманды подключается в следующих PR (collector, render,
// delivery, …). Сейчас — только диспетчер, version и health-stub.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/QCoreTech/log_analyser/internal/config"
	"github.com/QCoreTech/log_analyser/internal/observability/logging"
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

	// TODO(feat/scheduler): подключить scheduler + digest pipeline после
	// реализации collector/dedup/render/delivery. Пока держим процесс живым
	// до SIGINT/SIGTERM — это даёт реалистичную проверку graceful shutdown
	// и настроек logging/config на дымовой запуск контейнера.
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

	logger.Info("once: plan",
		slog.String("date", date),
		slog.String("tz", cfg.TZ),
		slog.String("hosts", strings.Join(cfg.Hosts, ",")),
	)
	// TODO(feat/pipeline): подменить на реальный digest cycle для указанных
	// суток. Пока стаб — чтобы CLI сигнатура стабилизировалась до кода.
	fmt.Fprintln(stderr, "once: пока не реализовано — TODO feat/pipeline")
	return exitRuntime
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
	// Пока что health = успешная загрузка конфига. В feat/collector и
	// feat/telegram сюда добавятся реальные пинги VL и TG getMe.
	logger.Info("health: config ok",
		slog.Int("hosts", len(cfg.Hosts)),
		slog.String("vl_url", cfg.VLURL),
		slog.String("grafana_url", cfg.GrafanaURL),
	)
	fmt.Fprintln(stdout, "ok: config loaded (полная проверка — в feat/collector+telegram)")
	return exitOK
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
