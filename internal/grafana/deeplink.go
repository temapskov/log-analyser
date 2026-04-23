// Package grafana собирает deeplink'и в Grafana Explore по схеме
// `schemaVersion=1 + orgId + panes=<JSON>`, подтверждённой через Context7
// (см. docs/plans/00-analysis.md §3.3 и §15.3).
//
// Контракт: входные данные — детерминированы, функция чистая, не делает
// сетевых вызовов. Проверка работоспособности URL — задача интеграционного
// теста (internal/grafana/deeplink_integration_test.go, build-tag `integration`)
// и /readyz daemon'а (в feat/observability).
package grafana

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// Config — параметры целевой Grafana и datasource. Заполняется из ENV
// (GRAFANA_URL, GRAFANA_ORG_ID, GRAFANA_VL_DS_UID, GRAFANA_VL_DS_TYPE)
// в internal/config.Config.
type Config struct {
	BaseURL string
	OrgID   int
	DSUID   string
	DSType  string
}

// Validate — явная проверка полноты перед использованием. Нужна, чтобы
// ранние тесты и health-чек давали осмысленную ошибку вместо кривого URL.
func (c Config) Validate() error {
	var errs []error
	if strings.TrimSpace(c.BaseURL) == "" {
		errs = append(errs, errors.New("BaseURL пуст"))
	} else if u, err := url.Parse(c.BaseURL); err != nil || u.Scheme == "" || u.Host == "" {
		errs = append(errs, fmt.Errorf("BaseURL=%q не похож на URL", c.BaseURL))
	}
	if c.OrgID < 1 {
		errs = append(errs, fmt.Errorf("OrgID=%d: должно быть ≥ 1", c.OrgID))
	}
	if strings.TrimSpace(c.DSUID) == "" {
		errs = append(errs, errors.New("DSUID пуст"))
	}
	if strings.TrimSpace(c.DSType) == "" {
		errs = append(errs, errors.New("DSType пуст"))
	}
	return errors.Join(errs...)
}

// paneQuery — один запрос внутри панели Explore. Имена и порядок полей
// критичны: Grafana парсит JSON структурно.
type paneQuery struct {
	RefID      string            `json:"refId"`
	Datasource paneDatasourceRef `json:"datasource"`
	Expr       string            `json:"expr"`
	QueryType  string            `json:"queryType,omitempty"`
}

type paneDatasourceRef struct {
	UID  string `json:"uid"`
	Type string `json:"type"`
}

type paneRange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// pane — состояние одной панели Explore (ключ вроде "a" в panes-словаре).
type pane struct {
	Datasource string      `json:"datasource"`
	Queries    []paneQuery `json:"queries"`
	Range      paneRange   `json:"range"`
}

// ExploreURL собирает ссылку вида:
//
//	<BaseURL>/explore?schemaVersion=1&orgId=<N>&panes=<url-encoded-json>
//
// expr — LogsQL (напр., `{host="t5"} (level:=error OR level:=critical)`),
// from/to — границы окна (включительно слева, исключительно справа в VL,
// но Grafana здесь не различает — это просто временной диапазон UI).
//
// Если BaseURL содержит path-префикс (например, грэфана за reverse-proxy'ём
// на /grafana/), он сохраняется.
func (c Config) ExploreURL(expr string, from, to time.Time) (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	if strings.TrimSpace(expr) == "" {
		return "", errors.New("expr пуст")
	}
	if from.IsZero() || to.IsZero() {
		return "", errors.New("from/to нулевые — передайте конкретное окно")
	}
	if !from.Before(to) {
		return "", fmt.Errorf("from (%s) должно быть строго меньше to (%s)", from.Format(time.RFC3339), to.Format(time.RFC3339))
	}

	p := pane{
		Datasource: c.DSUID,
		Queries: []paneQuery{{
			RefID:      "A",
			Datasource: paneDatasourceRef{UID: c.DSUID, Type: c.DSType},
			Expr:       expr,
		}},
		Range: paneRange{
			From: strconv.FormatInt(from.UnixMilli(), 10),
			To:   strconv.FormatInt(to.UnixMilli(), 10),
		},
	}

	panes := map[string]pane{"a": p}
	j, err := json.Marshal(panes)
	if err != nil {
		return "", fmt.Errorf("marshal panes: %w", err)
	}

	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return "", fmt.Errorf("parse BaseURL: %w", err)
	}
	// path.Join убирает двойные слэши и сохраняет префикс (напр. /grafana).
	u.Path = path.Join(u.Path, "explore")

	q := u.Query()
	q.Set("schemaVersion", "1")
	q.Set("orgId", strconv.Itoa(c.OrgID))
	q.Set("panes", string(j))
	u.RawQuery = q.Encode()

	return u.String(), nil
}

// AllHostsExpr — LogsQL-выражение для множества хостов и уровней.
// Пример: {host=~"t1|ali-t1|t2|aws-t3|t5"} (level:=error OR level:=critical).
// Если hosts пуст — пустой стрим-фильтр (не должно случиться в проде).
func AllHostsExpr(hosts, levels []string) string {
	expr := "{}"
	if len(hosts) > 0 {
		expr = fmt.Sprintf(`{host=~"%s"}`, strings.Join(hosts, "|"))
	}
	if len(levels) == 0 {
		return expr
	}
	parts := make([]string, 0, len(levels))
	for _, lvl := range levels {
		lvl = strings.TrimSpace(lvl)
		if lvl == "" {
			continue
		}
		parts = append(parts, "level:="+lvl)
	}
	if len(parts) == 0 {
		return expr
	}
	if len(parts) == 1 {
		return expr + " " + parts[0]
	}
	return expr + " (" + strings.Join(parts, " OR ") + ")"
}

// HostExpr — удобный хелпер: собирает LogsQL-выражение для конкретного
// хоста и списка уровней. Используется render'ом (feat/render) и
// observability'ю (health). Не включает _time — окно задаётся параметрами
// from/to в ExploreURL (UI сам его применит).
//
// Пример: HostExpr("t5", []string{"error","critical"}) →
//
//	{host="t5"} (level:=error OR level:=critical)
//
// Если уровней нет — возвращает только стрим-фильтр.
//
// NB: OQ-5 (host/app/level — поля стрима или логов?) ещё не закрыт.
// Если окажется, что level не поле стрима — сменить на `level:=...` без {}.
func HostExpr(host string, levels []string) string {
	base := fmt.Sprintf(`{host=%q}`, host)
	if len(levels) == 0 {
		return base
	}
	parts := make([]string, 0, len(levels))
	for _, lvl := range levels {
		lvl = strings.TrimSpace(lvl)
		if lvl == "" {
			continue
		}
		parts = append(parts, "level:="+lvl)
	}
	if len(parts) == 0 {
		return base
	}
	if len(parts) == 1 {
		return base + " " + parts[0]
	}
	return base + " (" + strings.Join(parts, " OR ") + ")"
}
