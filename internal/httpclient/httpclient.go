// Package httpclient — общая обёртка над http.Client для всех исходящих
// запросов daemon'а (VictoriaLogs, Telegram, Grafana API).
//
// Принципы (ADR-0007):
//   - Proxy берём из окружения (http.ProxyFromEnvironment), уважаем NO_PROXY.
//     Это важно: VL/Grafana — внутри корпсети и ходить через корп-прокси
//     к ним нельзя (см. feat/grafana-deeplink fix(proxy)).
//   - Retry только идемпотентных (GET) запросов на 5xx / network errors.
//     POST не ретраится, если не объявлен req.GetBody (replay).
//   - Маскирование: error messages не должны содержать значения из
//     Config.SensitiveValues (NFR-S2).
//   - Тайминг: единый timeout на запрос-попытку (не на весь retry-цикл);
//     retry-цикл отменяется через context.
package httpclient

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"strings"
	"time"
)

type Config struct {
	// Timeout — max длительность одной попытки (включая чтение тела).
	Timeout time.Duration
	// MaxRetries — сколько дополнительных попыток после первого фейла.
	// 0 — ретраев нет, запрос отправляется 1 раз.
	MaxRetries int
	// RetryBaseDelay — базовая задержка для экспоненциального backoff.
	// Фактические задержки: base, 2*base, 4*base, … + jitter ±25%.
	RetryBaseDelay time.Duration
	// MaxRetryDelay — верхняя граница задержки после jitter.
	MaxRetryDelay time.Duration
	// SensitiveValues — строки, которые нужно заменить на "***" при
	// форматировании ошибок/логов (токены, пароли).
	SensitiveValues []string
}

func (c Config) withDefaults() Config {
	if c.Timeout <= 0 {
		c.Timeout = 30 * time.Second
	}
	if c.MaxRetries < 0 {
		c.MaxRetries = 0
	}
	if c.RetryBaseDelay <= 0 {
		c.RetryBaseDelay = 200 * time.Millisecond
	}
	if c.MaxRetryDelay <= 0 {
		c.MaxRetryDelay = 10 * time.Second
	}
	return c
}

type Client struct {
	cfg    Config
	http   *http.Client
	logger *slog.Logger
}

// New создаёт клиент с заданным конфигом. logger может быть nil — тогда
// используется slog.Default().
func New(cfg Config, logger *slog.Logger) *Client {
	cfg = cfg.withDefaults()
	if logger == nil {
		logger = slog.Default()
	}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
	}
	return &Client{
		cfg: cfg,
		http: &http.Client{
			Transport: tr,
			Timeout:   cfg.Timeout,
		},
		logger: logger,
	}
}

// HTTPDoer позволяет подменить http.Client в тестах.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// HTTP — возвращает underlying http.Client (нужно, напр., для стрим-чтения,
// где своя логика закрытия Body).
func (c *Client) HTTP() *http.Client { return c.http }

// Do — идемпотентный GET/HEAD с retry. Для POST/PUT — только если у запроса
// есть GetBody (replay-safe). Тело ответа возвращается нечитанным — caller
// обязан закрыть. Retry для HTTP 5xx и network errors, НЕ для 4xx.
//
// Поведение при успешном коде ≥ 400: ответ возвращается как есть (caller
// сам разбирает), но retry не производится.
func (c *Client) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	if req.Body != nil && req.GetBody == nil && !isIdempotent(req.Method) {
		// Без GetBody мы не можем повторно проиграть тело.
		resp, err := c.http.Do(req.WithContext(ctx))
		if err != nil {
			return nil, c.maskErr(err)
		}
		return resp, nil
	}

	var lastErr error
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := c.backoffDelay(attempt)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
			if err := resetBody(req); err != nil {
				return nil, c.maskErr(fmt.Errorf("replay body: %w", err))
			}
		}

		start := time.Now()
		resp, err := c.http.Do(req.WithContext(ctx))
		dur := time.Since(start)

		switch {
		case err == nil && resp.StatusCode < 500:
			// Успех или клиентская ошибка — не ретраим.
			return resp, nil
		case err == nil:
			// 5xx — ретраим; закрываем тело.
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("server error %d", resp.StatusCode)
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return nil, err
		case isRetriableNetErr(err):
			lastErr = err
		default:
			return nil, c.maskErr(err)
		}

		c.logger.Debug("http retry",
			slog.Int("attempt", attempt+1),
			slog.Int("max", c.cfg.MaxRetries+1),
			slog.Duration("duration", dur),
			slog.String("err", c.maskStr(lastErr.Error())),
		)
	}
	return nil, c.maskErr(fmt.Errorf("после %d попыток: %w", c.cfg.MaxRetries+1, lastErr))
}

func isIdempotent(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete:
		return true
	default:
		return false
	}
}

func resetBody(req *http.Request) error {
	if req.GetBody == nil {
		return nil
	}
	body, err := req.GetBody()
	if err != nil {
		return err
	}
	req.Body = body
	return nil
}

func isRetriableNetErr(err error) bool {
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return true
	}
	// Строковый матчинг как fallback — некоторые ошибки не реализуют net.Error.
	s := err.Error()
	for _, frag := range []string{"connection reset", "connection refused", "EOF", "broken pipe", "no route to host"} {
		if strings.Contains(s, frag) {
			return true
		}
	}
	return false
}

func (c *Client) backoffDelay(attempt int) time.Duration {
	// base * 2^(attempt-1) + jitter ±25%.
	d := c.cfg.RetryBaseDelay << (attempt - 1)
	if d > c.cfg.MaxRetryDelay || d <= 0 {
		d = c.cfg.MaxRetryDelay
	}
	jitter := int64(d / 4)
	if jitter > 0 {
		if n, err := rand.Int(rand.Reader, big.NewInt(2*jitter)); err == nil {
			d = d - time.Duration(jitter) + time.Duration(n.Int64())
		}
	}
	return d
}

// maskErr — возвращает ошибку, в строке которой значения из SensitiveValues
// заменены на "***".
func (c *Client) maskErr(err error) error {
	if err == nil {
		return nil
	}
	s := c.maskStr(err.Error())
	if s == err.Error() {
		return err
	}
	return errors.New(s)
}

func (c *Client) maskStr(s string) string {
	for _, v := range c.cfg.SensitiveValues {
		if v == "" {
			continue
		}
		s = strings.ReplaceAll(s, v, "***")
	}
	return s
}

// DrainAndClose — удобный хелпер: выгрести и закрыть ResponseBody. Вызывать
// defer'ом у caller'а после чтения.
func DrainAndClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, body)
	_ = body.Close()
}
