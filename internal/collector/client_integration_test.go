//go:build integration

// Smoke-тест VL-клиента против реальной VictoriaLogs из .env.
// Запуск: `make test-integration` (читает .env).
package collector

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/QCoreTech/log_analyser/internal/httpclient"
)

func newIntegrationClient(t *testing.T) *Client {
	t.Helper()
	base := os.Getenv("VL_URL")
	if base == "" {
		t.Skip("VL_URL не задан — скип")
	}
	hc := httpclient.New(httpclient.Config{
		Timeout:    15 * time.Second,
		MaxRetries: 2,
	}, nil)
	// Для внутренних IP принудительно отключаем прокси — HTTP_PROXY в shell
	// ломает запросы к 10.x.x.x (см. fix из feat/grafana-deeplink).
	hc.HTTP().Transport = &http.Transport{Proxy: nil}
	c, err := New(Config{
		BaseURL:      base,
		Username:     os.Getenv("VL_BASIC_USER"),
		Password:     os.Getenv("VL_BASIC_PASS"),
		QueryTimeout: 30 * time.Second,
	}, hc, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPing_Integration(t *testing.T) {
	c := newIntegrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestStreamQuery_Integration_T5Errors24h(t *testing.T) {
	c := newIntegrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	now := time.Now().UTC()
	q := Query{
		Stream: StreamFilter{Host: "t5", Levels: []string{"error", "critical"}},
		From:   now.Add(-24 * time.Hour),
		To:     now,
		Pipe:   "| limit 100",
	}

	var (
		count    int
		firstErr LogEntry
	)
	err := c.StreamQuery(ctx, q, func(e LogEntry) error {
		if count == 0 {
			firstErr = e
		}
		count++
		return nil
	})
	if err != nil {
		t.Fatalf("StreamQuery: %v", err)
	}
	if count == 0 {
		t.Skipf("t5 за последние 24ч не дал ни одной error/critical-записи — проверка полезного формата невозможна")
	}
	t.Logf("t5 за 24ч: %d записей (limit 100)", count)
	// Валидация формата первой записи:
	if firstErr.Host != "t5" {
		t.Errorf("Host=%q, want=t5", firstErr.Host)
	}
	if firstErr.Level != "error" && firstErr.Level != "critical" {
		t.Errorf("Level=%q, want error|critical", firstErr.Level)
	}
	if firstErr.Time.IsZero() {
		t.Errorf("Time не распарсился")
	}
	if firstErr.Msg == "" {
		t.Errorf("Msg пустой")
	}
	if firstErr.StreamID == "" {
		t.Errorf("StreamID пустой")
	}
}

func TestHits_Integration_T5Last1h(t *testing.T) {
	c := newIntegrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	now := time.Now().UTC()
	q := Query{
		Stream: StreamFilter{Host: "t5"},
		From:   now.Add(-1 * time.Hour),
		To:     now,
	}
	h, err := c.Hits(ctx, q)
	if err != nil {
		t.Fatalf("Hits: %v", err)
	}
	if h == nil || len(h.Hits) == 0 {
		t.Skip("нет данных за последний час — пропускаем")
	}
	t.Logf("t5 за 1ч: total=%d points=%d", h.Hits[0].Total, len(h.Hits[0].Values))
}
