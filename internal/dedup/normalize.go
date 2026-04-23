// Package dedup — нормализация сообщений и fingerprint-группировка
// по ADR-0009.
//
// Подход: fingerprint = sha1(host + app + normalize(_msg)).
// Нормализация убирает из _msg всё, что меняется при каждом повторении
// той же логической ошибки: временные штампы, UUID, IP-адреса, hex-id'ы
// торгов, числа.
//
// Regex-профиль задаётся снаружи (в YAML config.Overlay.Fingerprint —
// см. ADR-0011). В этом пакете есть дефолтный профиль для торговых
// серверов QCoreTech (QCoreTech-specific форматы timestamp'ов, trade-UID
// и т.п.) — используется, если YAML не подан.
package dedup

import (
	"fmt"
	"regexp"
	"strings"
)

// RawRule — до-компилированное правило (как приходит из YAML-конфига).
type RawRule struct {
	Name    string
	Pattern string
	Replace string
}

// Rule — скомпилированное правило.
type Rule struct {
	Name    string
	Pattern *regexp.Regexp
	Replace string
}

// Compile компилирует список raw-правил в готовые Rule. Порядок сохраняется —
// он критичен (timestamps должны идти до чистых чисел, UUID до hex16 и т.п.).
func Compile(raw []RawRule) ([]Rule, error) {
	out := make([]Rule, 0, len(raw))
	for i, r := range raw {
		if strings.TrimSpace(r.Pattern) == "" {
			return nil, fmt.Errorf("правило #%d (%q): пустой pattern", i, r.Name)
		}
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return nil, fmt.Errorf("правило #%d (%q): %w", i, r.Name, err)
		}
		out = append(out, Rule{Name: r.Name, Pattern: re, Replace: r.Replace})
	}
	return out, nil
}

// Normalizer применяет набор правил к строке в зафиксированном порядке.
type Normalizer struct {
	rules []Rule
}

// NewNormalizer — если rules пуст, возвращает идемпотентный "no-op" нормализатор.
// Для значимого дедупа передавай DefaultRules() или кастомный профиль.
func NewNormalizer(rules []Rule) *Normalizer {
	return &Normalizer{rules: rules}
}

// Apply возвращает нормализованный _msg. Функция:
//   - идемпотентна: Apply(Apply(x)) == Apply(x) при «достаточно» полном
//     профиле (инвариант, проверяется тестом-invariant'ом).
//   - детерминирована: один и тот же вход → один и тот же выход.
func (n *Normalizer) Apply(msg string) string {
	for _, r := range n.rules {
		msg = r.Pattern.ReplaceAllString(msg, r.Replace)
	}
	return msg
}

// Rules возвращает копию списка правил (для наблюдаемости / логирования).
func (n *Normalizer) Rules() []Rule {
	out := make([]Rule, len(n.rules))
	copy(out, n.rules)
	return out
}

// DefaultRawRules — встроенный профиль нормализации для торговых серверов
// QCoreTech. Совпадает по смыслу с configs/fingerprint-profile.yaml, но
// компилируется из Go-исходника и используется, когда YAML не подан.
//
// Порядок важен: timestamps → uuid → ip → hex16+ → order/trade uid →
// numbers. Числа идут последними, т.к. все предыдущие правила содержат
// цифры внутри.
func DefaultRawRules() []RawRule {
	return []RawRule{
		{
			Name:    "ts_iso8601",
			Pattern: `\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+\-]\d{2}:?\d{2})?`,
			Replace: "<ts>",
		},
		{
			// QCoreTech-формат в _msg: "23.04.2026 06:35:19.484087 +00:00".
			Name:    "ts_ru",
			Pattern: `\d{2}\.\d{2}\.\d{4}[ T]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:\s*[+\-]\d{2}:\d{2})?`,
			Replace: "<ts>",
		},
		{
			Name:    "uuid",
			Pattern: `[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`,
			Replace: "<uuid>",
		},
		{
			// IPv4 с опциональным :port.
			Name:    "ipv4",
			Pattern: `\b(?:\d{1,3}\.){3}\d{1,3}(?::\d+)?\b`,
			Replace: "<ip>",
		},
		{
			// 16 и более hex-символов подряд: stream_id, order-id (OKX/Binance
			// отдают 16..24 hex), sha1 и т.п. Это перед numbers, чтобы не
			// склеилось в <N>.
			Name:    "hex16plus",
			Pattern: `\b[0-9a-fA-F]{16,}\b`,
			Replace: "<hex>",
		},
		{
			// Числа: целые и дробные. Последним — оставшееся, не схваченное выше.
			Name:    "numbers",
			Pattern: `\b\d+(?:\.\d+)?\b`,
			Replace: "<N>",
		},
	}
}

// DefaultRules — скомпилированный DefaultRawRules. Паникует на несовместимой
// сборке (невозможно в проде, regex'ы фиксированы).
func DefaultRules() []Rule {
	rules, err := Compile(DefaultRawRules())
	if err != nil {
		panic(fmt.Errorf("несовместимый дефолтный профиль: %w", err))
	}
	return rules
}

// DefaultNormalizer — shortcut: NewNormalizer(DefaultRules()).
func DefaultNormalizer() *Normalizer {
	return NewNormalizer(DefaultRules())
}
