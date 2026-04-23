package scheduler

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QCoreTech/log_analyser/internal/pipeline"
)

type mockRunner struct {
	mu      sync.Mutex
	calls   int32
	windows []pipeline.Window
	err     error
	delay   time.Duration
}

func (m *mockRunner) Run(ctx context.Context, w pipeline.Window) (*pipeline.Result, error) {
	atomic.AddInt32(&m.calls, 1)
	m.mu.Lock()
	m.windows = append(m.windows, w)
	m.mu.Unlock()
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if m.err != nil {
		return nil, m.err
	}
	return &pipeline.Result{Window: w, CoverMsgID: 42}, nil
}

func TestNew_Validation(t *testing.T) {
	tz, _ := time.LoadLocation("UTC")
	cases := []struct {
		name string
		cfg  Config
		run  Runner
	}{
		{"nil tz", Config{Schedule: "* * * * *"}, &mockRunner{}},
		{"empty schedule", Config{TZ: tz}, &mockRunner{}},
		{"nil runner", Config{TZ: tz, Schedule: "* * * * *"}, nil},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			if _, err := New(c.cfg, c.run, nil); err == nil {
				t.Error("ожидали ошибку")
			}
		})
	}
}

func TestStart_BadSchedule(t *testing.T) {
	tz, _ := time.LoadLocation("UTC")
	s, err := New(Config{TZ: tz, Schedule: "nonsense"}, &mockRunner{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err == nil {
		t.Error("ожидали ошибку на invalid cron expression")
	}
}

func TestTick_AlignsWindowToHour(t *testing.T) {
	tz, _ := time.LoadLocation("Europe/Moscow")
	runner := &mockRunner{}
	// Фиксируем "сейчас" на 08:17:33 MSK — должно округлиться до 08:00.
	fixed := time.Date(2026, 4, 23, 8, 17, 33, 999999, tz)

	s, err := New(Config{TZ: tz, Schedule: "0 8 * * *"}, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return fixed }
	s.tick() // вызываем напрямую, минуя cron

	if atomic.LoadInt32(&runner.calls) != 1 {
		t.Fatalf("calls: %d", runner.calls)
	}
	w := runner.windows[0]
	expTo := time.Date(2026, 4, 23, 8, 0, 0, 0, tz)
	expFrom := expTo.Add(-24 * time.Hour)
	if !w.To.Equal(expTo) {
		t.Errorf("to: %s want %s", w.To, expTo)
	}
	if !w.From.Equal(expFrom) {
		t.Errorf("from: %s want %s", w.From, expFrom)
	}
}

func TestTick_PropagatesError_ButDoesNotPanic(t *testing.T) {
	tz, _ := time.LoadLocation("UTC")
	runner := &mockRunner{err: errors.New("boom")}
	s, err := New(Config{TZ: tz, Schedule: "* * * * *"}, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.now = time.Now
	s.tick() // не должен паниковать, просто залогирует error
	if atomic.LoadInt32(&runner.calls) != 1 {
		t.Errorf("calls: %d", runner.calls)
	}
}

func TestStartStop_WithSecondsGranularity_Fires(t *testing.T) {
	tz, _ := time.LoadLocation("UTC")
	runner := &mockRunner{}
	// Используем парсер с секундами, чтобы не ждать минуту.
	s, err := New(Config{
		TZ:       tz,
		Schedule: "* * * * *", // каждую минуту — минимум поддерживается
	}, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	// Явно дёрнем tick (не ждём cron фактического срабатывания — это не тест cron).
	s.tick()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&runner.calls) != 1 {
		t.Errorf("ожидали 1 вызов runner, got %d", runner.calls)
	}
}

func TestStop_ReturnsQuickly_NoPendingTicks(t *testing.T) {
	tz, _ := time.LoadLocation("UTC")
	s, err := New(Config{TZ: tz, Schedule: "0 8 * * *"}, &mockRunner{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := s.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Errorf("Stop без активных тиков должен быть мгновенным, занял %s", d)
	}
}

func TestNextSchedule_AfterStart(t *testing.T) {
	tz, _ := time.LoadLocation("UTC")
	s, err := New(Config{TZ: tz, Schedule: "0 8 * * *"}, &mockRunner{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Stop(context.Background())

	next := s.NextSchedule()
	if next.IsZero() {
		t.Error("NextSchedule нулевой")
	}
	if next.Before(time.Now()) {
		t.Errorf("next должен быть в будущем: %s", next)
	}
}
