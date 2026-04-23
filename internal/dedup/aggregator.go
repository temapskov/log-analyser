package dedup

import (
	"sort"
	"sync"
	"time"

	"github.com/QCoreTech/log_analyser/internal/collector"
)

// Incident — один инцидент: группа записей одного fingerprint'а в одном хосте.
type Incident struct {
	Fingerprint Fingerprint
	Host        string
	App         string
	// Level эскалируется до максимального наблюдённого (critical > error).
	Level     string
	Module    string
	Count     uint64
	FirstSeen time.Time
	LastSeen  time.Time
	// Examples — первые N разных _msg (исходных, до нормализации).
	// Используются рендером для выдержек. Дубликаты отфильтровываются.
	Examples []string
	// StreamIDs — уникальные _stream_id, в которых встречался инцидент.
	StreamIDs map[string]struct{}
	// NormalizedMsg — результат Normalizer.Apply(first _msg); тот самый
	// «шаблон», попавший в хэш. Полезен для отладки и для Grafana-deeplink
	// (ссылка на "все подобные сообщения").
	NormalizedMsg string
}

// TotalStreams возвращает уникальных стримов, агрегированных в инциденте.
func (i *Incident) TotalStreams() int { return len(i.StreamIDs) }

// Aggregator — потокобезопасный агрегатор инцидентов по хостам.
// Интерфейс предназначен для stream-добавления напрямую из
// collector.StreamQuery — один обход VL, nol allocation per entry
// помимо new-incident create.
type Aggregator struct {
	normalizer  *Normalizer
	maxExamples int

	mu     sync.Mutex
	byHost map[string]map[Fingerprint]*Incident
}

// NewAggregator создаёт агрегатор. maxExamples — сколько разных _msg
// сохранять внутри одного инцидента для рендера (обычно 3–5).
func NewAggregator(n *Normalizer, maxExamples int) *Aggregator {
	if maxExamples < 1 {
		maxExamples = 1
	}
	return &Aggregator{
		normalizer:  n,
		maxExamples: maxExamples,
		byHost:      map[string]map[Fingerprint]*Incident{},
	}
}

// Add регистрирует запись в агрегаторе и возвращает fingerprint инцидента.
// Безопасен для параллельного вызова (например, по goroutine на хост).
func (a *Aggregator) Add(e collector.LogEntry) Fingerprint {
	norm := a.normalizer.Apply(e.Msg)
	fp := Compute(e.Host, e.App, norm)

	a.mu.Lock()
	defer a.mu.Unlock()

	byFP, ok := a.byHost[e.Host]
	if !ok {
		byFP = map[Fingerprint]*Incident{}
		a.byHost[e.Host] = byFP
	}
	inc, ok := byFP[fp]
	if !ok {
		inc = &Incident{
			Fingerprint:   fp,
			Host:          e.Host,
			App:           e.App,
			Level:         e.Level,
			Module:        e.Module,
			FirstSeen:     e.Time,
			LastSeen:      e.Time,
			StreamIDs:     map[string]struct{}{},
			NormalizedMsg: norm,
		}
		byFP[fp] = inc
	}
	inc.Count++
	if !e.Time.IsZero() {
		if e.Time.Before(inc.FirstSeen) {
			inc.FirstSeen = e.Time
		}
		if e.Time.After(inc.LastSeen) {
			inc.LastSeen = e.Time
		}
	}
	if e.StreamID != "" {
		inc.StreamIDs[e.StreamID] = struct{}{}
	}
	if levelRank(e.Level) > levelRank(inc.Level) {
		inc.Level = e.Level
	}
	if len(inc.Examples) < a.maxExamples && !containsMsg(inc.Examples, e.Msg) {
		inc.Examples = append(inc.Examples, e.Msg)
	}
	return fp
}

// IncidentsFor возвращает копии инцидентов для данного хоста, отсортированные
// по count убыв., FirstSeen возр. (стабильный порядок для снэпшот-тестов).
func (a *Aggregator) IncidentsFor(host string) []*Incident {
	a.mu.Lock()
	defer a.mu.Unlock()
	byFP := a.byHost[host]
	out := make([]*Incident, 0, len(byFP))
	for _, inc := range byFP {
		c := *inc
		// Глубокая копия мап/слайсов, чтобы caller не мог мутировать internal.
		c.StreamIDs = copyStringSet(inc.StreamIDs)
		c.Examples = append([]string(nil), inc.Examples...)
		out = append(out, &c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if !out[i].FirstSeen.Equal(out[j].FirstSeen) {
			return out[i].FirstSeen.Before(out[j].FirstSeen)
		}
		return out[i].Fingerprint < out[j].Fingerprint
	})
	return out
}

// Summary — сводка по хосту: общее число записей, уникальных инцидентов,
// сколько попадает выше / ниже шум-порога NoiseK (сравнение с count).
type Summary struct {
	Host           string
	TotalRecords   uint64
	TotalIncidents int
	Above          []*Incident // count >= NoiseK
	Below          []*Incident // count < NoiseK
	BelowRecords   uint64      // сумма Count у инцидентов ниже порога
}

// SummaryFor формирует Summary по хосту с заданным шум-порогом.
// NoiseK=1 означает «всё в Above, Below пустой».
func (a *Aggregator) SummaryFor(host string, noiseK int) Summary {
	if noiseK < 1 {
		noiseK = 1
	}
	incidents := a.IncidentsFor(host)
	var s Summary
	s.Host = host
	s.TotalIncidents = len(incidents)
	for _, inc := range incidents {
		s.TotalRecords += inc.Count
		if inc.Count >= uint64(noiseK) {
			s.Above = append(s.Above, inc)
		} else {
			s.Below = append(s.Below, inc)
			s.BelowRecords += inc.Count
		}
	}
	return s
}

// Hosts — список всех хостов, где была хоть одна запись.
func (a *Aggregator) Hosts() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, 0, len(a.byHost))
	for h := range a.byHost {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

func levelRank(l string) int {
	switch l {
	case "critical":
		return 40
	case "error":
		return 30
	case "warn", "warning":
		return 20
	case "info":
		return 10
	case "debug":
		return 5
	default:
		return 0
	}
}

func containsMsg(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func copyStringSet(src map[string]struct{}) map[string]struct{} {
	dst := make(map[string]struct{}, len(src))
	for k := range src {
		dst[k] = struct{}{}
	}
	return dst
}
