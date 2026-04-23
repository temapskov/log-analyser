// Package httpd — HTTP endpoints для observability: /metrics, /healthz, /readyz.
//
// Разделение healthz vs readyz:
//   - /healthz — liveness probe. Процесс жив, scheduler зарегистрирован,
//     state.db открыт. Быстрый, без внешних запросов.
//   - /readyz — readiness probe. Включает реальный ping VL и TG.getMe
//     (важно: без кэша оба чек занимали бы >100ms и могли бы залипать
//     прокси). Результат кэшируется на TTL, чтобы не DDoS'ить зависимости.
package httpd

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/QCoreTech/log_analyser/internal/observability/metrics"
)

// ReadyCheck — именованная проверка готовности.
type ReadyCheck struct {
	Name string
	// Check должен вернуть nil при OK, иначе — ошибку.
	Check func(ctx context.Context) error
}

// Server — минимальный HTTP-сервер для observability. Не использует
// http.DefaultServeMux.
type Server struct {
	addr         string
	metrics      *metrics.Metrics
	readyTTL     time.Duration
	checks       []ReadyCheck
	checkTimeout time.Duration

	mu         sync.Mutex
	lastCheck  time.Time
	lastAllOK  bool
	lastResult map[string]string // check -> "ok" | "error: ..."

	logger *slog.Logger
	server *http.Server
}

// Config параметры сервера.
type Config struct {
	Addr          string           // ":9090"
	Metrics       *metrics.Metrics // обязателен
	ReadyChecks   []ReadyCheck
	ReadyCacheTTL time.Duration // 0 → 30s
	CheckTimeout  time.Duration // 0 → 5s
}

func New(cfg Config, logger *slog.Logger) (*Server, error) {
	if cfg.Metrics == nil {
		return nil, errors.New("metrics nil")
	}
	if cfg.Addr == "" {
		cfg.Addr = ":9090"
	}
	if cfg.ReadyCacheTTL <= 0 {
		cfg.ReadyCacheTTL = 30 * time.Second
	}
	if cfg.CheckTimeout <= 0 {
		cfg.CheckTimeout = 5 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		addr:         cfg.Addr,
		metrics:      cfg.Metrics,
		checks:       cfg.ReadyChecks,
		readyTTL:     cfg.ReadyCacheTTL,
		checkTimeout: cfg.CheckTimeout,
		logger:       logger,
	}, nil
}

func (s *Server) mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(s.metrics.Registry, promhttp.HandlerOpts{
		Registry:            s.metrics.Registry,
		EnableOpenMetrics:   true,
		MaxRequestsInFlight: 4,
	}))
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/readyz", s.handleReadyz)
	return mux
}

// Start — non-blocking; возвращает ошибку только на bind-фейле.
func (s *Server) Start() error {
	s.server = &http.Server{
		Addr:              s.addr,
		Handler:           s.mux(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		err := s.server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("observability HTTP server died", slog.String("err", err.Error()))
		}
	}()
	s.logger.Info("observability HTTP started", slog.String("addr", s.addr))
	return nil
}

// Shutdown — graceful.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	allOK, results := s.runReadyChecks(r.Context())

	w.Header().Set("Content-Type", "application/json")
	if !allOK {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": statusText(allOK),
		"checks": results,
	})
}

func statusText(ok bool) string {
	if ok {
		return "ok"
	}
	return "fail"
}

func (s *Server) runReadyChecks(ctx context.Context) (bool, map[string]string) {
	s.mu.Lock()
	fresh := s.lastCheck.IsZero() || time.Since(s.lastCheck) >= s.readyTTL
	if !fresh {
		cached := copyMap(s.lastResult)
		allOK := s.lastAllOK
		s.mu.Unlock()
		return allOK, cached
	}
	s.mu.Unlock()

	results := map[string]string{}
	allOK := true
	for _, c := range s.checks {
		checkCtx, cancel := context.WithTimeout(ctx, s.checkTimeout)
		start := time.Now()
		err := c.Check(checkCtx)
		cancel()
		dur := time.Since(start)

		if s.metrics != nil {
			s.metrics.ReadyzCheckDurationSeconds.WithLabelValues(c.Name).Observe(dur.Seconds())
			v := 0.0
			if err == nil {
				v = 1
			}
			s.metrics.ReadyzCheckStatus.WithLabelValues(c.Name).Set(v)
		}

		if err != nil {
			allOK = false
			results[c.Name] = "error: " + err.Error()
		} else {
			results[c.Name] = "ok"
		}
	}

	s.mu.Lock()
	s.lastCheck = time.Now()
	s.lastResult = results
	s.lastAllOK = allOK
	s.mu.Unlock()
	return allOK, copyMap(results)
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Ensure prometheus is imported (used only via promhttp; avoid lint warning
// if import pruner kicks in).
var _ = prometheus.NewRegistry
