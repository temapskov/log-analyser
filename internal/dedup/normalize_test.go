package dedup

import (
	"strings"
	"testing"
)

func TestCompile_RejectsBadPattern(t *testing.T) {
	_, err := Compile([]RawRule{{Name: "bad", Pattern: "["}})
	if err == nil {
		t.Fatal("ожидали ошибку компиляции")
	}
	_, err = Compile([]RawRule{{Name: "empty", Pattern: ""}})
	if err == nil {
		t.Fatal("ожидали ошибку на пустой pattern")
	}
}

func TestDefaultNormalizer_Table(t *testing.T) {
	n := DefaultNormalizer()
	cases := []struct {
		name string
		in   string
		want string // ожидаемая нормализованная форма
	}{
		{
			// NB: `t5` внутри идентификатора не заменяется на <N> — нет
			// word-boundary между буквой и цифрой в Go regexp \b. Это
			// желаемое поведение: t5/t6 — разные стратегии, их ошибки
			// нужно различать.
			"qcoretech-style",
			"23.04.2026 06:35:19.484087 +00:00 | E | strategy:t_t5-1:50 | Стратегия 'strat' сообщила об ошибке",
			"<ts> | E | strategy:t_t5-<N>:<N> | Стратегия 'strat' сообщила об ошибке",
		},
		{
			"iso timestamp",
			"at 2026-04-22T05:00:00.123Z an error occurred",
			"at <ts> an error occurred",
		},
		{
			"uuid",
			"user=550e8400-e29b-41d4-a716-446655440000 failed",
			"user=<uuid> failed",
		},
		{
			"ipv4 with port",
			"connection refused to 10.0.1.45:8443",
			"connection refused to <ip>",
		},
		{
			"hex16 order id",
			"order uid: 0A1400023EA3E990 status: NEW",
			"order uid: <hex> status: NEW",
		},
		{
			"plain number",
			"processed 42 items",
			"processed <N> items",
		},
		{
			"two timestamps same line",
			"from 2026-04-22T05:00:00Z to 2026-04-23T05:00:00Z",
			"from <ts> to <ts>",
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := n.Apply(c.in)
			if got != c.want {
				t.Errorf("\n  in:   %s\n  got:  %s\n  want: %s", c.in, got, c.want)
			}
		})
	}
}

// TestNormalize_Idempotent — ключевой инвариант: повторное применение
// нормализатора к своему собственному выходу НЕ должно его менять.
// Если свойство нарушается, dedup начинает лавинно склеивать разные
// ошибки.
func TestNormalize_Idempotent(t *testing.T) {
	n := DefaultNormalizer()
	samples := []string{
		"plain string without numbers or timestamps",
		"23.04.2026 06:35:19.484087 +00:00 | E | strategy:t_t5-1:50 | error",
		"user=550e8400-e29b-41d4-a716-446655440000 ip=10.0.1.45:8443",
		"order uid: 0A1400023EA3E990 p: 0.72670000 q: 347.00000000",
		"at 2026-04-22T05:00:00Z error 500 from https://example.com/a/b?c=d",
	}
	for _, s := range samples {
		s := s
		t.Run(short(s), func(t *testing.T) {
			t.Parallel()
			once := n.Apply(s)
			twice := n.Apply(once)
			if once != twice {
				t.Errorf("не идемпотентно:\n  once:  %s\n  twice: %s", once, twice)
			}
		})
	}
}

// TestNormalize_DedupsVaryingTimestamps — два лога, отличающиеся только
// timestamp'ом, должны нормализоваться в одинаковую строку (иначе их
// fingerprint тоже разойдётся).
func TestNormalize_DedupsVaryingTimestamps(t *testing.T) {
	n := DefaultNormalizer()
	a := n.Apply("23.04.2026 06:35:19.484087 +00:00 | E | m | Ошибка X")
	b := n.Apply("23.04.2026 07:12:01.000000 +00:00 | E | m | Ошибка X")
	if a != b {
		t.Errorf("timestamp не схлопнулся:\n  a=%s\n  b=%s", a, b)
	}
}

func TestNormalize_DedupsVaryingOrderIDs(t *testing.T) {
	n := DefaultNormalizer()
	a := n.Apply("order uid: 0A1400023EA3E990 status: NEW")
	b := n.Apply("order uid: F24CB39B00E1AC91 status: NEW")
	if a != b {
		t.Errorf("order id не схлопнулся:\n  a=%s\n  b=%s", a, b)
	}
}

func TestNormalizer_Empty_NoOp(t *testing.T) {
	n := NewNormalizer(nil)
	if got := n.Apply("anything 42 goes"); got != "anything 42 goes" {
		t.Errorf("пустой nomalizer должен быть no-op: %q", got)
	}
}

// short — хелпер для t.Run имени.
func short(s string) string {
	if len(s) <= 40 {
		return s
	}
	return strings.ReplaceAll(s[:40], " ", "_")
}

// FuzzNormalize — свойство: результат Apply — фиксированная точка
// (Apply(Apply(x)) == Apply(x)). Запуск: `go test -run=Fuzz -fuzz=FuzzNormalize`.
func FuzzNormalize(f *testing.F) {
	n := DefaultNormalizer()
	seeds := []string{
		"",
		"a",
		"error 42",
		"2026-04-22T05:00:00Z",
		"0A1400023EA3E990",
		"trade uid: 550e8400-e29b-41d4-a716-446655440000",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		once := n.Apply(in)
		twice := n.Apply(once)
		if once != twice {
			t.Errorf("fixpoint fail:\n  in:    %q\n  once:  %q\n  twice: %q", in, once, twice)
		}
	})
}
