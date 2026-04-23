// Package render готовит per-host отчёт (Markdown по ADR-0003) и
// cover-сообщение для Telegram (HTML parse_mode, ADR-0008).
//
// Шаблоны живут в internal/render/templates и зашиты через embed.FS,
// поэтому бинарь самодостаточен (нужно для distroless image — ADR-0002).
//
// FuncMap ограничен и типизирован: никакой ручной работы с путями/URL
// внутри шаблонов. Grafana-deeplink'и строятся снаружи через
// grafana.Config.ExploreURL и передаются в ViewModel готовыми строками
// (ViewModel сознательно не хранит reference на grafana.Config, чтобы
// рендер не зависел от состояния сети).
package render

import (
	"embed"
	"fmt"
	"html"
	"io"
	"strings"
	"text/template"
	"time"

	"github.com/QCoreTech/log_analyser/internal/dedup"
)

//go:embed templates/report.md.tmpl templates/cover.html.tmpl
var tmplFS embed.FS

// IncidentView — incident'у в ViewModel уже прикручен Deeplink.
type IncidentView struct {
	*dedup.Incident
	Deeplink string
}

// HostReport — входные данные per-host шаблона.
type HostReport struct {
	Host            string
	Label           string // optional человекочитаемое имя (host_labels в YAML)
	TZ              string
	Window          Window
	GeneratedAt     time.Time
	TotalError      uint64
	TotalCritical   uint64
	TotalRecords    uint64
	TotalIncidents  int
	AppTotals       []dedup.AppTotals
	TopIncidents    []IncidentView
	BelowIncidents  []IncidentView
	BelowRecords    uint64
	TopN            int
	NoiseK          int
	HostDeeplink    string // «все логи за окно по хосту»
}

type Window struct {
	From time.Time
	To   time.Time
}

// CoverHostRow — одна строка таблицы хостов в cover.
type CoverHostRow struct {
	Host           string
	TotalError     uint64
	TotalCritical  uint64
	TopApp         string // имя приложения с максимальным count в этом хосте
}

// CoverData — входные данные для cover-сообщения (FR-6).
type CoverData struct {
	TZ               string
	Window           Window
	Hosts            []CoverHostRow
	TotalError       uint64
	TotalCritical    uint64
	TotalIncidents   int
	AllHostsDeeplink string
	PartialDelivery  bool
}

// Renderer — проинициализированные шаблоны + таймзона.
type Renderer struct {
	report *template.Template
	cover  *template.Template
	tz     *time.Location
}

// New создаёт Renderer с загруженной таймзоной (для форматирования times
// в шаблонах). При ошибке парсинга tz — возвращает error.
func New(tz string) (*Renderer, error) {
	if tz == "" {
		tz = "Europe/Moscow"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("tz %q: %w", tz, err)
	}
	r := &Renderer{tz: loc}
	funcs := r.funcMap()

	report, err := template.New("report.md.tmpl").Funcs(funcs).ParseFS(tmplFS, "templates/report.md.tmpl")
	if err != nil {
		return nil, fmt.Errorf("report template: %w", err)
	}
	cover, err := template.New("cover.html.tmpl").Funcs(funcs).ParseFS(tmplFS, "templates/cover.html.tmpl")
	if err != nil {
		return nil, fmt.Errorf("cover template: %w", err)
	}
	r.report = report
	r.cover = cover
	return r, nil
}

// RenderHost пишет per-host Markdown-отчёт в w.
func (r *Renderer) RenderHost(w io.Writer, rep HostReport) error {
	if rep.TZ == "" {
		rep.TZ = r.tz.String()
	}
	return r.report.ExecuteTemplate(w, "report.md.tmpl", rep)
}

// RenderCover пишет cover-сообщение для Telegram (HTML parse_mode) в w.
func (r *Renderer) RenderCover(w io.Writer, cov CoverData) error {
	if cov.TZ == "" {
		cov.TZ = r.tz.String()
	}
	return r.cover.ExecuteTemplate(w, "cover.html.tmpl", cov)
}

func (r *Renderer) funcMap() template.FuncMap {
	return template.FuncMap{
		"fmtTime":   r.fmtTime,
		"fmtTimeRU": r.fmtTimeRU,
		"firstLine": firstLine,
		"htmlEsc":   html.EscapeString,
		"add":       func(a, b int) int { return a + b },
	}
}

func (r *Renderer) fmtTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.In(r.tz).Format("2006-01-02 15:04:05")
}

func (r *Renderer) fmtTimeRU(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.In(r.tz).Format("02.01.2006 15:04")
}

// firstLine обрезает строку до первой \n и ограничивает длину (160),
// чтобы заголовок инцидента не развалил markdown-вид.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	const maxLen = 160
	if len(s) > maxLen {
		// Безопасная обрезка по рунам.
		r := []rune(s)
		if len(r) > maxLen {
			r = r[:maxLen]
		}
		s = string(r) + "…"
	}
	return s
}
