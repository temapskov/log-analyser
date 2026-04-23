// Package collector — клиент VictoriaLogs и построитель LogsQL-запросов.
//
// LogsQL builder и client разделены: builder — чистая функция (тестируется
// изолированно), client — HTTP-сторона (тестируется через httptest + integration).
package collector

import (
	"fmt"
	"strings"
	"time"
)

// StreamFilter — стрим-фильтр VictoriaLogs вида
// `{host="t5",level=~"error|critical"}`.
// Формируется для быстрого отсева: стрим-фильтры используют индекс VL
// и пропускают блоки данных (см. docs/plans/00-analysis.md §3.1).
type StreamFilter struct {
	Host   string
	Levels []string // если пусто — фильтр по level не добавляется
}

// Build возвращает строковое представление стрим-фильтра без кавычек окружения.
// Пример: `{host="t5",level=~"error|critical"}`.
func (f StreamFilter) Build() string {
	var parts []string
	if f.Host != "" {
		parts = append(parts, fmt.Sprintf(`host=%q`, f.Host))
	}
	switch len(f.Levels) {
	case 0:
		// нет фильтра по level
	case 1:
		parts = append(parts, fmt.Sprintf(`level=%q`, f.Levels[0]))
	default:
		parts = append(parts, fmt.Sprintf(`level=~%q`, strings.Join(f.Levels, "|")))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// Query описывает параметры LogsQL-запроса к VictoriaLogs.
// Все поля опциональны, кроме Stream и времени.
type Query struct {
	Stream StreamFilter
	From   time.Time // включительно
	To     time.Time // исключительно
	// Extra — свободные фильтры, которые допишутся после стрим-фильтра
	// и _time. Напр., `app:="agent"` или `_msg:~"stopped"`.
	Extra string
	// Pipe — хвостовые операции: `| stats by (app) count()`,
	// `| sort by (_time desc)` и т.п. Передаётся как есть.
	Pipe string
}

// Build возвращает LogsQL-строку, готовую к отправке в /select/logsql/query.
//
// Формат по рекомендации VL: стрим-фильтр → _time → прочие фильтры → pipe.
// Один _time фильтр наверху — это perf-recommendation из документации VL
// (см. docs/plans/00-analysis.md §3.1).
//
// Пример:
//
//	{host="t5",level=~"error|critical"} _time:[2026-04-22T05:00:00Z, 2026-04-23T05:00:00Z)
//
// Пустые поля пропускаются. Валидация — см. Validate().
func (q Query) Build() (string, error) {
	if err := q.Validate(); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(q.Stream.Build())
	b.WriteString(" _time:[")
	b.WriteString(q.From.UTC().Format(time.RFC3339Nano))
	b.WriteString(", ")
	b.WriteString(q.To.UTC().Format(time.RFC3339Nano))
	b.WriteString(")")
	if e := strings.TrimSpace(q.Extra); e != "" {
		b.WriteString(" ")
		b.WriteString(e)
	}
	if p := strings.TrimSpace(q.Pipe); p != "" {
		b.WriteString(" ")
		if !strings.HasPrefix(p, "|") {
			b.WriteString("| ")
		}
		b.WriteString(p)
	}
	return b.String(), nil
}

// Validate — бизнес-проверки параметров запроса.
func (q Query) Validate() error {
	if q.Stream.Host == "" && len(q.Stream.Levels) == 0 {
		return fmt.Errorf("пустой стрим-фильтр — задайте Host и/или Levels")
	}
	if q.From.IsZero() || q.To.IsZero() {
		return fmt.Errorf("From/To не заданы")
	}
	if !q.From.Before(q.To) {
		return fmt.Errorf("From (%s) должно быть строго меньше To (%s)",
			q.From.Format(time.RFC3339), q.To.Format(time.RFC3339))
	}
	return nil
}
