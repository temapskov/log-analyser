package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"":        slog.LevelInfo,
		"info":    slog.LevelInfo,
		"INFO":    slog.LevelInfo,
		" debug ": slog.LevelDebug,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
	}
	for in, want := range cases {
		in, want := in, want
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			got, err := ParseLevel(in)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != want {
				t.Fatalf("got %v, want %v", got, want)
			}
		})
	}
	if _, err := ParseLevel("trace"); err == nil {
		t.Fatal("ожидали ошибку для неизвестного уровня")
	}
}

func TestParseFormat(t *testing.T) {
	cases := map[string]Format{
		"":     FormatJSON,
		"json": FormatJSON,
		"text": FormatText,
		"TEXT": FormatText,
	}
	for in, want := range cases {
		in, want := in, want
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			got, err := ParseFormat(in)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != want {
				t.Fatalf("got %v, want %v", got, want)
			}
		})
	}
	if _, err := ParseFormat("xml"); err == nil {
		t.Fatal("ожидали ошибку для неизвестного формата")
	}
}

func TestNew_MasksSecretsInAttributes(t *testing.T) {
	token := "SECRET_TOKEN_VALUE_DO_NOT_LOG"
	var buf bytes.Buffer
	l := New(Options{
		Writer:  &buf,
		Level:   slog.LevelInfo,
		Format:  FormatJSON,
		Secrets: []string{token, ""},
	})
	l.Info("starting",
		slog.String("tg_url", "https://api.telegram.org/bot"+token+"/getMe"),
		slog.String("comment", "everything is fine"),
	)
	out := buf.String()
	if strings.Contains(out, token) {
		t.Fatalf("токен просочился в лог: %s", out)
	}
	if !strings.Contains(out, "***") {
		t.Fatalf("ожидали маркер ***, got: %s", out)
	}
	if !strings.Contains(out, "everything is fine") {
		t.Fatalf("нормальные атрибуты потерялись: %s", out)
	}
}

func TestNew_NoSecretsConfigured_NoMaskingOverhead(t *testing.T) {
	var buf bytes.Buffer
	l := New(Options{Writer: &buf, Level: slog.LevelInfo, Format: FormatJSON})
	l.Info("ok", slog.String("x", "y"))
	if !strings.Contains(buf.String(), `"x":"y"`) {
		t.Fatalf("ожидали нормальный JSON-вывод, got: %s", buf.String())
	}
}
