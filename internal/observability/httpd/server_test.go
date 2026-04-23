package httpd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QCoreTech/log_analyser/internal/observability/metrics"
)

func newServer(t *testing.T, checks []ReadyCheck) *Server {
	t.Helper()
	s, err := New(Config{
		Addr:          ":0",
		Metrics:       metrics.New("test", "deadbeef"),
		ReadyChecks:   checks,
		ReadyCacheTTL: 50 * time.Millisecond,
		CheckTimeout:  100 * time.Millisecond,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestHealthz_AlwaysOK(t *testing.T) {
	s := newServer(t, nil)
	r := httptest.NewRecorder()
	s.handleHealthz(r, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if r.Code != 200 {
		t.Errorf("code %d", r.Code)
	}
	if !strings.Contains(r.Body.String(), `"status":"ok"`) {
		t.Errorf("body: %s", r.Body.String())
	}
}

func TestReadyz_AllOK(t *testing.T) {
	s := newServer(t, []ReadyCheck{
		{Name: "vl", Check: func(context.Context) error { return nil }},
		{Name: "tg", Check: func(context.Context) error { return nil }},
	})
	r := httptest.NewRecorder()
	s.handleReadyz(r, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if r.Code != 200 {
		t.Errorf("code %d, body=%s", r.Code, r.Body.String())
	}
	var out struct {
		Status string
		Checks map[string]string
	}
	_ = json.NewDecoder(r.Body).Decode(&out)
	if out.Status != "ok" {
		t.Errorf("status: %s", out.Status)
	}
	if out.Checks["vl"] != "ok" || out.Checks["tg"] != "ok" {
		t.Errorf("checks: %+v", out.Checks)
	}
}

func TestReadyz_OneFails_503(t *testing.T) {
	s := newServer(t, []ReadyCheck{
		{Name: "vl", Check: func(context.Context) error { return nil }},
		{Name: "tg", Check: func(context.Context) error { return errors.New("bad token") }},
	})
	r := httptest.NewRecorder()
	s.handleReadyz(r, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if r.Code != http.StatusServiceUnavailable {
		t.Errorf("code %d, body=%s", r.Code, r.Body.String())
	}
	if !strings.Contains(r.Body.String(), "bad token") {
		t.Errorf("body must contain error text: %s", r.Body.String())
	}
}

func TestReadyz_CachesResult(t *testing.T) {
	var calls int32
	s := newServer(t, []ReadyCheck{
		{Name: "x", Check: func(context.Context) error {
			atomic.AddInt32(&calls, 1)
			return nil
		}},
	})
	// TTL 50ms. Три вызова подряд должны уложиться в 1 call.
	for i := 0; i < 3; i++ {
		r := httptest.NewRecorder()
		s.handleReadyz(r, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("кэш не отработал: got=%d calls, want=1", got)
	}
	// После TTL + epsilon — снова должен проверить.
	time.Sleep(80 * time.Millisecond)
	r := httptest.NewRecorder()
	s.handleReadyz(r, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("после TTL должен быть новый вызов: got=%d, want=2", got)
	}
}

func TestMetrics_EndpointExposesCustom(t *testing.T) {
	m := metrics.New("1.2.3", "cafebabe")
	s, err := New(Config{Addr: ":0", Metrics: m}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Инкрементнем метрики, чтобы они попали в вывод.
	m.DigestCycleTotal.WithLabelValues("ok").Inc()
	m.LastSuccessfulDeliveryTS.Set(1_700_000_000)

	ts := httptest.NewServer(s.mux())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	s1 := string(b)
	for _, want := range []string{
		"log_analyser_digest_cycle_total",
		`log_analyser_build_info{commit="cafebabe",version="1.2.3"}`,
		"log_analyser_last_successful_delivery_timestamp_seconds 1.7e+09",
		"go_goroutines",
	} {
		if !strings.Contains(s1, want) {
			t.Errorf("missing %q in /metrics:\n%s", want, s1[:min(2000, len(s1))])
		}
	}
}

func TestStartShutdown_BindEphemeral(t *testing.T) {
	s := newServer(t, nil)
	s.addr = "127.0.0.1:0" // случайный порт
	// Для теста надо явно собрать server — http.Server сам биндит порт в Start.
	// Здесь мы используем обычный Start, который биндит в goroutine; чтобы дождаться
	// bind'а — просто сделаем небольшую пазуу.
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
