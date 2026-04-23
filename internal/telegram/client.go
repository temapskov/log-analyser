// Package telegram — тонкий клиент Telegram Bot API под нужды daemon'а
// (ADR-0008). Поддерживает ровно четыре метода: getMe (healthcheck),
// sendMessage (cover, HTML parse_mode), sendDocument (fallback для одиночных
// файлов), sendMediaGroup (основной путь доставки 5 attachments альбомом).
//
// Принципы:
//   - Прокси через httpclient.ProxyFromEnvironment (NO_PROXY уважается).
//   - 429 Too Many Requests — respect parameters.retry_after (ждём, retry).
//   - 5xx и network errors в send*Group — НЕ ретраим (файлы уже могли
//     частично уйти, дубль хуже временного фейла). Caller разбирается
//     через state (ADR-0002) в следующих PR'ах.
//   - Секреты: TG_BOT_TOKEN никогда не должен попасть в логи/error.
//     Клиент держит его в struct и передаёт только в URL path.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Config — параметры клиента.
type Config struct {
	// Token — значение TG_BOT_TOKEN (без префикса "bot").
	Token string
	// APIBase — обычно https://api.telegram.org. Для self-hosted Bot API
	// Server можно переопределить.
	APIBase string
	// HTTPTimeout — таймаут одного HTTP-запроса.
	HTTPTimeout time.Duration
	// MaxRetryAfter — верхняя граница ожидания 429 retry_after. Если
	// TG просит подождать больше — возвращаем ошибку.
	MaxRetryAfter time.Duration
	// MaxRetries — сколько дополнительных попыток при 429 (0 — без retry).
	MaxRetries int
}

func (c Config) withDefaults() Config {
	if c.APIBase == "" {
		c.APIBase = "https://api.telegram.org"
	}
	if c.HTTPTimeout <= 0 {
		c.HTTPTimeout = 30 * time.Second
	}
	if c.MaxRetryAfter <= 0 {
		c.MaxRetryAfter = 60 * time.Second
	}
	if c.MaxRetries < 0 {
		c.MaxRetries = 0
	}
	return c
}

type Client struct {
	cfg    Config
	http   *http.Client
	logger *slog.Logger
}

// New создаёт клиент. http может быть nil — будет использован стандартный
// с proxy из env и заданным HTTPTimeout.
func New(cfg Config, httpClient *http.Client, logger *slog.Logger) (*Client, error) {
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("Token пуст")
	}
	cfg = cfg.withDefaults()
	if logger == nil {
		logger = slog.Default()
	}
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: cfg.HTTPTimeout,
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
			},
		}
	}
	return &Client{cfg: cfg, http: httpClient, logger: logger}, nil
}

// apiResp — общий формат ответа Bot API.
//
//	{"ok":true, "result":...}
//	{"ok":false, "error_code":N, "description":"...", "parameters":{"retry_after":60}}
type apiResp struct {
	OK          bool            `json:"ok"`
	ErrorCode   int             `json:"error_code,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  respParameters  `json:"parameters,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
}

type respParameters struct {
	RetryAfter  int   `json:"retry_after,omitempty"`
	MigrateToID int64 `json:"migrate_to_chat_id,omitempty"`
}

// APIError возвращается при ok=false в ответе.
type APIError struct {
	Code        int
	Description string
	RetryAfter  time.Duration
}

func (e *APIError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("telegram API %d: %s (retry after %s)", e.Code, e.Description, e.RetryAfter)
	}
	return fmt.Sprintf("telegram API %d: %s", e.Code, e.Description)
}

// endpoint строит URL /bot<TOKEN>/<method>. Токен в path — стандарт Bot API.
func (c *Client) endpoint(method string) string {
	return fmt.Sprintf("%s/bot%s/%s",
		strings.TrimRight(c.cfg.APIBase, "/"), c.cfg.Token, method)
}

// doJSON выполняет запрос, парсит общий apiResp, обрабатывает 429 retry_after.
// out — куда десериализовать `result` (может быть nil).
func (c *Client) doJSON(ctx context.Context, method string, buildReq func() (*http.Request, error), out any) error {
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		req, err := buildReq()
		if err != nil {
			return err
		}
		req = req.WithContext(ctx)

		resp, err := c.http.Do(req)
		if err != nil {
			return c.scrubErr(fmt.Errorf("http %s: %w", method, err))
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return c.scrubErr(fmt.Errorf("read body %s: %w", method, readErr))
		}

		var r apiResp
		if err := json.Unmarshal(body, &r); err != nil {
			return c.scrubErr(fmt.Errorf("parse response %s (status=%d): %w; body=%q",
				method, resp.StatusCode, err, truncate(string(body), 256)))
		}

		if r.OK {
			if out != nil && len(r.Result) > 0 {
				if err := json.Unmarshal(r.Result, out); err != nil {
					return fmt.Errorf("parse result %s: %w", method, err)
				}
			}
			return nil
		}

		apiErr := &APIError{
			Code:        r.ErrorCode,
			Description: r.Description,
			RetryAfter:  time.Duration(r.Parameters.RetryAfter) * time.Second,
		}
		// Ретраимся только на 429 и только если TG явно указал retry_after
		// в разумных пределах.
		if apiErr.Code == http.StatusTooManyRequests &&
			attempt < c.cfg.MaxRetries &&
			apiErr.RetryAfter > 0 &&
			apiErr.RetryAfter <= c.cfg.MaxRetryAfter {
			c.logger.Warn("telegram 429, ждём",
				slog.String("method", method),
				slog.Duration("retry_after", apiErr.RetryAfter),
				slog.Int("attempt", attempt+1),
			)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(apiErr.RetryAfter):
			}
			continue
		}
		return apiErr
	}
	return fmt.Errorf("telegram %s: исчерпан retry-бюджет", method)
}

// scrubErr вытирает токен из текста ошибки (NFR-S2 защита).
func (c *Client) scrubErr(err error) error {
	if err == nil || c.cfg.Token == "" {
		return err
	}
	s := err.Error()
	if !strings.Contains(s, c.cfg.Token) {
		return err
	}
	return errors.New(strings.ReplaceAll(s, c.cfg.Token, "***"))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ----- API methods -----

// User — минимально нужные поля Bot User для healthcheck.
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

// GetMe — пинг токена. Возвращает Bot User.
func (c *Client) GetMe(ctx context.Context) (*User, error) {
	var u User
	err := c.doJSON(ctx, "getMe", func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, c.endpoint("getMe"), nil)
	}, &u)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// Message — минимальные поля отправленного сообщения (message_id достаточно
// для идемпотентности и логов).
type Message struct {
	MessageID int64 `json:"message_id"`
	Date      int64 `json:"date"`
	Chat      struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
}

// SendMessageParams — параметры sendMessage (расширяется по мере надобности).
type SendMessageParams struct {
	ChatID                int64
	Text                  string
	ParseMode             string // "HTML" | "MarkdownV2" | ""
	DisableWebPagePreview bool
	MessageThreadID       int64 // для supergroup topics (v0.2)
}

// SendMessage — текстовое сообщение. Используется для cover.
// Для cover ожидаемый ParseMode="HTML".
func (c *Client) SendMessage(ctx context.Context, p SendMessageParams) (*Message, error) {
	if p.ChatID == 0 {
		return nil, errors.New("ChatID == 0")
	}
	if strings.TrimSpace(p.Text) == "" {
		return nil, errors.New("пустой Text")
	}
	form := url.Values{}
	form.Set("chat_id", strconv.FormatInt(p.ChatID, 10))
	form.Set("text", p.Text)
	if p.ParseMode != "" {
		form.Set("parse_mode", p.ParseMode)
	}
	if p.DisableWebPagePreview {
		form.Set("disable_web_page_preview", "true")
	}
	if p.MessageThreadID != 0 {
		form.Set("message_thread_id", strconv.FormatInt(p.MessageThreadID, 10))
	}

	var msg Message
	err := c.doJSON(ctx, "sendMessage", func() (*http.Request, error) {
		req, _ := http.NewRequest(http.MethodPost, c.endpoint("sendMessage"), strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return req, nil
	}, &msg)
	if err != nil {
		return nil, err
	}
	return &msg, nil
}

// Attachment — файл для sendDocument / sendMediaGroup.
type Attachment struct {
	// Filename — имя файла, видимое в TG.
	Filename string
	// Content — содержимое. Если sendMediaGroup прогоняет retry (не
	// делаем этого в MVP), потребуется seekable Reader; пока просто
	// читаем один раз.
	Content io.Reader
	// Caption — опциональный текст. Лимит 1024 символа (силой не
	// обрезаем — caller отвечает).
	Caption   string
	ParseMode string
}

// SendDocumentParams — sendDocument.
type SendDocumentParams struct {
	ChatID          int64
	MessageThreadID int64
	Doc             Attachment
}

// SendDocument шлёт один файл multipart-формой.
func (c *Client) SendDocument(ctx context.Context, p SendDocumentParams) (*Message, error) {
	if p.ChatID == 0 {
		return nil, errors.New("ChatID == 0")
	}
	if p.Doc.Filename == "" || p.Doc.Content == nil {
		return nil, errors.New("Doc.Filename/Content обязательны")
	}

	body, contentType, err := buildMultipart(func(w *multipart.Writer) error {
		if err := w.WriteField("chat_id", strconv.FormatInt(p.ChatID, 10)); err != nil {
			return err
		}
		if p.MessageThreadID != 0 {
			if err := w.WriteField("message_thread_id", strconv.FormatInt(p.MessageThreadID, 10)); err != nil {
				return err
			}
		}
		if p.Doc.Caption != "" {
			if err := w.WriteField("caption", p.Doc.Caption); err != nil {
				return err
			}
			if p.Doc.ParseMode != "" {
				if err := w.WriteField("parse_mode", p.Doc.ParseMode); err != nil {
					return err
				}
			}
		}
		return writeFilePart(w, "document", p.Doc.Filename, p.Doc.Content)
	})
	if err != nil {
		return nil, err
	}

	var msg Message
	err = c.doJSON(ctx, "sendDocument", func() (*http.Request, error) {
		req, _ := http.NewRequest(http.MethodPost, c.endpoint("sendDocument"), bytes.NewReader(body))
		req.Header.Set("Content-Type", contentType)
		return req, nil
	}, &msg)
	if err != nil {
		return nil, err
	}
	return &msg, nil
}

// SendMediaGroupParams — sendMediaGroup.
type SendMediaGroupParams struct {
	ChatID          int64
	MessageThreadID int64
	// Documents — 2..10 файлов (TG требование). В первом документе caption
	// работает как caption для всего альбома (визуально под первой превью).
	Documents []Attachment
}

// mediaDoc — элемент JSON media[] для InputMediaDocument.
type mediaDoc struct {
	Type      string `json:"type"` // "document"
	Media     string `json:"media"`
	Caption   string `json:"caption,omitempty"`
	ParseMode string `json:"parse_mode,omitempty"`
}

// SendMediaGroup шлёт 2..10 файлов альбомом. Возвращает Message каждого
// отправленного документа (первый элемент — сам альбом; порядок совпадает
// с Documents).
func (c *Client) SendMediaGroup(ctx context.Context, p SendMediaGroupParams) ([]Message, error) {
	if p.ChatID == 0 {
		return nil, errors.New("ChatID == 0")
	}
	n := len(p.Documents)
	if n < 2 || n > 10 {
		return nil, fmt.Errorf("sendMediaGroup ожидает 2..10 файлов, передано %d", n)
	}
	for i, d := range p.Documents {
		if d.Filename == "" || d.Content == nil {
			return nil, fmt.Errorf("documents[%d]: Filename/Content обязательны", i)
		}
	}

	// Сначала формируем media JSON, потом дописываем multipart parts
	// с соответствующими attach-именами.
	media := make([]mediaDoc, n)
	attachNames := make([]string, n)
	for i, d := range p.Documents {
		attachNames[i] = fmt.Sprintf("f%d", i)
		media[i] = mediaDoc{
			Type:      "document",
			Media:     "attach://" + attachNames[i],
			Caption:   d.Caption,
			ParseMode: d.ParseMode,
		}
	}
	mediaJSON, err := json.Marshal(media)
	if err != nil {
		return nil, fmt.Errorf("marshal media: %w", err)
	}

	body, contentType, err := buildMultipart(func(w *multipart.Writer) error {
		if err := w.WriteField("chat_id", strconv.FormatInt(p.ChatID, 10)); err != nil {
			return err
		}
		if p.MessageThreadID != 0 {
			if err := w.WriteField("message_thread_id", strconv.FormatInt(p.MessageThreadID, 10)); err != nil {
				return err
			}
		}
		if err := w.WriteField("media", string(mediaJSON)); err != nil {
			return err
		}
		for i, d := range p.Documents {
			if err := writeFilePart(w, attachNames[i], d.Filename, d.Content); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	var msgs []Message
	err = c.doJSON(ctx, "sendMediaGroup", func() (*http.Request, error) {
		req, _ := http.NewRequest(http.MethodPost, c.endpoint("sendMediaGroup"), bytes.NewReader(body))
		req.Header.Set("Content-Type", contentType)
		return req, nil
	}, &msgs)
	if err != nil {
		return nil, err
	}
	return msgs, nil
}

// ----- helpers -----

func buildMultipart(writeFn func(*multipart.Writer) error) ([]byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := writeFn(w); err != nil {
		return nil, "", err
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

// writeFilePart пишет бинарный part c указанным form-field name и filename.
// Добавляет Content-Type: application/octet-stream по умолчанию.
func writeFilePart(w *multipart.Writer, fieldName, filename string, r io.Reader) error {
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition",
		fmt.Sprintf(`form-data; name=%q; filename=%q`, fieldName, filename))
	h.Set("Content-Type", "application/octet-stream")
	part, err := w.CreatePart(h)
	if err != nil {
		return err
	}
	_, err = io.Copy(part, r)
	return err
}
