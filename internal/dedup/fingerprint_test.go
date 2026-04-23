package dedup

import (
	"strings"
	"testing"
)

func TestCompute_Deterministic(t *testing.T) {
	a := Compute("t5", "agent", "Ошибка X <ts>")
	b := Compute("t5", "agent", "Ошибка X <ts>")
	if a != b {
		t.Errorf("недетерминированный fingerprint: %s vs %s", a, b)
	}
}

func TestCompute_DifferentInputs_DifferentOutputs(t *testing.T) {
	// Защита от коллизии «foo"|"bar" == "foobar"» за счёт разделителя \0.
	a := Compute("foo", "bar", "msg")
	b := Compute("foobar", "", "msg")
	if a == b {
		t.Errorf("ambiguity: %s == %s", a, b)
	}
}

func TestCompute_OnlyNormalizedMsgAffects(t *testing.T) {
	// Два сообщения, отличающиеся до нормализации, но одинаковые после,
	// должны в этой функции (Compute получает УЖЕ нормализованный msg)
	// дать одинаковый fingerprint.
	a := Compute("t5", "agent", "Ошибка X")
	b := Compute("t5", "agent", "Ошибка X")
	if a != b {
		t.Errorf("одинаковый вход даёт разные fingerprint: %s vs %s", a, b)
	}
	c := Compute("t5", "agent", "Ошибка Y")
	if a == c {
		t.Errorf("разные нормализации не различаются: %s", a)
	}
}

func TestFingerprint_Short(t *testing.T) {
	fp := Compute("t5", "agent", "Ошибка X")
	if len(fp) != 40 {
		t.Errorf("sha1 должен быть 40 hex-символов: %d (%s)", len(fp), fp)
	}
	if len(fp.Short()) != 12 {
		t.Errorf("Short должен быть 12: %d", len(fp.Short()))
	}
	if !strings.HasPrefix(string(fp), fp.Short()) {
		t.Errorf("Short не префикс полного: %s / %s", fp.Short(), fp)
	}

	tiny := Fingerprint("abc")
	if tiny.Short() != "abc" {
		t.Errorf("на коротких fingerprint'ах Short должен возвращать как есть: %q", tiny.Short())
	}
}

// Проверка что fingerprint действительно hex: нигде не проскальзывают
// не-hex символы (может случиться, если sha1 Sum неправильно forматировать).
func TestFingerprint_IsHex(t *testing.T) {
	fp := Compute("t5", "agent", "msg")
	for _, r := range string(fp) {
		ok := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
		if !ok {
			t.Errorf("не-hex символ %q в %s", r, fp)
		}
	}
}
