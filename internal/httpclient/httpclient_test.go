package httpclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, cfg Config) *Client {
	t.Helper()
	c := New(cfg, nil)
	// Быстрый backoff для unit-тестов.
	c.cfg.RetryBaseDelay = time.Millisecond
	c.cfg.MaxRetryDelay = 5 * time.Millisecond
	return c
}

func TestDo_SuccessFirstTry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "hello")
	}))
	defer srv.Close()

	c := newTestClient(t, Config{MaxRetries: 2})
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := c.Do(context.Background(), req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer DrainAndClose(resp.Body)
	if resp.StatusCode != 200 {
		t.Errorf("status %d", resp.StatusCode)
	}
}

func TestDo_RetriesOn5xx(t *testing.T) {
	var count int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&count, 1)
		if n < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(t, Config{MaxRetries: 3})
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := c.Do(context.Background(), req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer DrainAndClose(resp.Body)
	if resp.StatusCode != 200 {
		t.Errorf("ожидали 200 после retry, got %d", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&count); got != 3 {
		t.Errorf("попыток: got=%d, want=3", got)
	}
}

func TestDo_ExhaustsRetries(t *testing.T) {
	var count int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&count, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newTestClient(t, Config{MaxRetries: 2})
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	_, err := c.Do(context.Background(), req)
	if err == nil {
		t.Fatal("ожидали ошибку после исчерпания retry")
	}
	if !strings.Contains(err.Error(), "после 3 попыток") {
		t.Errorf("сообщение не указывает количество попыток: %v", err)
	}
	if got := atomic.LoadInt32(&count); got != 3 {
		t.Errorf("попыток: got=%d, want=3", got)
	}
}

func TestDo_NoRetryOn4xx(t *testing.T) {
	var count int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&count, 1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := newTestClient(t, Config{MaxRetries: 5})
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := c.Do(context.Background(), req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer DrainAndClose(resp.Body)
	if resp.StatusCode != 401 {
		t.Errorf("status=%d", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&count); got != 1 {
		t.Errorf("4xx не должен ретраиться, got=%d попыток", got)
	}
}

func TestDo_ContextCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(t, Config{MaxRetries: 3, Timeout: 500 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	_, err := c.Do(ctx, req)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Errorf("ожидали context.Canceled, got %v", err)
	}
}

func TestMaskErr(t *testing.T) {
	c := New(Config{SensitiveValues: []string{"topsecret", ""}}, nil)
	err := errors.New("connection to https://x?token=topsecret failed")
	masked := c.maskErr(err)
	if strings.Contains(masked.Error(), "topsecret") {
		t.Fatalf("секрет просочился: %v", masked)
	}
	if !strings.Contains(masked.Error(), "***") {
		t.Fatalf("маркер ***: %v", masked)
	}
}

func TestDo_POSTWithoutGetBody_NoRetry(t *testing.T) {
	var count int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&count, 1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	c := newTestClient(t, Config{MaxRetries: 3})
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader("body"))
	// Отменяем авто-установленный GetBody (Go проставляет его для strings.Reader),
	// чтобы сэмулировать «тело, которое нельзя повторно проиграть».
	req.GetBody = nil
	resp, err := c.Do(context.Background(), req)
	// Ожидаем ответ как есть — ретраев нет, ошибка отсутствует.
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer DrainAndClose(resp.Body)
	if got := atomic.LoadInt32(&count); got != 1 {
		t.Errorf("POST без GetBody не должен ретраиться, got=%d попыток", got)
	}
}

func TestDo_POSTWithGetBody_Retries(t *testing.T) {
	var count int32
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&count, 1)
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if n < 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(t, Config{MaxRetries: 3})
	body := "replayable"
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(body)), nil
	}
	resp, err := c.Do(context.Background(), req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer DrainAndClose(resp.Body)
	if got := atomic.LoadInt32(&count); got != 2 {
		t.Errorf("ожидали 2 попытки, got=%d", got)
	}
	for i, b := range bodies {
		if b != body {
			t.Errorf("body[%d]=%q, want=%q", i, b, body)
		}
	}
}

func TestBackoffDelay_IsBounded(t *testing.T) {
	c := New(Config{
		RetryBaseDelay: 100 * time.Millisecond,
		MaxRetryDelay:  200 * time.Millisecond,
	}, nil)
	for attempt := 1; attempt <= 20; attempt++ {
		d := c.backoffDelay(attempt)
		if d > c.cfg.MaxRetryDelay+c.cfg.MaxRetryDelay/4+time.Millisecond {
			t.Errorf("attempt %d: delay %v > ceiling", attempt, d)
		}
		if d <= 0 {
			t.Errorf("attempt %d: non-positive delay %v", attempt, d)
		}
	}
}
