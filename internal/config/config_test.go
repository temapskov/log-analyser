package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setEnvs устанавливает env-переменные для теста и восстанавливает исходное
// значение на t.Cleanup. Используем t.Setenv, чтобы избежать гонок между
// параллельными тестами (Setenv помечает тест непараллельным автоматически).
func setEnvs(t *testing.T, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func requiredEnvs() map[string]string {
	return map[string]string{
		"TG_BOT_TOKEN":      "bot-token-xyz",
		"TG_CHAT_ID":        "-1001234567890",
		"VL_URL":            "https://vl.example.com",
		"GRAFANA_URL":       "https://grafana.example.com",
		"GRAFANA_VL_DS_UID": "vl-prod-1",
	}
}

func TestLoad_OK_WithDefaults(t *testing.T) {
	setEnvs(t, requiredEnvs())
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.TGBotToken != "bot-token-xyz" {
		t.Errorf("TGBotToken: got %q", cfg.TGBotToken)
	}
	if cfg.TGChatID != -1001234567890 {
		t.Errorf("TGChatID: got %d", cfg.TGChatID)
	}
	if got := cfg.Hosts; strings.Join(got, ",") != "t1,ali-t1,t2,aws-t3,t5" {
		t.Errorf("Hosts default wrong: %v", got)
	}
	if cfg.NoiseK != 5 {
		t.Errorf("NoiseK default must be 5 (решение TL §4.2): got %d", cfg.NoiseK)
	}
	if cfg.ReportFormat != FormatMarkdown {
		t.Errorf("ReportFormat default must be md: got %q", cfg.ReportFormat)
	}
	if cfg.TZ != "Europe/Moscow" {
		t.Errorf("TZ default: got %q", cfg.TZ)
	}
	if cfg.VLTimeout != 30*time.Second {
		t.Errorf("VLTimeout default: got %v", cfg.VLTimeout)
	}
}

func TestLoad_MissingRequired(t *testing.T) {
	// Явно не ставим TG_BOT_TOKEN.
	for _, k := range []string{"TG_BOT_TOKEN", "TG_CHAT_ID", "VL_URL", "GRAFANA_URL", "GRAFANA_VL_DS_UID"} {
		t.Setenv(k, "")
	}
	if _, err := Load(); err == nil {
		t.Fatal("ожидали ошибку об отсутствии required-переменных")
	}
}

func TestValidate_BadURL(t *testing.T) {
	setEnvs(t, requiredEnvs())
	t.Setenv("VL_URL", "vl.example.com") // без схемы
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("ожидали ошибку валидации VL_URL без схемы")
	}
}

func TestValidate_BadTZ(t *testing.T) {
	setEnvs(t, requiredEnvs())
	t.Setenv("TZ", "Nowhere/Atlantis")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("ожидали ошибку валидации TZ")
	}
}

func TestValidate_BadReportFormat(t *testing.T) {
	setEnvs(t, requiredEnvs())
	t.Setenv("REPORT_FORMAT", "pdf")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("ожидали ошибку валидации REPORT_FORMAT")
	}
}

func TestValidate_BadNumerics(t *testing.T) {
	setEnvs(t, requiredEnvs())
	t.Setenv("NOISE_K", "0")
	t.Setenv("TOP_N", "0")
	t.Setenv("REPORTS_RETENTION_DAYS", "0")
	t.Setenv("VL_MAX_RETRIES", "-1")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = cfg.Validate()
	if err == nil {
		t.Fatal("ожидали ошибки валидации числовых полей")
	}
	for _, want := range []string{"NOISE_K", "TOP_N", "REPORTS_RETENTION_DAYS", "VL_MAX_RETRIES"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ожидали упоминание %s в ошибке: %v", want, err)
		}
	}
}

func TestLoad_YAMLOverlay_OK(t *testing.T) {
	setEnvs(t, requiredEnvs())
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := `
fingerprint:
  normalizers:
    - name: uuid
      pattern: '[0-9a-f-]{36}'
      replace: '<uuid>'
    - name: ipv4
      pattern: '\d+\.\d+\.\d+\.\d+'
      replace: '<ip>'
host_labels:
  t5: Прод-T5
  ali-t1: Ali-T1 (Alibaba)
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_FILE", path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Overlay.Fingerprint.Normalizers) != 2 {
		t.Fatalf("ожидали 2 normalizer'а, got %d", len(cfg.Overlay.Fingerprint.Normalizers))
	}
	if cfg.Overlay.Fingerprint.Normalizers[0].Name != "uuid" {
		t.Errorf("первый normalizer имя: %q", cfg.Overlay.Fingerprint.Normalizers[0].Name)
	}
	if cfg.Overlay.HostLabels["t5"] != "Прод-T5" {
		t.Errorf("host_labels[t5]: %q", cfg.Overlay.HostLabels["t5"])
	}
}

func TestLoad_YAMLOverlay_RejectsSecrets(t *testing.T) {
	setEnvs(t, requiredEnvs())
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// ADR-0011: секреты в YAML запрещены.
	data := `
tg_bot_token: "should-not-be-here"
fingerprint: {}
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_FILE", path)

	_, err := Load()
	if err == nil {
		t.Fatal("ожидали ошибку: секрет в YAML")
	}
	if !strings.Contains(err.Error(), "tg_bot_token") {
		t.Errorf("сообщение должно указывать запрещённый ключ: %v", err)
	}
}

func TestLoad_YAMLOverlay_RejectsUnknownField(t *testing.T) {
	setEnvs(t, requiredEnvs())
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := `
unknown_field: 42
fingerprint:
  normalizers: []
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_FILE", path)

	_, err := Load()
	if err == nil {
		t.Fatal("ожидали ошибку: неизвестное поле в YAML (KnownFields)")
	}
}

func TestLoad_YAMLOverlay_MissingFile(t *testing.T) {
	setEnvs(t, requiredEnvs())
	t.Setenv("CONFIG_FILE", "/definitely/not/exist.yaml")
	if _, err := Load(); err == nil {
		t.Fatal("ожидали ошибку: отсутствующий config-файл")
	}
}

func TestSensitiveValues(t *testing.T) {
	cfg := &Config{
		TGBotToken:  "token",
		VLBasicUser: "user",
		VLBasicPass: "pass",
	}
	got := cfg.SensitiveValues()
	joined := strings.Join(got, "|")
	for _, want := range []string{"token", "user", "pass"} {
		if !strings.Contains(joined, want) {
			t.Errorf("SensitiveValues должно включать %q, got %v", want, got)
		}
	}
	cfg2 := &Config{TGBotToken: "t"}
	if len(cfg2.SensitiveValues()) != 1 {
		t.Errorf("без basic auth должно быть только 1 значение, got %v", cfg2.SensitiveValues())
	}
}
