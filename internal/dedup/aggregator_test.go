package dedup

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/QCoreTech/log_analyser/internal/collector"
)

func makeEntry(host, app, level, module, msg string, ts time.Time) collector.LogEntry {
	return collector.LogEntry{
		Time:     ts,
		StreamID: host + "-" + app,
		Host:     host,
		App:      app,
		Level:    level,
		Module:   module,
		Msg:      msg,
	}
}

func TestAggregator_DedupSameFingerprint(t *testing.T) {
	agg := NewAggregator(DefaultNormalizer(), 3)
	t0 := time.Date(2026, 4, 23, 6, 0, 0, 0, time.UTC)

	for i := 0; i < 1000; i++ {
		// Меняется только timestamp и номер ордера — оба должны
		// нормализоваться, получится один fingerprint.
		ts := t0.Add(time.Duration(i) * time.Millisecond)
		msg := fmt.Sprintf("%s | order uid: 0A1400023EA3E%03X NEW", ts.Format("02.01.2006 15:04:05.000000"), i)
		agg.Add(makeEntry("t5", "agent", "error", "m", msg, ts))
	}

	incidents := agg.IncidentsFor("t5")
	if len(incidents) != 1 {
		t.Fatalf("ожидали 1 инцидент, got %d", len(incidents))
	}
	inc := incidents[0]
	if inc.Count != 1000 {
		t.Errorf("count: got=%d want=1000", inc.Count)
	}
	if inc.Host != "t5" || inc.App != "agent" || inc.Level != "error" {
		t.Errorf("метаданные: %+v", inc)
	}
	if len(inc.Examples) != 3 {
		t.Errorf("examples: ожидали до 3, got=%d", len(inc.Examples))
	}
	if !inc.FirstSeen.Equal(t0) {
		t.Errorf("FirstSeen: %s vs %s", inc.FirstSeen, t0)
	}
	if inc.LastSeen.Before(t0) {
		t.Errorf("LastSeen < FirstSeen?")
	}
}

func TestAggregator_SeparateHostsSeparateIncidents(t *testing.T) {
	agg := NewAggregator(DefaultNormalizer(), 3)
	ts := time.Now().UTC()
	agg.Add(makeEntry("t5", "agent", "error", "m", "ошибка X", ts))
	agg.Add(makeEntry("t1", "agent", "error", "m", "ошибка X", ts))

	if got := agg.IncidentsFor("t5"); len(got) != 1 || got[0].Host != "t5" {
		t.Errorf("t5: %+v", got)
	}
	if got := agg.IncidentsFor("t1"); len(got) != 1 || got[0].Host != "t1" {
		t.Errorf("t1: %+v", got)
	}
	if got := agg.Hosts(); len(got) != 2 || got[0] != "t1" || got[1] != "t5" {
		t.Errorf("hosts: %v", got)
	}
}

func TestAggregator_LevelEscalates(t *testing.T) {
	agg := NewAggregator(DefaultNormalizer(), 1)
	ts := time.Now().UTC()
	agg.Add(makeEntry("t5", "a", "error", "m", "x", ts))
	agg.Add(makeEntry("t5", "a", "critical", "m", "x", ts.Add(time.Second)))
	agg.Add(makeEntry("t5", "a", "error", "m", "x", ts.Add(2*time.Second)))

	incidents := agg.IncidentsFor("t5")
	if len(incidents) != 1 {
		t.Fatalf("ожидали 1 инцидент, got %d", len(incidents))
	}
	if incidents[0].Level != "critical" {
		t.Errorf("level не эскалировался: %q", incidents[0].Level)
	}
}

func TestAggregator_ConcurrentAdd(t *testing.T) {
	agg := NewAggregator(DefaultNormalizer(), 1)
	ts := time.Now().UTC()
	const workers = 20
	const perWorker = 500
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				agg.Add(makeEntry("t5", "agent", "error", "m",
					// десятичное, чтобы попасть под правило `numbers`
					// и гарантированно схлопнуться в один fingerprint.
					fmt.Sprintf("attempt %d failed", i),
					ts.Add(time.Duration(i)*time.Microsecond)))
			}
		}(w)
	}
	wg.Wait()

	incidents := agg.IncidentsFor("t5")
	if len(incidents) != 1 {
		t.Fatalf("после concurrent add ожидали 1 инцидент, got %d", len(incidents))
	}
	if got := incidents[0].Count; got != workers*perWorker {
		t.Errorf("count: got=%d want=%d", got, workers*perWorker)
	}
}

func TestAggregator_SummaryNoiseSplit(t *testing.T) {
	agg := NewAggregator(DefaultNormalizer(), 1)
	ts := time.Now().UTC()
	// Группа "шумная": 10 одинаковых (после нормализации) сообщений.
	for i := 0; i < 10; i++ {
		agg.Add(makeEntry("t5", "a", "error", "m", "noisy order "+fmt.Sprint(i), ts))
	}
	// Группа "тихая": 2 таких же на другой шаблон.
	for i := 0; i < 2; i++ {
		agg.Add(makeEntry("t5", "a", "error", "m", "rare event "+fmt.Sprint(i), ts))
	}
	// Одиночка.
	agg.Add(makeEntry("t5", "a", "error", "m", "one-off", ts))

	s := agg.SummaryFor("t5", 5)
	if s.TotalIncidents != 3 {
		t.Errorf("TotalIncidents: got=%d want=3", s.TotalIncidents)
	}
	if s.TotalRecords != 13 {
		t.Errorf("TotalRecords: got=%d want=13", s.TotalRecords)
	}
	if len(s.Above) != 1 || s.Above[0].Count != 10 {
		t.Errorf("Above: %+v", s.Above)
	}
	if len(s.Below) != 2 || s.BelowRecords != 3 {
		t.Errorf("Below: %+v, rec=%d", s.Below, s.BelowRecords)
	}
}

func TestAggregator_SortsByCountDesc(t *testing.T) {
	agg := NewAggregator(DefaultNormalizer(), 1)
	ts := time.Now().UTC()
	// три разных шаблона с разным count.
	for i := 0; i < 5; i++ {
		agg.Add(makeEntry("t5", "a", "error", "m", "A error "+fmt.Sprint(i), ts))
	}
	for i := 0; i < 10; i++ {
		agg.Add(makeEntry("t5", "a", "error", "m", "B error "+fmt.Sprint(i), ts))
	}
	for i := 0; i < 3; i++ {
		agg.Add(makeEntry("t5", "a", "error", "m", "C error "+fmt.Sprint(i), ts))
	}
	incs := agg.IncidentsFor("t5")
	if len(incs) != 3 {
		t.Fatalf("incidents: got %d", len(incs))
	}
	if incs[0].Count < incs[1].Count || incs[1].Count < incs[2].Count {
		t.Errorf("sort wrong: %d %d %d", incs[0].Count, incs[1].Count, incs[2].Count)
	}
}

func TestAggregator_ExamplesDedupeSameString(t *testing.T) {
	agg := NewAggregator(DefaultNormalizer(), 5)
	ts := time.Now().UTC()
	for i := 0; i < 100; i++ {
		agg.Add(makeEntry("t5", "a", "error", "m", "same msg", ts))
	}
	inc := agg.IncidentsFor("t5")[0]
	if len(inc.Examples) != 1 {
		t.Errorf("ожидали 1 уникальный пример, got %d", len(inc.Examples))
	}
	if inc.Count != 100 {
		t.Errorf("count: %d", inc.Count)
	}
}

func TestAggregator_IncidentsFor_ReturnsCopies(t *testing.T) {
	agg := NewAggregator(DefaultNormalizer(), 1)
	ts := time.Now().UTC()
	agg.Add(makeEntry("t5", "a", "error", "m", "x", ts))

	incs := agg.IncidentsFor("t5")
	// мутация в caller'е не должна влиять на внутренний state
	incs[0].Count = 9999
	incs[0].Examples[0] = "MUTATED"

	fresh := agg.IncidentsFor("t5")
	if fresh[0].Count == 9999 {
		t.Error("IncidentsFor вернул внутреннюю ссылку — state можно мутировать")
	}
}

func TestLevelRank(t *testing.T) {
	if !(levelRank("critical") > levelRank("error")) {
		t.Error("critical > error")
	}
	if !(levelRank("error") > levelRank("warn")) {
		t.Error("error > warn")
	}
	if !(levelRank("warn") == levelRank("warning")) {
		t.Error("warn == warning")
	}
	if levelRank("unknown") != 0 {
		t.Error("неизвестный — 0")
	}
}
