// Package state — персистентное хранилище digest run'ов (ADR-0002).
//
// Используется pure-Go драйвер `modernc.org/sqlite`: это обязательное
// требование для деплоя в distroless/static (нет CGO → нет libc). Цена —
// ~15–30% производительности против CGO-обёртки, но у нас < 100 tx/сутки,
// узким местом не является.
//
// Роль store — гарантировать идемпотентность digest cycle (FR-12, NFR-R3):
// если за одно и то же окно run уже помечен как 'done' — повторный запуск
// не отправляет cover повторно.
//
// State НЕ хранит ни содержимого отчётов, ни fingerprint'ов (хотя схема
// оставляет место под это в v0.2 для realtime alert'ов); только маркеры
// доставки.
package state

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// Status — константы статуса run'а.
const (
	StatusStarted   = "started"
	StatusCoverSent = "cover_sent"
	StatusDone      = "done"
	StatusFailed    = "failed"
)

// ErrAlreadyDelivered — возвращается из BeginRun, если run с таким окном
// уже помечен как 'done'. Caller в pipeline должен обработать её явным
// skip'ом (вместо повторной отправки).
var ErrAlreadyDelivered = errors.New("digest за это окно уже доставлен")

// Run — модель строки таблицы runs.
type Run struct {
	ID          string
	WindowFrom  time.Time
	WindowTo    time.Time
	Status      string
	CoverMsgID  sql.NullInt64
	FilesSent   int
	CreatedAt   time.Time
	CompletedAt sql.NullTime
	Err         sql.NullString
}

// Store — интерфейс state-хранилища; реализация пока одна (SQLite).
type Store struct {
	db     *sql.DB
	logger *slog.Logger
}

// Config — параметры открытия.
type Config struct {
	// Path — путь к файлу .db. Для in-memory передавайте ":memory:".
	Path string
	// Timezone — tz для `_time_format=sqlite`; все время хранится в UTC,
	// возвращается в указанной tz при чтении. По умолчанию UTC.
	Timezone string
}

// Open создаёт (при необходимости) файл state и применяет схему.
// Callsite отвечает за Close().
func Open(cfg Config, logger *slog.Logger) (*Store, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if strings.TrimSpace(cfg.Path) == "" {
		return nil, errors.New("Path пуст")
	}
	dsn := buildDSN(cfg)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sql.Open: %w", err)
	}
	// SQLite с WAL любит низкий параллелизм записей.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxIdleTime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	return &Store{db: db, logger: logger}, nil
}

// buildDSN формирует строку подключения к SQLite с нужными прагмами.
// Специальный кейс `:memory:` используется для unit-тестов.
func buildDSN(cfg Config) string {
	if cfg.Path == ":memory:" {
		// Для in-memory WAL бессмыслен.
		return "file::memory:?cache=shared&_pragma=foreign_keys(1)&_time_format=sqlite&_txlock=immediate"
	}
	tz := cfg.Timezone
	if tz == "" {
		tz = "UTC"
	}
	// Используем file:-URI с URL-encoded path для поддержки пробелов/юникода.
	return fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)&_time_format=sqlite&_txlock=immediate&_timezone=%s",
		url.PathEscape(cfg.Path), url.QueryEscape(tz))
}

// Close закрывает БД.
func (s *Store) Close() error { return s.db.Close() }

// BeginRun пытается зарегистрировать новый run'а для окна [from, to).
// Правила:
//   - если существующий run в состоянии 'done' — возвращает его ID и
//     ErrAlreadyDelivered. Caller обязан пропустить доставку.
//   - если существующий run в 'started' / 'cover_sent' / 'failed' —
//     возвращает ID существующего и resumed=true (caller возобновляет).
//   - иначе — INSERT и возвращает новый UUID-hex ID.
//
// Использует IMMEDIATE-транзакцию, чтобы атомарно сделать SELECT + INSERT.
func (s *Store) BeginRun(ctx context.Context, from, to time.Time) (runID string, resumed bool, err error) {
	if !from.Before(to) {
		return "", false, fmt.Errorf("from (%s) >= to (%s)", from.Format(time.RFC3339), to.Format(time.RFC3339))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var existingID, status string
	row := tx.QueryRowContext(ctx,
		`SELECT run_id, status FROM runs WHERE window_from=? AND window_to=? LIMIT 1`,
		from.UTC(), to.UTC())
	err = row.Scan(&existingID, &status)
	switch {
	case err == nil:
		if status == StatusDone {
			_ = tx.Commit()
			return existingID, true, ErrAlreadyDelivered
		}
		if err := tx.Commit(); err != nil {
			return "", false, fmt.Errorf("commit: %w", err)
		}
		return existingID, true, nil
	case errors.Is(err, sql.ErrNoRows):
		// не существует — вставим.
	default:
		return "", false, fmt.Errorf("select run: %w", err)
	}

	newID, err := randomHex(16)
	if err != nil {
		return "", false, err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO runs(run_id, window_from, window_to, status, created_at) VALUES (?,?,?,?,?)`,
		newID, from.UTC(), to.UTC(), StatusStarted, time.Now().UTC())
	if err != nil {
		return "", false, fmt.Errorf("insert run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", false, fmt.Errorf("commit: %w", err)
	}
	return newID, false, nil
}

// MarkCoverSent помечает, что cover-сообщение доставлено и сохраняет его
// message_id. Это pre-commit marker: после этого sendMediaGroup может
// продолжать, а повторный рестарт после неё уже не дублирует cover.
func (s *Store) MarkCoverSent(ctx context.Context, runID string, coverMsgID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE runs SET status=?, cover_msg_id=? WHERE run_id=?`,
		StatusCoverSent, coverMsgID, runID)
	if err != nil {
		return fmt.Errorf("mark cover_sent: %w", err)
	}
	return nil
}

// FinishRun помечает run как полностью доставленный.
func (s *Store) FinishRun(ctx context.Context, runID string, filesSent int) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE runs SET status=?, files_sent=?, completed_at=? WHERE run_id=?`,
		StatusDone, filesSent, time.Now().UTC(), runID)
	if err != nil {
		return fmt.Errorf("finish run: %w", err)
	}
	return nil
}

// FailRun помечает run как failed с текстом ошибки.
func (s *Store) FailRun(ctx context.Context, runID, errMsg string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE runs SET status=?, err=?, completed_at=? WHERE run_id=?`,
		StatusFailed, errMsg, time.Now().UTC(), runID)
	if err != nil {
		return fmt.Errorf("fail run: %w", err)
	}
	return nil
}

// FindCompletedRun ищет успешно завершённый run за заданное окно.
// Возвращает nil, nil если такого нет.
func (s *Store) FindCompletedRun(ctx context.Context, from, to time.Time) (*Run, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT run_id, window_from, window_to, status, cover_msg_id, files_sent, created_at, completed_at, err
		 FROM runs WHERE window_from=? AND window_to=? AND status=? LIMIT 1`,
		from.UTC(), to.UTC(), StatusDone)
	var r Run
	err := row.Scan(&r.ID, &r.WindowFrom, &r.WindowTo, &r.Status,
		&r.CoverMsgID, &r.FilesSent, &r.CreatedAt, &r.CompletedAt, &r.Err)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find run: %w", err)
	}
	return &r, nil
}

// RecentRuns — последние N запусков, отсортированные по created_at desc.
// Нужно для CLI `status` (опционально) и /healthz (есть ли недавние успехи).
func (s *Store) RecentRuns(ctx context.Context, limit int) ([]Run, error) {
	if limit < 1 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT run_id, window_from, window_to, status, cover_msg_id, files_sent, created_at, completed_at, err
		 FROM runs ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.WindowFrom, &r.WindowTo, &r.Status,
			&r.CoverMsgID, &r.FilesSent, &r.CreatedAt, &r.CompletedAt, &r.Err); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
