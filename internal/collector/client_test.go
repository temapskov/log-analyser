package collector

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QCoreTech/log_analyser/internal/httpclient"
)

func newTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	hc := httpclient.New(httpclient.Config{MaxRetries: 1, RetryBaseDelay: time.Millisecond}, nil)
	c, err := New(Config{BaseURL: baseURL}, hc, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestConfig_Validate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{"ok", Config{BaseURL: "http://x"}, ""},
		{"empty", Config{}, "BaseURL пуст"},
		{"bad scheme", Config{BaseURL: "vl.example.com"}, "требуется схема"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := c.cfg.validate()
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("err=%v, want %q", err, c.wantErr)
			}
		})
	}
}

func TestStreamQuery_Parses_RealVLPayload(t *testing.T) {
	// Реальный формат VL (проверен 2026-04-23 на prod: 10.145.0.43:9428).
	payload := strings.TrimSpace(`
{"_time":"2026-04-23T06:35:19.484087Z","_stream_id":"00000000000000003e58936810bdc6f326919ef35eb9ade0","_stream":"{app=\"agent\",host=\"t5\",job=\"xt\",level=\"error\",module=\"strategy:t_t5-1:50\"}","_msg":"Стратегия 'strat' сообщила об ошибке","app":"agent","host":"t5","job":"xt","level":"error","module":"strategy:t_t5-1:50"}
{"_time":"2026-04-23T06:36:00.000000Z","_stream_id":"aa","_stream":"{host=\"t5\"}","_msg":"second","host":"t5","level":"critical"}
`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("ожидали POST, got %s", r.Method)
		}
		_ = r.ParseForm()
		if q := r.Form.Get("query"); !strings.Contains(q, "t5") {
			t.Errorf("query не содержит t5: %q", q)
		}
		w.Header().Set("Content-Type", "application/x-jsonlines")
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	from := time.Date(2026, 4, 22, 5, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 23, 5, 0, 0, 0, time.UTC)
	q := Query{Stream: StreamFilter{Host: "t5", Levels: []string{"error", "critical"}}, From: from, To: to}

	var entries []LogEntry
	err := c.StreamQuery(context.Background(), q, func(e LogEntry) error {
		entries = append(entries, e)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamQuery: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("записей: got=%d, want=2", len(entries))
	}
	if entries[0].Host != "t5" || entries[0].Level != "error" || entries[0].App != "agent" {
		t.Errorf("entry 0: %+v", entries[0])
	}
	if entries[0].Time.IsZero() {
		t.Error("entry 0 Time не распарсился")
	}
	if entries[1].Level != "critical" {
		t.Errorf("entry 1 level: %q", entries[1].Level)
	}
}

func TestStreamQuery_CallbackCanAbort(t *testing.T) {
	payload := strings.Repeat(`{"_time":"2026-04-23T00:00:00Z","_stream_id":"x","_stream":"{}","_msg":"m","host":"t5","level":"error"}`+"\n", 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	q := Query{
		Stream: StreamFilter{Host: "t5", Levels: []string{"error"}},
		From:   time.Now().Add(-time.Hour),
		To:     time.Now(),
	}
	var got int
	err := c.StreamQuery(context.Background(), q, func(LogEntry) error {
		got++
		if got == 3 {
			return io.EOF
		}
		return nil
	})
	if err != nil {
		t.Fatalf("StreamQuery: %v", err)
	}
	if got != 3 {
		t.Errorf("abort after 3: got=%d", got)
	}
}

func TestStreamQuery_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, "invalid LogsQL")
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	q := Query{
		Stream: StreamFilter{Host: "t5"},
		From:   time.Now().Add(-time.Hour),
		To:     time.Now(),
	}
	err := c.StreamQuery(context.Background(), q, func(LogEntry) error { return nil })
	if err == nil {
		t.Fatal("ожидали ошибку")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("должно содержать status: %v", err)
	}
}

func TestStreamQuery_SkipsMalformedLines(t *testing.T) {
	payload := `{"_time":"2026-04-23T00:00:00Z","_msg":"ok","host":"t5","level":"error"}` + "\n" +
		`this is not json` + "\n" +
		`{"_time":"2026-04-23T00:01:00Z","_msg":"ok2","host":"t5","level":"error"}` + "\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	q := Query{
		Stream: StreamFilter{Host: "t5", Levels: []string{"error"}},
		From:   time.Now().Add(-time.Hour),
		To:     time.Now(),
	}
	var good int
	err := c.StreamQuery(context.Background(), q, func(e LogEntry) error {
		if e.Msg == "" {
			t.Errorf("пустой Msg: %+v", e)
		}
		good++
		return nil
	})
	if err != nil {
		t.Fatalf("StreamQuery: %v", err)
	}
	if good != 2 {
		t.Errorf("валидных записей: got=%d want=2", good)
	}
}

func TestHits_Parses(t *testing.T) {
	payload := `{"hits":[{"fields":{},"timestamps":["2026-04-23T00:00:00Z"],"values":[554272],"total":554272}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	q := Query{
		Stream: StreamFilter{Host: "t5"},
		From:   time.Now().Add(-time.Hour),
		To:     time.Now(),
	}
	h, err := c.Hits(context.Background(), q)
	if err != nil {
		t.Fatalf("Hits: %v", err)
	}
	if len(h.Hits) != 1 || h.Hits[0].Total != 554272 {
		t.Errorf("unexpected: %+v", h)
	}
}

func TestPing_SendsMinimalQuery(t *testing.T) {
	var called int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		_ = r.ParseForm()
		if r.Form.Get("query") == "" {
			t.Errorf("пустой query")
		}
		if r.Form.Get("limit") != "1" {
			t.Errorf("limit не 1: %q", r.Form.Get("limit"))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "{}")
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if called != 1 {
		t.Errorf("вызовов: %d", called)
	}
}

func TestPing_PropagatesBasicAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "{}")
	}))
	defer srv.Close()

	hc := httpclient.New(httpclient.Config{MaxRetries: 0}, nil)
	c, err := New(Config{BaseURL: srv.URL, Username: "u", Password: "p"}, hc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(gotAuth, "Basic ") {
		t.Errorf("Authorization: %q", gotAuth)
	}
}

// TestStreamQuery_BigLine — проверка, что bufio.Scanner буфер достаточно
// большой для сверхбольших _msg (до 10 МБ).
func TestStreamQuery_BigLine(t *testing.T) {
	big := strings.Repeat("A", 200_000)
	payload := fmt.Sprintf(`{"_time":"2026-04-23T00:00:00Z","_stream":"{}","_stream_id":"x","_msg":%q,"host":"t5","level":"error"}`+"\n", big)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	q := Query{
		Stream: StreamFilter{Host: "t5", Levels: []string{"error"}},
		From:   time.Now().Add(-time.Hour),
		To:     time.Now(),
	}
	var got int
	err := c.StreamQuery(context.Background(), q, func(e LogEntry) error {
		if len(e.Msg) != len(big) {
			t.Errorf("Msg len: got=%d, want=%d", len(e.Msg), len(big))
		}
		got++
		return nil
	})
	if err != nil {
		t.Fatalf("StreamQuery: %v", err)
	}
	if got != 1 {
		t.Errorf("got=%d want=1", got)
	}
}
