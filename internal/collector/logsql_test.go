package collector

import (
	"strings"
	"testing"
	"time"
)

func TestStreamFilter_Build(t *testing.T) {
	cases := []struct {
		name string
		in   StreamFilter
		want string
	}{
		{"host only", StreamFilter{Host: "t5"}, `{host="t5"}`},
		{"host + one level", StreamFilter{Host: "t5", Levels: []string{"error"}}, `{host="t5",level="error"}`},
		{"host + two levels", StreamFilter{Host: "t5", Levels: []string{"error", "critical"}}, `{host="t5",level=~"error|critical"}`},
		{"levels only", StreamFilter{Levels: []string{"error"}}, `{level="error"}`},
		{"hyphen host", StreamFilter{Host: "ali-t1", Levels: []string{"error"}}, `{host="ali-t1",level="error"}`},
		{"empty", StreamFilter{}, `{}`},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := c.in.Build(); got != c.want {
				t.Errorf("got=%q want=%q", got, c.want)
			}
		})
	}
}

func TestQuery_Build(t *testing.T) {
	from := time.Date(2026, 4, 22, 5, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 23, 5, 0, 0, 0, time.UTC)

	cases := []struct {
		name     string
		q        Query
		contains []string
	}{
		{
			"minimal",
			Query{Stream: StreamFilter{Host: "t5", Levels: []string{"error", "critical"}}, From: from, To: to},
			[]string{
				`{host="t5",level=~"error|critical"}`,
				`_time:[2026-04-22T05:00:00Z, 2026-04-23T05:00:00Z)`,
			},
		},
		{
			"with extra",
			Query{Stream: StreamFilter{Host: "t5", Levels: []string{"error"}}, From: from, To: to, Extra: `app:="agent"`},
			[]string{
				`{host="t5",level="error"}`,
				`_time:[`,
				`app:="agent"`,
			},
		},
		{
			"with pipe auto pipe prefix",
			Query{Stream: StreamFilter{Host: "t5", Levels: []string{"error"}}, From: from, To: to, Pipe: `stats by (app) count()`},
			[]string{"| stats by (app) count()"},
		},
		{
			"with pipe already prefixed",
			Query{Stream: StreamFilter{Host: "t5", Levels: []string{"error"}}, From: from, To: to, Pipe: `| sort by (_time desc)`},
			[]string{"| sort by (_time desc)"},
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := c.q.Build()
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			for _, sub := range c.contains {
				if !strings.Contains(got, sub) {
					t.Errorf("missing %q in: %s", sub, got)
				}
			}
		})
	}
}

func TestQuery_Validate(t *testing.T) {
	from := time.Date(2026, 4, 22, 5, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)

	cases := []struct {
		name string
		q    Query
		want string
	}{
		{"no stream", Query{From: from, To: to}, "пустой стрим-фильтр"},
		{"no from", Query{Stream: StreamFilter{Host: "t5"}, To: to}, "From/To"},
		{"no to", Query{Stream: StreamFilter{Host: "t5"}, From: from}, "From/To"},
		{"from > to", Query{Stream: StreamFilter{Host: "t5"}, From: to, To: from}, "должно быть строго меньше"},
		{"from == to", Query{Stream: StreamFilter{Host: "t5"}, From: from, To: from}, "должно быть строго меньше"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := c.q.Validate()
			if err == nil {
				t.Fatal("ожидали ошибку")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("err=%q, want substring %q", err.Error(), c.want)
			}
		})
	}
}

func TestQuery_Build_RejectsInvalid(t *testing.T) {
	_, err := Query{}.Build()
	if err == nil {
		t.Fatal("ожидали ошибку на пустой Query")
	}
}
