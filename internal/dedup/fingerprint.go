package dedup

import (
	"crypto/sha1"
	"encoding/hex"
	"io"
)

// Fingerprint — hex-представление sha1 от (host + 0x00 + app + 0x00 + norm_msg).
// Используется как дедуп-ключ инцидента (ADR-0009).
//
// Почему sha1: хэшу нужна лишь отсутствие коллизий в пределах дневного окна
// (~сотни тысяч уникальных паттернов max). sha1 даёт 160 бит — более чем
// достаточно, при этом быстрее sha256. Безопасность здесь не критична:
// fingerprint хранится локально, никогда не используется в криптографии.
type Fingerprint string

// Compute считает fingerprint для тройки (host, app, normalizedMsg).
// Разделитель 0x00 исключает ambiguity между, напр., ("foo","bar") и ("foobar","").
func Compute(host, app, normalizedMsg string) Fingerprint {
	h := sha1.New()
	writeField(h, host)
	writeField(h, app)
	_, _ = io.WriteString(h, normalizedMsg)
	return Fingerprint(hex.EncodeToString(h.Sum(nil)))
}

func writeField(h io.Writer, s string) {
	_, _ = io.WriteString(h, s)
	_, _ = h.Write([]byte{0})
}

// Short возвращает первые 12 hex-символов — компактный вариант для логов
// и UI, без потери уникальности на реалистичных объёмах (< 2^48 вариантов).
func (f Fingerprint) Short() string {
	if len(f) <= 12 {
		return string(f)
	}
	return string(f[:12])
}
