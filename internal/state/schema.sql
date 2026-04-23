-- Схема state-хранилища log_analyser (ADR-0002).
-- Создаётся при первом подключении через CREATE TABLE IF NOT EXISTS.
-- Миграции в MVP линейны и аддитивны — добавление колонок через новые
-- DDL-файлы в последующих PR.

CREATE TABLE IF NOT EXISTS runs (
    run_id       TEXT PRIMARY KEY,
    window_from  DATETIME NOT NULL,
    window_to    DATETIME NOT NULL,
    status       TEXT NOT NULL,            -- 'started' | 'cover_sent' | 'done' | 'failed'
    cover_msg_id INTEGER,
    files_sent   INTEGER NOT NULL DEFAULT 0,
    created_at   DATETIME NOT NULL,
    completed_at DATETIME,
    err          TEXT,
    UNIQUE(window_from, window_to)
);

CREATE INDEX IF NOT EXISTS idx_runs_window ON runs(window_from, window_to);
CREATE INDEX IF NOT EXISTS idx_runs_created ON runs(created_at);
CREATE INDEX IF NOT EXISTS idx_runs_status ON runs(status);
