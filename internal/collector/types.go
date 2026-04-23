package collector

import (
	"encoding/json"
	"time"
)

// LogEntry — одна запись из VictoriaLogs.
// VL отдаёт JSON-lines (Content-Type: application/x-jsonlines), каждая строка —
// объект с обязательными полями (_time, _msg, _stream, _stream_id) и
// произвольным набором полей лога.
//
// Вариант реального ответа VL (2026-04-23, prod):
//
//	{"_time":"2026-04-23T06:35:19.484087Z",
//	 "_stream_id":"0000...",
//	 "_stream":"{app=\"agent\",host=\"t5\",job=\"xt\",level=\"error\",module=\"strategy:...\"}",
//	 "_msg":"...",
//	 "app":"agent", "host":"t5", "job":"xt", "level":"error",
//	 "module":"strategy:..."}
type LogEntry struct {
	Time     time.Time `json:"_time"`
	StreamID string    `json:"_stream_id"`
	Stream   string    `json:"_stream"`
	Msg      string    `json:"_msg"`

	// Общие поля торговых серверов QCoreTech — извлекаем из верхнего уровня
	// (не из _stream, чтобы не парсить двойной формат). Отсутствующие поля
	// остаются пустыми строками.
	Host   string `json:"host,omitempty"`
	App    string `json:"app,omitempty"`
	Level  string `json:"level,omitempty"`
	Module string `json:"module,omitempty"`
	Job    string `json:"job,omitempty"`

	// Raw — произвольные поля, не распознанные выше. Используется
	// render'ом для детализированных выдержек и отладки.
	Raw map[string]json.RawMessage `json:"-"`
}

// HitPoint — одна точка гистограммы /select/logsql/hits.
type HitPoint struct {
	Timestamp time.Time         `json:"-"`
	Value     uint64            `json:"-"`
	Fields    map[string]string `json:"fields,omitempty"`
}

// HitsResponse — ответ /select/logsql/hits.
// Формат VL: {"hits":[{"fields":{...},"timestamps":["..."],"values":[...],"total":N}]}.
type HitsResponse struct {
	Hits []HitsSeries `json:"hits"`
}

type HitsSeries struct {
	Fields     map[string]string `json:"fields"`
	Timestamps []time.Time       `json:"timestamps"`
	Values     []uint64          `json:"values"`
	Total      uint64            `json:"total"`
}
