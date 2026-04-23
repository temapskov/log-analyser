package version

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCurrent_ReflectsPackageVars(t *testing.T) {
	orig := Version
	t.Cleanup(func() { Version = orig })

	Version = "1.2.3"
	got := Current()
	if got.Version != "1.2.3" {
		t.Errorf("Version: got %q", got.Version)
	}
	if got.GoVersion == "" || got.OS == "" || got.Arch == "" {
		t.Errorf("runtime-поля должны быть заполнены: %+v", got)
	}
}

func TestInfo_String(t *testing.T) {
	orig := Version
	t.Cleanup(func() { Version = orig })
	Version = "0.1.0"
	s := Current().String()
	if !strings.Contains(s, "log_analyser") || !strings.Contains(s, "0.1.0") {
		t.Errorf("String: %q", s)
	}
}

func TestInfo_JSON(t *testing.T) {
	b, err := json.Marshal(Current())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"version"`) {
		t.Errorf("JSON: %s", string(b))
	}
}
