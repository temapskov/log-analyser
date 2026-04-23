package render

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/QCoreTech/log_analyser/internal/dedup"
)

func mustTime(layout, s string) time.Time {
	t, err := time.Parse(layout, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestNew_RejectsBadTZ(t *testing.T) {
	if _, err := New("Nowhere/Atlantis"); err == nil {
		t.Fatal("ожидали ошибку на кривую TZ")
	}
}

func TestRenderHost_NoRecords_ShowsEmptyNotice(t *testing.T) {
	r, err := New("UTC")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	err = r.RenderHost(&buf, HostReport{
		Host: "t1",
		TZ:   "UTC",
		Window: Window{
			From: mustTime(time.RFC3339, "2026-04-22T05:00:00Z"),
			To:   mustTime(time.RFC3339, "2026-04-23T05:00:00Z"),
		},
		GeneratedAt:  mustTime(time.RFC3339, "2026-04-23T08:00:00Z"),
		HostDeeplink: "https://grafana/explore?host=t1",
	})
	if err != nil {
		t.Fatalf("RenderHost: %v", err)
	}
	s := buf.String()
	for _, want := range []string{
		"# Отчёт по ошибкам: `t1`",
		"За окно ошибок уровня `error` / `critical` не обнаружено",
		"[Открыть в Grafana →](https://grafana/explore?host=t1)",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
}

func TestRenderHost_HappyPath(t *testing.T) {
	r, err := New("Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	from := mustTime(time.RFC3339, "2026-04-22T05:00:00Z")
	to := mustTime(time.RFC3339, "2026-04-23T05:00:00Z")

	incA := &dedup.Incident{
		Fingerprint:   dedup.Compute("t5", "agent", "norm-a"),
		Host:          "t5",
		App:           "agent",
		Level:         "error",
		Module:        "strategy:50",
		Count:         412,
		FirstSeen:     from,
		LastSeen:      to,
		NormalizedMsg: "<ts> | E | strategy:<N> | Ошибка X",
		Examples:      []string{"23.04.2026 06:35:19 | E | strategy:50 | Ошибка X"},
		StreamIDs:     map[string]struct{}{"s1": {}, "s2": {}},
	}
	incB := &dedup.Incident{
		Fingerprint:   dedup.Compute("t5", "agent", "norm-b"),
		Host:          "t5",
		App:           "md-gateway",
		Level:         "critical",
		Count:         12,
		FirstSeen:     from,
		LastSeen:      to,
		NormalizedMsg: "connection refused to <ip>",
		Examples:      []string{"connection refused to 10.0.1.45:8443"},
		StreamIDs:     map[string]struct{}{"s3": {}},
	}
	below := &dedup.Incident{
		Fingerprint:   dedup.Compute("t5", "agent", "norm-c"),
		Host:          "t5",
		App:           "agent",
		Level:         "error",
		Count:         2,
		NormalizedMsg: "noisy edge",
	}

	rep := HostReport{
		Host:           "t5",
		Label:          "Прод-T5",
		TZ:             "Europe/Moscow",
		Window:         Window{From: from, To: to},
		GeneratedAt:    mustTime(time.RFC3339, "2026-04-23T05:01:00Z"),
		TotalError:     420,
		TotalCritical:  12,
		TotalRecords:   434,
		TotalIncidents: 3,
		AppTotals: []dedup.AppTotals{
			{App: "agent", Error: 414, Total: 414},
			{App: "md-gateway", Critical: 12, Total: 12},
		},
		TopIncidents: []IncidentView{
			{Incident: incA, Deeplink: "https://grafana/explore?a"},
			{Incident: incB, Deeplink: "https://grafana/explore?b"},
		},
		BelowIncidents: []IncidentView{{Incident: below, Deeplink: "https://grafana/explore?c"}},
		BelowRecords:   2,
		TopN:           2,
		NoiseK:         5,
		HostDeeplink:   "https://grafana/explore?host=t5",
	}

	var buf bytes.Buffer
	if err := r.RenderHost(&buf, rep); err != nil {
		t.Fatalf("RenderHost: %v", err)
	}
	s := buf.String()
	for _, want := range []string{
		"# Отчёт по ошибкам: `t5` (Прод-T5)",
		"**Период:** 2026-04-22 08:00:00",
		"| `agent` | 414 | 0 | 414 |",
		"| `md-gateway` | 0 | 12 | 12 |",
		"## Топ-2 инцидентов",
		"### 1. [`agent`]",
		"### 2. [`md-gateway`]",
		"(https://grafana/explore?a)",
		"(https://grafana/explore?b)",
		"## Прочее (ниже шум-порога K=5)",
		"**1** инцидентов, **2** записей",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in output:\n%s", want, s)
		}
	}
}

func TestRenderCover_Shape(t *testing.T) {
	r, err := New("Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	from := mustTime(time.RFC3339, "2026-04-22T05:00:00Z")
	to := mustTime(time.RFC3339, "2026-04-23T05:00:00Z")

	var buf bytes.Buffer
	err = r.RenderCover(&buf, CoverData{
		TZ:     "Europe/Moscow",
		Window: Window{From: from, To: to},
		Hosts: []CoverHostRow{
			{Host: "t1", TotalError: 1204, TotalCritical: 12, TopApp: "order-svc"},
			{Host: "ali-t1", TotalError: 412, TotalCritical: 3, TopApp: "md-gateway"},
			{Host: "t2", TotalError: 8912, TotalCritical: 45, TopApp: "exec-engine"},
			{Host: "aws-t3", TotalError: 87, TotalCritical: 0, TopApp: "risk-engine"},
			{Host: "t5", TotalError: 2041, TotalCritical: 18, TopApp: "fix-gw"},
		},
		TotalError:       12656,
		TotalCritical:    78,
		TotalIncidents:   137,
		AllHostsDeeplink: "https://grafana/explore?all",
	})
	if err != nil {
		t.Fatalf("RenderCover: %v", err)
	}
	s := buf.String()
	for _, want := range []string{
		"<b>Ежедневный отчёт об ошибках торговых серверов</b>",
		"22.04.2026 08:00 — 23.04.2026 08:00 (Europe/Moscow)",
		"t1",
		"t2",
		"8912",
		"12656 error, 78 critical",
		`<a href="https://grafana/explore?all">`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "[partial-report]") {
		t.Errorf("неожиданный partial-маркер")
	}
}

func TestRenderCover_PartialDelivery(t *testing.T) {
	r, err := New("UTC")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	err = r.RenderCover(&buf, CoverData{
		Window:           Window{From: time.Unix(0, 0), To: time.Unix(3600, 0)},
		PartialDelivery:  true,
		AllHostsDeeplink: "https://g/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "[partial-report]") {
		t.Errorf("маркер partial должен быть:\n%s", buf.String())
	}
}

func TestRenderCover_EscapesHTMLInTopApp(t *testing.T) {
	r, _ := New("UTC")
	var buf bytes.Buffer
	err := r.RenderCover(&buf, CoverData{
		Window: Window{From: time.Unix(0, 0), To: time.Unix(3600, 0)},
		Hosts: []CoverHostRow{
			{Host: "t5", TotalError: 1, TopApp: `<script>alert("xss")</script>`},
		},
		AllHostsDeeplink: "https://g",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := buf.String()
	if strings.Contains(s, "<script>") {
		t.Errorf("сырой <script> просочился:\n%s", s)
	}
	if !strings.Contains(s, "&lt;script&gt;") {
		t.Errorf("ожидаем escaped variant: %s", s)
	}
}

func TestFirstLine(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"hello", "hello"},
		{"line1\nline2\nline3", "line1"},
		{"  spaced  ", "spaced"},
		{strings.Repeat("a", 300), strings.Repeat("a", 160) + "…"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.in[:min(10, len(c.in))], func(t *testing.T) {
			if got := firstLine(c.in); got != c.want {
				t.Errorf("got=%q want=%q", got, c.want)
			}
		})
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
