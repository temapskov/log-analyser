package grafana

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		BaseURL: "https://grafana.example.com",
		OrgID:   1,
		DSUID:   "PD775F2863313E6C7",
		DSType:  "victoriametrics-logs-datasource",
	}
}

func TestConfig_Validate(t *testing.T) {
	cases := []struct {
		name    string
		mut     func(*Config)
		wantErr string
	}{
		{"ok", func(*Config) {}, ""},
		{"empty base", func(c *Config) { c.BaseURL = "" }, "BaseURL пуст"},
		{"bad url", func(c *Config) { c.BaseURL = "not-a-url" }, "не похож на URL"},
		{"missing scheme", func(c *Config) { c.BaseURL = "grafana.example.com" }, "не похож на URL"},
		{"zero org", func(c *Config) { c.OrgID = 0 }, "OrgID"},
		{"negative org", func(c *Config) { c.OrgID = -1 }, "OrgID"},
		{"empty uid", func(c *Config) { c.DSUID = " " }, "DSUID"},
		{"empty type", func(c *Config) { c.DSType = "" }, "DSType"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := validConfig()
			tc.mut(&c)
			err := c.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ожидали ошибку %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err=%q, want substring %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestExploreURL_BasicShape(t *testing.T) {
	// Зафиксированное окно для стабильных эпох (UnixMilli):
	//   2026-04-22T05:00:00Z = 1776834000000 ms
	//   2026-04-23T05:00:00Z = 1776920400000 ms
	from := time.Date(2026, 4, 22, 5, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 23, 5, 0, 0, 0, time.UTC)
	c := validConfig()
	u, err := c.ExploreURL(`{host="t5"} (level:=error OR level:=critical)`, from, to)
	if err != nil {
		t.Fatalf("ExploreURL: %v", err)
	}

	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	if parsed.Scheme != "https" {
		t.Errorf("scheme: %q", parsed.Scheme)
	}
	if parsed.Host != "grafana.example.com" {
		t.Errorf("host: %q", parsed.Host)
	}
	if parsed.Path != "/explore" {
		t.Errorf("path: %q", parsed.Path)
	}

	q := parsed.Query()
	if q.Get("schemaVersion") != "1" {
		t.Errorf("schemaVersion: %q", q.Get("schemaVersion"))
	}
	if q.Get("orgId") != "1" {
		t.Errorf("orgId: %q", q.Get("orgId"))
	}

	// panes должен распарситься обратно в структуру.
	var panes map[string]pane
	if err := json.Unmarshal([]byte(q.Get("panes")), &panes); err != nil {
		t.Fatalf("panes decode: %v (raw=%q)", err, q.Get("panes"))
	}
	p, ok := panes["a"]
	if !ok {
		t.Fatalf(`panes["a"] отсутствует: %+v`, panes)
	}
	if p.Datasource != c.DSUID {
		t.Errorf("panes[a].datasource: %q", p.Datasource)
	}
	if len(p.Queries) != 1 {
		t.Fatalf("queries: %d", len(p.Queries))
	}
	q0 := p.Queries[0]
	if q0.RefID != "A" {
		t.Errorf("refId: %q", q0.RefID)
	}
	if q0.Datasource.UID != c.DSUID || q0.Datasource.Type != c.DSType {
		t.Errorf("query datasource: %+v", q0.Datasource)
	}
	if !strings.Contains(q0.Expr, `host="t5"`) || !strings.Contains(q0.Expr, "level:=error") {
		t.Errorf("expr повреждён: %q", q0.Expr)
	}

	if p.Range.From != "1776834000000" {
		t.Errorf("range.from: %q", p.Range.From)
	}
	if p.Range.To != "1776920400000" {
		t.Errorf("range.to: %q", p.Range.To)
	}
}

func TestExploreURL_PreservesPathPrefix(t *testing.T) {
	// Grafana часто стоит за reverse-proxy на /grafana/.
	c := validConfig()
	c.BaseURL = "https://corp.example.com/grafana"
	u, err := c.ExploreURL("{host=\"t1\"}", time.Unix(1, 0), time.Unix(2, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(u, "/grafana/explore?") {
		t.Errorf("префикс path потерян: %s", u)
	}
}

func TestExploreURL_Errors(t *testing.T) {
	from := time.Now()
	to := from.Add(time.Hour)

	type tc struct {
		name    string
		mut     func(*Config)
		expr    string
		from    time.Time
		to      time.Time
		wantSub string
	}
	cases := []tc{
		{"empty expr", func(*Config) {}, "", from, to, "expr пуст"},
		{"zero from", func(*Config) {}, "x", time.Time{}, to, "нулевые"},
		{"from == to", func(*Config) {}, "x", from, from, "должно быть строго меньше"},
		{"from > to", func(*Config) {}, "x", to, from, "должно быть строго меньше"},
		{"bad config", func(c *Config) { c.DSUID = "" }, "x", from, to, "DSUID"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			cfg := validConfig()
			c.mut(&cfg)
			if _, err := cfg.ExploreURL(c.expr, c.from, c.to); err == nil {
				t.Fatal("ожидали ошибку")
			} else if !strings.Contains(err.Error(), c.wantSub) {
				t.Errorf("err=%q, want substring %q", err.Error(), c.wantSub)
			}
		})
	}
}

func TestHostExpr(t *testing.T) {
	cases := []struct {
		name   string
		host   string
		levels []string
		want   string
	}{
		{"no levels", "t5", nil, `{host="t5"}`},
		{"empty string level", "t5", []string{"", "  "}, `{host="t5"}`},
		{"one level", "t5", []string{"error"}, `{host="t5"} level:=error`},
		{"two levels", "t5", []string{"error", "critical"}, `{host="t5"} (level:=error OR level:=critical)`},
		{"hyphenated host", "ali-t1", []string{"error"}, `{host="ali-t1"} level:=error`},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := HostExpr(c.host, c.levels)
			if got != c.want {
				t.Errorf("got=%q, want=%q", got, c.want)
			}
		})
	}
}

// TestExploreURL_Stability — смок-проверка на регрессии сериализации: JSON
// map должен порождать стабильный URL (encoding/json упорядочивает ключи
// map'а лексикографически). Если Grafana когда-то поменяет ожидания —
// этот тест отловит изменение.
func TestExploreURL_Stability(t *testing.T) {
	c := validConfig()
	from := time.Date(2026, 4, 22, 5, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 23, 5, 0, 0, 0, time.UTC)
	const expr = `{host="t5"} (level:=error OR level:=critical)`

	first, err := c.ExploreURL(expr, from, to)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		got, err := c.ExploreURL(expr, from, to)
		if err != nil {
			t.Fatal(err)
		}
		if got != first {
			t.Fatalf("URL нестабилен на попытке %d:\n  first: %s\n  got:   %s", i, first, got)
		}
	}
}
