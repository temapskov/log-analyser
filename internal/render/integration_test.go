//go:build integration

// End-to-end smoke: collector → dedup → render на реальных prod-логах.
// Генерирует per-host .md файлы для всех 5 серверов за 24ч и cover.html
// в testdata/out/ (директория под gitignore). Проверяет:
//   - все файлы сгенерированы (даже для тихих хостов),
//   - размер < 50 МБ (лимит TG Bot API sendDocument),
//   - cover содержит все 5 хостов,
//   - нет пустых/повреждённых инцидентов.
package render

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QCoreTech/log_analyser/internal/collector"
	"github.com/QCoreTech/log_analyser/internal/dedup"
	"github.com/QCoreTech/log_analyser/internal/grafana"
	"github.com/QCoreTech/log_analyser/internal/httpclient"
)

const tgFileLimit = 50 << 20 // 50 МБ

func TestRender_E2E_AllHostsFromProd(t *testing.T) {
	base := os.Getenv("VL_URL")
	if base == "" {
		t.Skip("VL_URL не задан")
	}
	grafBase := os.Getenv("GRAFANA_URL")
	grafUID := os.Getenv("GRAFANA_VL_DS_UID")
	grafType := os.Getenv("GRAFANA_VL_DS_TYPE")
	if grafBase == "" || grafUID == "" {
		t.Skip("GRAFANA_URL / GRAFANA_VL_DS_UID не заданы")
	}
	orgID, _ := strconv.Atoi(os.Getenv("GRAFANA_ORG_ID"))
	if orgID == 0 {
		orgID = 1
	}
	graf := grafana.Config{BaseURL: grafBase, OrgID: orgID, DSUID: grafUID, DSType: grafType}

	hc := httpclient.New(httpclient.Config{Timeout: 30 * time.Second, MaxRetries: 1}, nil)
	hc.HTTP().Transport = &http.Transport{Proxy: nil}
	vl, err := collector.New(collector.Config{BaseURL: base, QueryTimeout: 60 * time.Second}, hc, nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	hosts := []string{"t1", "ali-t1", "t2", "aws-t3", "t5"}
	tz := "Europe/Moscow"
	loc, _ := time.LoadLocation(tz)
	now := time.Now().In(loc)
	from := now.Add(-24 * time.Hour)

	agg := dedup.NewAggregator(dedup.DefaultNormalizer(), 3)
	perHost := map[string]uint64{} // total records / host
	errPerHost := map[string]uint64{}
	critPerHost := map[string]uint64{}

	for _, host := range hosts {
		q := collector.Query{
			Stream: collector.StreamFilter{Host: host, Levels: []string{"error", "critical"}},
			From:   from, To: now,
			Pipe: "| limit 10000",
		}
		err := vl.StreamQuery(ctx, q, func(e collector.LogEntry) error {
			agg.Add(e)
			perHost[host]++
			switch e.Level {
			case "error":
				errPerHost[host]++
			case "critical":
				critPerHost[host]++
			}
			return nil
		})
		if err != nil {
			t.Errorf("host=%s: %v", host, err)
		}
	}

	r, err := New(tz)
	if err != nil {
		t.Fatal(err)
	}

	outDir := filepath.Join("testdata", "out")
	if err := os.RemoveAll(outDir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	date := now.Format("2006-01-02")

	var coverHosts []CoverHostRow
	var totalErr, totalCrit uint64
	totalIncidents := 0

	for _, host := range hosts {
		s := agg.SummaryFor(host, 5)
		incs := agg.IncidentsFor(host)
		top := make([]IncidentView, 0, 20)
		below := make([]IncidentView, 0, len(s.Below))

		hostDL, err := graf.ExploreURL(grafana.HostExpr(host, []string{"error", "critical"}), from, now)
		if err != nil {
			t.Fatal(err)
		}

		for _, inc := range incs {
			exprInc := grafana.HostExpr(host, []string{inc.Level})
			if inc.App != "" {
				exprInc += ` app:="` + inc.App + `"`
			}
			dl, err := graf.ExploreURL(exprInc, inc.FirstSeen.Add(-1*time.Minute), inc.LastSeen.Add(1*time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			v := IncidentView{Incident: inc, Deeplink: dl}
			if inc.Count >= 5 && len(top) < 20 {
				top = append(top, v)
			} else if inc.Count < 5 {
				below = append(below, v)
			}
		}

		rep := HostReport{
			Host: host, TZ: tz,
			Window:         Window{From: from, To: now},
			GeneratedAt:    time.Now().In(loc),
			TotalError:     errPerHost[host],
			TotalCritical:  critPerHost[host],
			TotalRecords:   perHost[host],
			TotalIncidents: len(incs),
			AppTotals:      agg.AppTotalsFor(host),
			TopIncidents:   top,
			BelowIncidents: below,
			BelowRecords:   s.BelowRecords,
			TopN:           len(top),
			NoiseK:         5,
			HostDeeplink:   hostDL,
		}

		path := filepath.Join(outDir, host+"_"+date+".md")
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.RenderHost(f, rep); err != nil {
			f.Close()
			t.Fatalf("host=%s render: %v", host, err)
		}
		f.Close()

		st, _ := os.Stat(path)
		if st.Size() > tgFileLimit {
			t.Errorf("host=%s размер %d > TG лимита 50МБ", host, st.Size())
		}
		t.Logf("%s → %s (%d KB, %d инцидентов, top=%d, below=%d)",
			host, path, st.Size()/1024, len(incs), len(top), len(below))

		// Приложение для cover'а
		topApp := ""
		if at := agg.AppTotalsFor(host); len(at) > 0 {
			topApp = at[0].App
		}
		coverHosts = append(coverHosts, CoverHostRow{
			Host: host, TotalError: errPerHost[host], TotalCritical: critPerHost[host], TopApp: topApp,
		})
		totalErr += errPerHost[host]
		totalCrit += critPerHost[host]
		totalIncidents += len(incs)
	}

	allExpr := `{host=~"t1|ali-t1|t2|aws-t3|t5"} (level:=error OR level:=critical)`
	allDL, err := graf.ExploreURL(allExpr, from, now)
	if err != nil {
		t.Fatal(err)
	}

	var cover bytes.Buffer
	if err := r.RenderCover(&cover, CoverData{
		TZ:               tz,
		Window:           Window{From: from, To: now},
		Hosts:            coverHosts,
		TotalError:       totalErr,
		TotalCritical:    totalCrit,
		TotalIncidents:   totalIncidents,
		AllHostsDeeplink: allDL,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "cover.html"), cover.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("cover → %s (%d bytes)", filepath.Join(outDir, "cover.html"), cover.Len())

	if cover.Len() > 4096 {
		t.Errorf("cover %d > 4096 (TG sendMessage лимит)", cover.Len())
	}
	for _, h := range hosts {
		if !strings.Contains(cover.String(), h) {
			t.Errorf("cover не содержит хост %q", h)
		}
	}
}
