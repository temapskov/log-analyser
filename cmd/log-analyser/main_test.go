package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun_Version(t *testing.T) {
	var out, errw bytes.Buffer
	code := run([]string{"version"}, &out, &errw)
	if code != exitOK {
		t.Fatalf("exit code: got %d, want %d", code, exitOK)
	}
	if !strings.Contains(out.String(), "log_analyser") {
		t.Errorf("output should contain binary name: %q", out.String())
	}
}

func TestRun_Help(t *testing.T) {
	var out, errw bytes.Buffer
	code := run([]string{"help"}, &out, &errw)
	if code != exitOK {
		t.Fatalf("exit code: got %d", code)
	}
	if !strings.Contains(out.String(), "run") ||
		!strings.Contains(out.String(), "once") ||
		!strings.Contains(out.String(), "health") {
		t.Errorf("usage must list all commands, got: %q", out.String())
	}
}

func TestRun_NoArgs(t *testing.T) {
	var out, errw bytes.Buffer
	code := run(nil, &out, &errw)
	if code != exitUsage {
		t.Errorf("no args: expected usage exit, got %d", code)
	}
}

func TestRun_UnknownCommand(t *testing.T) {
	var out, errw bytes.Buffer
	code := run([]string{"foobar"}, &out, &errw)
	if code != exitUsage {
		t.Errorf("unknown cmd: expected usage exit, got %d", code)
	}
	if !strings.Contains(errw.String(), "неизвестная команда") {
		t.Errorf("expected russian error message, got: %q", errw.String())
	}
}

func TestRun_Once_RequiresDate(t *testing.T) {
	var out, errw bytes.Buffer
	// Не ставим env'ы — но flag-check произойдёт ДО загрузки конфига,
	// так что тест проверяет ровно usage-error.
	code := run([]string{"once"}, &out, &errw)
	if code != exitUsage {
		t.Errorf("once without --date: expected usage exit, got %d", code)
	}
}
