// Package logging настраивает slog-хендлер по конфигу и гарантирует
// маскирование секретов (см. ADR-0006 и NFR-S2). Все другие пакеты получают
// логгер через параметр — глобального slog.Default() в прод-коде не касаемся.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

type Format int

const (
	FormatJSON Format = iota
	FormatText
)

func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "json":
		return FormatJSON, nil
	case "text":
		return FormatText, nil
	default:
		return 0, fmt.Errorf("неизвестный формат логов %q (ожидаются json|text)", s)
	}
}

func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("неизвестный уровень логов %q (ожидаются debug|info|warn|error)", s)
	}
}

type Options struct {
	Writer io.Writer
	Level  slog.Level
	Format Format
	// Secrets — значения, которые должны быть заменены на "***" в любом атрибуте
	// (поверх поля, строки, URL). Пустые строки игнорируются. Порядок не важен.
	Secrets []string
}

// New возвращает готовый *slog.Logger. Секретные значения маскируются через
// ReplaceAttr. Это защита NFR-S2: даже если где-то случайно залогировали
// url со встроенным токеном — он не уйдёт в stdout.
func New(opts Options) *slog.Logger {
	masker := newMasker(opts.Secrets)
	ho := &slog.HandlerOptions{
		Level:       opts.Level,
		ReplaceAttr: masker,
	}
	var h slog.Handler
	switch opts.Format {
	case FormatText:
		h = slog.NewTextHandler(opts.Writer, ho)
	default:
		h = slog.NewJSONHandler(opts.Writer, ho)
	}
	return slog.New(h)
}

func newMasker(secrets []string) func(groups []string, a slog.Attr) slog.Attr {
	filtered := make([]string, 0, len(secrets))
	for _, s := range secrets {
		if s != "" {
			filtered = append(filtered, s)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return func(_ []string, a slog.Attr) slog.Attr {
		if a.Value.Kind() != slog.KindString {
			return a
		}
		v := a.Value.String()
		for _, s := range filtered {
			if strings.Contains(v, s) {
				v = strings.ReplaceAll(v, s, "***")
			}
		}
		if v != a.Value.String() {
			a.Value = slog.StringValue(v)
		}
		return a
	}
}
