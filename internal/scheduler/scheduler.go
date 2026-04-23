// Package scheduler — обёртка над robfig/cron/v3 для запуска digest cycle
// по расписанию (FR-1, ADR-0005).
//
// Контракт:
//   - один тик = один вызов pipeline.Run с детерминированным окном
//     [now.Truncate(Hour) - 24h, now.Truncate(Hour)). Таким образом
//     повторные запуски в том же часе (редкий кейс после рестарта
//     ровно на границе) идут на одно и то же окно и ловятся state'ом.
//   - cron работает в заданной TZ (config.TZ, по умолчанию Europe/Moscow).
//   - Stop ждёт завершения активного тика до истечения переданного ctx.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/QCoreTech/log_analyser/internal/pipeline"
)

// Runner — минимальный интерфейс pipeline, нужен для тестов.
type Runner interface {
	Run(ctx context.Context, window pipeline.Window) (*pipeline.Result, error)
}

// Config — параметры scheduler'а.
type Config struct {
	Schedule   string         // cron-выражение, напр. "0 8 * * *"
	TZ         *time.Location // требуется; nil → ошибка в Start
	RunTimeout time.Duration  // таймаут одного tick'а; 0 → 15 минут
}

// Scheduler состоит из cron + инжектированного runner'а.
type Scheduler struct {
	cfg    Config
	cron   *cron.Cron
	runner Runner
	logger *slog.Logger

	// active — счётчик активных тиков (для graceful stop диагностики).
	active atomic.Int32
	// now — мокабельно в тестах.
	now func() time.Time
}

// New создаёт scheduler. Запуск — через Start.
func New(cfg Config, runner Runner, logger *slog.Logger) (*Scheduler, error) {
	if cfg.TZ == nil {
		return nil, errors.New("TZ не задан")
	}
	if cfg.Schedule == "" {
		return nil, errors.New("Schedule пуст")
	}
	if cfg.RunTimeout <= 0 {
		cfg.RunTimeout = 15 * time.Minute
	}
	if runner == nil {
		return nil, errors.New("runner nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	c := cron.New(
		cron.WithLocation(cfg.TZ),
		cron.WithLogger(cron.VerbosePrintfLogger(newSlogBridge(logger))),
	)
	return &Scheduler{
		cfg: cfg, cron: c, runner: runner, logger: logger,
		now: time.Now,
	}, nil
}

// Start регистрирует job и запускает cron. Не блокирующий.
func (s *Scheduler) Start() error {
	if _, err := s.cron.AddFunc(s.cfg.Schedule, s.tick); err != nil {
		return fmt.Errorf("cron AddFunc %q: %w", s.cfg.Schedule, err)
	}
	s.cron.Start()
	s.logger.Info("scheduler started",
		slog.String("schedule", s.cfg.Schedule),
		slog.String("tz", s.cfg.TZ.String()),
	)
	return nil
}

// Stop останавливает cron и ждёт активные тики. Если ctx выходит раньше —
// возвращает ctx.Err(). Параллельный вызов Stop безопасен (cron.Stop()
// идемпотентен для запущенного scheduler'а).
func (s *Scheduler) Stop(ctx context.Context) error {
	stopCtx := s.cron.Stop()
	s.logger.Info("scheduler stopping", slog.Int("active_ticks", int(s.active.Load())))
	select {
	case <-stopCtx.Done():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// tick вызывается cron'ом по расписанию.
func (s *Scheduler) tick() {
	s.active.Add(1)
	defer s.active.Add(-1)

	now := s.now().In(s.cfg.TZ)
	// Нормируем окно на начало часа, в котором сработал cron — это даёт
	// идентичные границы при возможных повторных срабатываниях (редкий
	// edge при рестарте ровно в 08:00:30 после падения в 08:00:15).
	to := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, s.cfg.TZ)
	from := to.Add(-24 * time.Hour)

	s.logger.Info("scheduler tick",
		slog.Time("from", from),
		slog.Time("to", to),
	)

	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.RunTimeout)
	defer cancel()

	start := time.Now()
	res, err := s.runner.Run(ctx, pipeline.Window{From: from, To: to})
	dur := time.Since(start)
	if err != nil {
		s.logger.Error("scheduler run failed",
			slog.Duration("duration", dur),
			slog.String("err", err.Error()),
		)
		return
	}
	s.logger.Info("scheduler run ok",
		slog.Duration("duration", dur),
		slog.Int64("cover_msg_id", res.CoverMsgID),
		slog.Int("files", len(res.MediaMsgIDs)),
		slog.Int("partial_errors", len(res.Errors)),
	)
}

// ActiveTicks — справочно для диагностики/healthcheck.
func (s *Scheduler) ActiveTicks() int { return int(s.active.Load()) }

// NextSchedule возвращает время следующего тика (для /readyz/metrics в будущем).
func (s *Scheduler) NextSchedule() time.Time {
	entries := s.cron.Entries()
	if len(entries) == 0 {
		return time.Time{}
	}
	return entries[0].Next
}

// slogBridge — адаптер slog.Logger → cron.PrintfLogger. Используется как
// приёмник внутренних логов cron.
type slogBridge struct{ l *slog.Logger }

func newSlogBridge(l *slog.Logger) slogBridge { return slogBridge{l: l} }

func (b slogBridge) Printf(format string, args ...any) {
	b.l.Debug(fmt.Sprintf(format, args...))
}
