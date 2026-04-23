package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testToken = "123:abc"

func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	cfg := Config{Token: testToken, APIBase: srv.URL, MaxRetries: 2, MaxRetryAfter: 500 * time.Millisecond}
	c, err := New(cfg, srv.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func jsonWrite(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func TestNew_RejectsEmptyToken(t *testing.T) {
	if _, err := New(Config{Token: ""}, nil, nil); err == nil {
		t.Fatal("ожидали ошибку на пустой токен")
	}
}

func TestGetMe_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/bot"+testToken+"/getMe") {
			t.Errorf("path=%s", r.URL.Path)
		}
		jsonWrite(w, map[string]any{"ok": true, "result": map[string]any{"id": 42, "is_bot": true, "username": "log_bot"}})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	u, err := c.GetMe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != 42 || !u.IsBot || u.Username != "log_bot" {
		t.Errorf("user: %+v", u)
	}
}

func TestSendMessage_RespectsRetryAfter(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			jsonWrite(w, map[string]any{
				"ok":          false,
				"error_code":  429,
				"description": "Too Many Requests: retry after 0",
				"parameters":  map[string]any{"retry_after": 0},
			})
			return
		}
		jsonWrite(w, map[string]any{"ok": true, "result": map[string]any{"message_id": 1001}})
	}))
	defer srv.Close()
	// Даже retry_after=0 — всё равно retry-цикл должен сработать.
	c := newTestClient(t, srv)

	msg, err := c.SendMessage(context.Background(), SendMessageParams{ChatID: -100, Text: "hi"})
	// retry_after=0 ≤ MaxRetryAfter, но наше условие требует > 0 для retry.
	// Значит ожидаем APIError 429.
	if err == nil {
		t.Fatalf("expected 429 error, got msg: %+v", msg)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != 429 {
		t.Errorf("ожидали 429, got: %v", err)
	}
}

func TestSendMessage_RetriesOn429WithPositiveRetryAfter(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			jsonWrite(w, map[string]any{
				"ok":          false,
				"error_code":  429,
				"description": "Too Many Requests: retry after 0",
				"parameters":  map[string]any{"retry_after": 1},
			})
			return
		}
		jsonWrite(w, map[string]any{"ok": true, "result": map[string]any{"message_id": 7}})
	}))
	defer srv.Close()
	cfg := Config{Token: testToken, APIBase: srv.URL, MaxRetries: 2, MaxRetryAfter: 2 * time.Second}
	c, err := New(cfg, srv.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}

	msg, err := c.SendMessage(context.Background(), SendMessageParams{ChatID: -100, Text: "hi"})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if msg.MessageID != 7 {
		t.Errorf("msg id: %d", msg.MessageID)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("calls: %d", calls)
	}
}

func TestSendMessage_EmptyParams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("не должны вызвать сервер")
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	if _, err := c.SendMessage(context.Background(), SendMessageParams{Text: "hi"}); err == nil {
		t.Fatal("ожидали ошибку без ChatID")
	}
	if _, err := c.SendMessage(context.Background(), SendMessageParams{ChatID: 1}); err == nil {
		t.Fatal("ожидали ошибку без Text")
	}
}

func TestSendDocument_Multipart(t *testing.T) {
	var gotFilename, gotCaption, gotContent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ct := r.Header.Get("Content-Type")
		_, params, err := mime.ParseMediaType(ct)
		if err != nil {
			t.Fatalf("content-type: %v", err)
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			switch part.FormName() {
			case "document":
				gotFilename = part.FileName()
				b, _ := io.ReadAll(part)
				gotContent = string(b)
			case "caption":
				b, _ := io.ReadAll(part)
				gotCaption = string(b)
			}
		}
		jsonWrite(w, map[string]any{"ok": true, "result": map[string]any{"message_id": 5}})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	msg, err := c.SendDocument(context.Background(), SendDocumentParams{
		ChatID: -100,
		Doc: Attachment{
			Filename: "t5_2026-04-23.md",
			Content:  strings.NewReader("# отчёт"),
			Caption:  "ежедневный отчёт",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.MessageID != 5 {
		t.Errorf("msg id: %d", msg.MessageID)
	}
	if gotFilename != "t5_2026-04-23.md" {
		t.Errorf("filename: %q", gotFilename)
	}
	if gotContent != "# отчёт" {
		t.Errorf("content: %q", gotContent)
	}
	if gotCaption != "ежедневный отчёт" {
		t.Errorf("caption: %q", gotCaption)
	}
}

func TestSendMediaGroup_FiveDocs(t *testing.T) {
	var mediaJSON string
	fileBodies := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Fatal(err)
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(part)
			if part.FormName() == "media" {
				mediaJSON = string(b)
			} else {
				fileBodies[part.FormName()] = string(b)
			}
		}
		jsonWrite(w, map[string]any{"ok": true, "result": []map[string]any{
			{"message_id": 1}, {"message_id": 2}, {"message_id": 3}, {"message_id": 4}, {"message_id": 5},
		}})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	docs := []Attachment{
		{Filename: "t1.md", Content: strings.NewReader("A")},
		{Filename: "ali-t1.md", Content: strings.NewReader("B")},
		{Filename: "t2.md", Content: strings.NewReader("C")},
		{Filename: "aws-t3.md", Content: strings.NewReader("D")},
		{Filename: "t5.md", Content: strings.NewReader("E"), Caption: "caption5"},
	}
	msgs, err := c.SendMediaGroup(context.Background(), SendMediaGroupParams{ChatID: -100, Documents: docs})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 5 {
		t.Fatalf("msgs: %d", len(msgs))
	}
	// media — корректный JSON-массив с 5 элементами, все attach://fN.
	var arr []mediaDoc
	if err := json.Unmarshal([]byte(mediaJSON), &arr); err != nil {
		t.Fatal(err)
	}
	if len(arr) != 5 {
		t.Fatalf("media len: %d (%s)", len(arr), mediaJSON)
	}
	for i, m := range arr {
		if m.Type != "document" {
			t.Errorf("type: %q", m.Type)
		}
		want := "attach://f" + string(rune('0'+i))
		if m.Media != want {
			t.Errorf("media[%d]=%q want %q", i, m.Media, want)
		}
	}
	if arr[4].Caption != "caption5" {
		t.Errorf("caption5: %q", arr[4].Caption)
	}
	// Все файлы на месте.
	expected := map[string]string{"f0": "A", "f1": "B", "f2": "C", "f3": "D", "f4": "E"}
	for k, v := range expected {
		if fileBodies[k] != v {
			t.Errorf("file %s: got %q want %q", k, fileBodies[k], v)
		}
	}
}

func TestSendMediaGroup_Validation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("не должно вызвать сервер")
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	// 1 документ — мало
	_, err := c.SendMediaGroup(context.Background(), SendMediaGroupParams{
		ChatID:    -1,
		Documents: []Attachment{{Filename: "a", Content: strings.NewReader("x")}},
	})
	if err == nil || !strings.Contains(err.Error(), "2..10") {
		t.Errorf("ожидали 2..10 error: %v", err)
	}
	// 11 документов — много
	docs := make([]Attachment, 11)
	for i := range docs {
		docs[i] = Attachment{Filename: "a", Content: strings.NewReader("x")}
	}
	_, err = c.SendMediaGroup(context.Background(), SendMediaGroupParams{ChatID: -1, Documents: docs})
	if err == nil || !strings.Contains(err.Error(), "2..10") {
		t.Errorf("ожидали 2..10 error: %v", err)
	}
	// chat_id=0
	_, err = c.SendMediaGroup(context.Background(), SendMediaGroupParams{Documents: []Attachment{
		{Filename: "a", Content: strings.NewReader("x")},
		{Filename: "b", Content: strings.NewReader("y")},
	}})
	if err == nil {
		t.Error("ожидали ошибку без chat_id")
	}
}

func TestAPIError_FormatsRetryAfter(t *testing.T) {
	e := &APIError{Code: 429, Description: "Too Many Requests", RetryAfter: 30 * time.Second}
	if !strings.Contains(e.Error(), "retry after 30s") {
		t.Errorf("%v", e)
	}
}

func TestScrubErr_TokenNotInMessage(t *testing.T) {
	c := &Client{cfg: Config{Token: "topsecret"}}
	e := errors.New("connect to https://api.telegram.org/bottopsecret/getMe failed: connection refused")
	got := c.scrubErr(e)
	if strings.Contains(got.Error(), "topsecret") {
		t.Fatalf("токен просочился: %v", got)
	}
	if !strings.Contains(got.Error(), "***") {
		t.Fatalf("ожидали ***: %v", got)
	}
}

func TestSendMessage_SendsExpectedForm(t *testing.T) {
	var gotForm string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		gotForm = r.Form.Encode()
		jsonWrite(w, map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	_, err := c.SendMessage(context.Background(), SendMessageParams{
		ChatID: -100, Text: "<b>hi</b>", ParseMode: "HTML", DisableWebPagePreview: true, MessageThreadID: 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"chat_id=-100", "text=", "parse_mode=HTML", "disable_web_page_preview=true", "message_thread_id=42"} {
		if !strings.Contains(gotForm, want) {
			t.Errorf("form=%q missing %q", gotForm, want)
		}
	}
}

func TestDoJSON_ContextCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		jsonWrite(w, map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.SendMessage(ctx, SendMessageParams{ChatID: -1, Text: "x"})
	if err == nil {
		t.Fatal("ожидали context.Canceled")
	}
}
