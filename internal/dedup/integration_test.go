//go:build integration

// Измерение compression ratio dedup'а на реальных логах VictoriaLogs.
// Тянет до N error/critical-записей с каждого prod-хоста, прогоняет
// через Aggregator с DefaultNormalizer() и печатает статистику.
//
// Тест не падает на плохом ratio — он информативный. Падает, если VL
// недоступна или возвращает противоречивый формат.
//
// Запуск: `make test-integration`.
package dedup

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/QCoreTech/log_analyser/internal/collector"
	"github.com/QCoreTech/log_analyser/internal/httpclient"
)

func newVLClient(t *testing.T) *collector.Client {
	t.Helper()
	base := os.Getenv("VL_URL")
	if base == "" {
		t.Skip("VL_URL не задан — скип")
	}
	hc := httpclient.New(httpclient.Config{
		Timeout:    30 * time.Second,
		MaxRetries: 1,
	}, nil)
	hc.HTTP().Transport = &http.Transport{Proxy: nil}
	c, err := collector.New(collector.Config{
		BaseURL:      base,
		QueryTimeout: 60 * time.Second,
	}, hc, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestDedup_Integration_RealCompression тянет по 5000 error/critical-записей
// с каждого prod-хоста и выводит compression ratio. Главный КPI этого теста:
// соотношение #записей / #инцидентов (больше — лучше).
func TestDedup_Integration_RealCompression(t *testing.T) {
	c := newVLClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	hosts := []string{"t1", "ali-t1", "t2", "aws-t3", "t5"}
	now := time.Now().UTC()

	agg := NewAggregator(DefaultNormalizer(), 3)
	totalRecords := 0
	perHost := map[string]int{}

	for _, host := range hosts {
		q := collector.Query{
			Stream: collector.StreamFilter{Host: host, Levels: []string{"error", "critical"}},
			From:   now.Add(-24 * time.Hour),
			To:     now,
			Pipe:   "| limit 5000",
		}
		got := 0
		err := c.StreamQuery(ctx, q, func(e collector.LogEntry) error {
			agg.Add(e)
			got++
			return nil
		})
		if err != nil {
			t.Errorf("host=%s: %v", host, err)
			continue
		}
		perHost[host] = got
		totalRecords += got
	}

	t.Logf("=== dedup compression (24h × 5 хостов, limit 5000/хост) ===")
	t.Logf("всего записей: %d", totalRecords)
	for _, host := range hosts {
		recs := perHost[host]
		s := agg.SummaryFor(host, 5)
		if recs == 0 {
			t.Logf("  %-10s: нет error/critical", host)
			continue
		}
		ratio := float64(recs) / float64(max1(s.TotalIncidents))
		t.Logf("  %-10s: records=%5d incidents=%4d  ratio=%5.1f×  above_noise=%3d  below=%3d (%d rec)",
			host, recs, s.TotalIncidents, ratio, len(s.Above), len(s.Below), s.BelowRecords)
	}

	if totalRecords == 0 {
		t.Skip("нет данных для анализа")
	}
	// Валидация формата: по каждому хосту хотя бы один инцидент должен
	// иметь непустые Examples и валидный fingerprint.
	for _, host := range hosts {
		incs := agg.IncidentsFor(host)
		if len(incs) == 0 {
			continue
		}
		top := incs[0]
		if top.Fingerprint == "" || len(top.Fingerprint) != 40 {
			t.Errorf("host=%s top fingerprint кривой: %q", host, top.Fingerprint)
		}
		if len(top.Examples) == 0 {
			t.Errorf("host=%s top без Examples", host)
		}
		if top.Count == 0 {
			t.Errorf("host=%s top.Count == 0", host)
		}
	}
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}
