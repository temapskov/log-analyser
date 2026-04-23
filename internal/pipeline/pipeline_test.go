package pipeline

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QCoreTech/log_analyser/internal/collector"
	"github.com/QCoreTech/log_analyser/internal/grafana"
	"github.com/QCoreTech/log_analyser/internal/httpclient"
	"github.com/QCoreTech/log_analyser/internal/render"
	"github.com/QCoreTech/log_analyser/internal/telegram"
)

// makeVLServer — мок VL, возвращает 2 записи для `t5`, 0 для остальных.
func makeVLServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		q := r.Form.Get("query")
		switch {
		case strings.Contains(q, `host="t5"`):
			_, _ = io.WriteString(w, strings.TrimSpace(`
{"_time":"2026-04-23T06:35:19Z","_stream_id":"s1","_stream":"{host=\"t5\"}","_msg":"order uid: AAAA error","host":"t5","app":"agent","level":"error"}
{"_time":"2026-04-23T06:40:12Z","_stream_id":"s2","_stream":"{host=\"t5\"}","_msg":"order uid: BBBB critical","host":"t5","app":"agent","level":"critical"}
`))
		default:
			// пустой ответ = 0 записей
			w.WriteHeader(http.StatusOK)
		}
	}))
}

type tgCalls struct {
	mu         sync.Mutex
	cover      url.Values
	mediaJSON  string
	mediaFiles map[string]string
	sendDocs   []string
	coverReply *map[string]any
	mediaReply *map[string]any
	docReply   *map[string]any
	failMedia  bool
	failDoc    bool
}

func makeTGServer(t *testing.T, calls *tgCalls) *httptest.Server {
	t.Helper()
	calls.mediaFiles = map[string]string{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.mu.Lock()
		defer calls.mu.Unlock()
		path := r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(path, "/sendMessage"):
			_ = r.ParseForm()
			calls.cover = r.Form
			rep := map[string]any{"ok": true, "result": map[string]any{"message_id": 100}}
			if calls.coverReply != nil {
				rep = *calls.coverReply
			}
			_ = json.NewEncoder(w).Encode(rep)
		case strings.HasSuffix(path, "/sendMediaGroup"):
			if calls.failMedia {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"ok": false, "error_code": 400, "description": "media group broke",
				})
				return
			}
			_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			mr := multipart.NewReader(r.Body, params["boundary"])
			for {
				part, err := mr.NextPart()
				if err != nil {
					break
				}
				b, _ := io.ReadAll(part)
				if part.FormName() == "media" {
					calls.mediaJSON = string(b)
				} else {
					calls.mediaFiles[part.FileName()] = string(b)
				}
			}
			result := []map[string]any{}
			for i := 0; i < 5; i++ {
				result = append(result, map[string]any{"message_id": 200 + i})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
		case strings.HasSuffix(path, "/sendDocument"):
			if calls.failDoc {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"ok": false, "error_code": 400, "description": "doc broke",
				})
				return
			}
			_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			mr := multipart.NewReader(r.Body, params["boundary"])
			for {
				part, err := mr.NextPart()
				if err != nil {
					break
				}
				if part.FormName() == "document" {
					calls.sendDocs = append(calls.sendDocs, part.FileName())
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 300}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func newPipeline(t *testing.T, vlURL, tgURL string, cfgMut func(*Config)) *Pipeline {
	t.Helper()
	hc := httpclient.New(httpclient.Config{Timeout: 5 * time.Second}, nil)
	vl, err := collector.New(collector.Config{BaseURL: vlURL}, hc, nil)
	if err != nil {
		t.Fatal(err)
	}
	tg, err := telegram.New(telegram.Config{Token: "tok", APIBase: tgURL}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rdr, err := render.New("Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("Europe/Moscow")
	cfg := Config{
		Hosts:       []string{"t1", "t5"},
		Levels:      []string{"error", "critical"},
		NoiseK:      1,
		TopN:        10,
		MaxExamples: 3,
		ReportsDir:  t.TempDir(),
		ReportExt:   "md",
		TZ:          loc,
		ChatID:      -100,
	}
	if cfgMut != nil {
		cfgMut(&cfg)
	}
	p, err := New(cfg, Dependencies{
		VL: vl, TG: tg, Renderer: rdr,
		Grafana: grafana.Config{BaseURL: "http://g", OrgID: 1, DSUID: "u", DSType: "vl"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRun_HappyPath(t *testing.T) {
	vl := makeVLServer(t)
	defer vl.Close()
	calls := &tgCalls{}
	tg := makeTGServer(t, calls)
	defer tg.Close()
	p := newPipeline(t, vl.URL, tg.URL, nil)

	now := time.Now()
	res, err := p.Run(context.Background(), Window{From: now.Add(-time.Hour), To: now})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.CoverMsgID != 100 {
		t.Errorf("cover msg id: %d", res.CoverMsgID)
	}
	if len(res.MediaMsgIDs) != 5 {
		// sendMediaGroup ответил 5 сообщениями — даже хотя файлов 2, это ок для теста.
		t.Errorf("media msg ids: %v", res.MediaMsgIDs)
	}
	// 2 файла созданы (t1 и t5).
	for _, host := range []string{"t1", "t5"} {
		h := res.PerHost[host]
		if h.FilePath == "" {
			t.Errorf("host=%s file missing", host)
		}
		if _, err := os.Stat(h.FilePath); err != nil {
			t.Errorf("host=%s stat: %v", host, err)
		}
	}
	if res.PerHost["t5"].TotalRecords != 2 {
		t.Errorf("t5 records: %d", res.PerHost["t5"].TotalRecords)
	}
	// Cover содержит hosts.
	if !strings.Contains(calls.cover.Get("text"), "t5") {
		t.Errorf("cover текст без t5: %q", calls.cover.Get("text"))
	}
}

func TestRun_FallsBackToSendDocumentOnMediaFail(t *testing.T) {
	vl := makeVLServer(t)
	defer vl.Close()
	calls := &tgCalls{failMedia: true}
	tg := makeTGServer(t, calls)
	defer tg.Close()
	p := newPipeline(t, vl.URL, tg.URL, nil)

	now := time.Now()
	_, err := p.Run(context.Background(), Window{From: now.Add(-time.Hour), To: now})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(calls.sendDocs) != 2 {
		t.Errorf("ожидали 2 sendDocument fallback, got %d (%v)", len(calls.sendDocs), calls.sendDocs)
	}
}

func TestRun_ZeroRecordHostStillRendered(t *testing.T) {
	vl := makeVLServer(t)
	defer vl.Close()
	calls := &tgCalls{}
	tg := makeTGServer(t, calls)
	defer tg.Close()
	p := newPipeline(t, vl.URL, tg.URL, nil)

	now := time.Now()
	res, err := p.Run(context.Background(), Window{From: now.Add(-time.Hour), To: now})
	if err != nil {
		t.Fatal(err)
	}
	t1 := res.PerHost["t1"]
	if t1.FilePath == "" {
		t.Fatal("t1 файл должен быть создан (FR-5: для пустого хоста тоже)")
	}
	b, err := os.ReadFile(t1.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "не обнаружено") {
		t.Errorf("t1 файл без notice: %s", string(b))
	}
}

func TestRun_PartialHostFailureStillDeliversOthers(t *testing.T) {
	vlFail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		q := r.Form.Get("query")
		if strings.Contains(q, `host="t1"`) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// t5 отрабатывает нормально
		_, _ = io.WriteString(w, strings.TrimSpace(`{"_time":"2026-04-23T06:35:19Z","_stream_id":"s","_stream":"{}","_msg":"x","host":"t5","app":"a","level":"error"}`))
	}))
	defer vlFail.Close()
	calls := &tgCalls{}
	tg := makeTGServer(t, calls)
	defer tg.Close()

	p := newPipeline(t, vlFail.URL, tg.URL, nil)
	now := time.Now()
	res, err := p.Run(context.Background(), Window{From: now.Add(-time.Hour), To: now})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Errors) == 0 {
		t.Fatal("ожидали partial error для t1")
	}
	if res.PerHost["t5"].FilePath == "" {
		t.Fatal("t5 должен быть доставлен, несмотря на фейл t1")
	}
	// Cover должен содержать partial-маркер.
	if !strings.Contains(calls.cover.Get("text"), "[partial-report]") {
		t.Errorf("partial маркер отсутствует в cover: %q", calls.cover.Get("text"))
	}
}

func TestRun_WindowValidation(t *testing.T) {
	vl := makeVLServer(t)
	defer vl.Close()
	tg := makeTGServer(t, &tgCalls{})
	defer tg.Close()
	p := newPipeline(t, vl.URL, tg.URL, nil)

	now := time.Now()
	_, err := p.Run(context.Background(), Window{From: now, To: now.Add(-time.Hour)})
	if err == nil {
		t.Fatal("ожидали ошибку на обратное окно")
	}
}

func TestRun_FilenameFormat(t *testing.T) {
	vl := makeVLServer(t)
	defer vl.Close()
	calls := &tgCalls{}
	tg := makeTGServer(t, calls)
	defer tg.Close()
	p := newPipeline(t, vl.URL, tg.URL, func(c *Config) {
		c.Hosts = []string{"t5"}
		// Один хост → sendMediaGroup невозможен (2..10), fallback на sendDocument.
	})
	tzLoc, _ := time.LoadLocation("Europe/Moscow")
	to := time.Date(2026, 4, 23, 8, 0, 0, 0, tzLoc)
	from := to.Add(-24 * time.Hour)
	res, err := p.Run(context.Background(), Window{From: from, To: to})
	if err != nil {
		t.Fatal(err)
	}
	h := res.PerHost["t5"]
	expected := "t5_2026-04-23.md"
	if filepath.Base(h.FilePath) != expected {
		t.Errorf("filename: %q want %q", filepath.Base(h.FilePath), expected)
	}
	// Один файл → sendDocument (не media group).
	if len(calls.sendDocs) != 1 {
		t.Errorf("sendDocs: %d (%v)", len(calls.sendDocs), calls.sendDocs)
	}
}
