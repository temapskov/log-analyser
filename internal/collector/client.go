package collector

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QCoreTech/log_analyser/internal/httpclient"
)

// Config — параметры VL-клиента.
type Config struct {
	BaseURL  string
	Username string // optional, basic auth
	Password string // optional
	// QueryTimeout — таймаут на стрим-запрос query (длинный, т.к. может
	// тянуть миллионы строк). 0 → без таймаута (управляется context'ом).
	QueryTimeout time.Duration
}

func (c Config) validate() error {
	if strings.TrimSpace(c.BaseURL) == "" {
		return errors.New("BaseURL пуст")
	}
	if !strings.HasPrefix(c.BaseURL, "http://") && !strings.HasPrefix(c.BaseURL, "https://") {
		return fmt.Errorf("BaseURL=%q: требуется схема http:// или https://", c.BaseURL)
	}
	return nil
}

// Client — HTTP-клиент VictoriaLogs.
type Client struct {
	cfg    Config
	http   *httpclient.Client
	raw    *http.Client // для стрим-чтения (без retry)
	logger *slog.Logger
}

// New создаёт клиента. raw — сырой http.Client для стрим-ответов; если nil,
// используется http.Client из httpclient.Client.
func New(cfg Config, hc *httpclient.Client, logger *slog.Logger) (*Client, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if hc == nil {
		return nil, errors.New("httpclient.Client не задан")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		cfg:    cfg,
		http:   hc,
		raw:    hc.HTTP(),
		logger: logger,
	}, nil
}

// setAuth добавляет Authorization header, если задан basic auth.
func (c *Client) setAuth(req *http.Request) {
	if c.cfg.Username != "" || c.cfg.Password != "" {
		req.SetBasicAuth(c.cfg.Username, c.cfg.Password)
	}
}

// endpoint строит URL <BaseURL>/select/logsql/<path>.
func (c *Client) endpoint(path string) string {
	base := strings.TrimRight(c.cfg.BaseURL, "/")
	return base + "/select/logsql/" + strings.TrimLeft(path, "/")
}

// StreamQuery выполняет /select/logsql/query и передаёт каждую строку-запись
// в callback. Callback получает возможность прервать стрим (возврат io.EOF
// считается штатным завершением).
//
// Метод НЕ ретраится: повторно запустить стрим в середине ответа нельзя.
// На connection errors клиент вернёт ошибку, caller сам решит, перезапускать
// ли весь запрос.
func (c *Client) StreamQuery(ctx context.Context, q Query, onEntry func(LogEntry) error) error {
	queryStr, err := q.Build()
	if err != nil {
		return fmt.Errorf("build LogsQL: %w", err)
	}
	if c.cfg.QueryTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.cfg.QueryTimeout)
		defer cancel()
	}

	form := url.Values{}
	form.Set("query", queryStr)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("query"), strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.setAuth(req)

	resp, err := c.raw.Do(req)
	if err != nil {
		return fmt.Errorf("VL query: %w", err)
	}
	defer httpclient.DrainAndClose(resp.Body)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("VL query status=%d body=%q", resp.StatusCode, string(body))
	}

	scanner := bufio.NewScanner(resp.Body)
	// VL-записи могут быть большими (один _msg бывает ~100КБ). Отодвинем
	// лимит bufio до 10 МБ — defensively.
	scanner.Buffer(make([]byte, 0, 64<<10), 10<<20)

	var count int
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry LogEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			c.logger.Warn("VL: битая JSON-строка, скип",
				slog.Int("bytes", len(line)),
				slog.String("err", err.Error()),
			)
			continue
		}
		count++
		if err := onEntry(entry); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("callback прервал стрим: %w", err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("чтение стрима: %w", err)
	}
	c.logger.Debug("VL query done", slog.Int("entries", count))
	return nil
}

// Hits выполняет /select/logsql/hits — агрегированные счётчики попаданий.
// Не стрим; всё тело вычитывается в память.
func (c *Client) Hits(ctx context.Context, q Query) (*HitsResponse, error) {
	queryStr, err := q.Build()
	if err != nil {
		return nil, fmt.Errorf("build LogsQL: %w", err)
	}

	form := url.Values{}
	form.Set("query", queryStr)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("hits"), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.setAuth(req)

	resp, err := c.http.Do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("VL hits: %w", err)
	}
	defer httpclient.DrainAndClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("VL hits status=%d body=%q", resp.StatusCode, string(b))
	}
	var out HitsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode hits: %w", err)
	}
	return &out, nil
}

// Ping — минимальный запрос для healthcheck. Использует /select/logsql/query
// с тривиальным фильтром и limit=1, чтобы быстро проверить, что VL отвечает
// и возвращает корректный Content-Type.
func (c *Client) Ping(ctx context.Context) error {
	// 1-часовое окно, limit=1 — дешёвый probe.
	form := url.Values{}
	form.Set("query", "_time:1h")
	form.Set("limit", "1")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("query"), strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.setAuth(req)

	resp, err := c.http.Do(ctx, req)
	if err != nil {
		return err
	}
	defer httpclient.DrainAndClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ping status=%d", resp.StatusCode)
	}
	return nil
}
