//go:build integration

// Smoke-тест против реальной Grafana. Запуск:
//
//	make test-integration
//
// или (с загруженным .env):
//
//	set -a; . ./.env; set +a
//	go test -tags=integration -run Integration ./internal/grafana/...
//
// Если обязательные env не заданы — тест скипается, не падает.
// Если нужен bearer-токен — выставить GRAFANA_TOKEN; без него ожидаем
// 302 на /login (что тоже означает «сервер жив, URL корректной формы»).
package grafana

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestExploreURL_Integration_HEAD(t *testing.T) {
	base := os.Getenv("GRAFANA_URL")
	uid := os.Getenv("GRAFANA_VL_DS_UID")
	dsType := os.Getenv("GRAFANA_VL_DS_TYPE")
	if base == "" || uid == "" || dsType == "" {
		t.Skip("GRAFANA_URL / GRAFANA_VL_DS_UID / GRAFANA_VL_DS_TYPE не заданы — скип")
	}
	orgID, _ := strconv.Atoi(os.Getenv("GRAFANA_ORG_ID"))
	if orgID == 0 {
		orgID = 1
	}

	c := Config{
		BaseURL: base,
		OrgID:   orgID,
		DSUID:   uid,
		DSType:  dsType,
	}
	from := time.Now().Add(-24 * time.Hour).UTC()
	to := time.Now().UTC()
	u, err := c.ExploreURL(HostExpr("t5", []string{"error", "critical"}), from, to)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Logf("URL: %s", u)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	if tok := os.Getenv("GRAFANA_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	// Не следуем за редиректами: 302 на /login — валидный сигнал,
	// что Grafana жива и URL-форма корректна (auth проверяется ДО
	// рендера Explore).
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("HEAD %s: %v", u, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK,
		http.StatusFound,             // 302 — типично без auth
		http.StatusSeeOther,          // 303
		http.StatusTemporaryRedirect, // 307
		http.StatusPermanentRedirect, // 308
		http.StatusUnauthorized,      // 401 — auth required
		http.StatusForbidden:         // 403 — auth required (иначе)
		t.Logf("OK: status=%d", resp.StatusCode)
	default:
		t.Fatalf("неожиданный статус %d для %s", resp.StatusCode, u)
	}
}
