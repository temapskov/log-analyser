// Package pipeline — оркестратор digest cycle.
//
// Одна Pipeline.Run() исполняет всю цепочку: collect (per-host параллельно) →
// dedup → render (md-файл на хост + cover HTML) → deliver (cover в TG +
// sendMediaGroup или fallback на sendDocument×5).
//
// Идемпотентность (FR-12) ЗДЕСЬ НЕ ОБЕСПЕЧИВАЕТСЯ — это задача state-слоя
// (ADR-0002, следующий PR). Поэтому повторный Run на одно и то же окно
// доставит дубль. CLI `once` принимает это как осознанный выбор оператора.
package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/QCoreTech/log_analyser/internal/collector"
	"github.com/QCoreTech/log_analyser/internal/dedup"
	"github.com/QCoreTech/log_analyser/internal/grafana"
	"github.com/QCoreTech/log_analyser/internal/render"
	"github.com/QCoreTech/log_analyser/internal/state"
	"github.com/QCoreTech/log_analyser/internal/telegram"
)

// Config — параметры digest cycle.
type Config struct {
	Hosts            []string
	Levels           []string
	NoiseK           int
	TopN             int
	MaxExamples      int
	ReportsDir       string
	ReportExt        string // "md" | "html" | "txt"
	TZ               *time.Location
	HostLabels       map[string]string
	ChatID           int64
	ParseMode        string       // "HTML" по умолчанию
	FingerprintRules []dedup.Rule // nil → DefaultRules
}

// Dependencies — зависимости, инжектируются из main.
type Dependencies struct {
	VL       *collector.Client
	TG       *telegram.Client
	Grafana  grafana.Config
	Renderer *render.Renderer
	// State — опционально; если nil, идемпотентность (FR-12) НЕ
	// обеспечивается (повторный Run на одно окно отправит дубль).
	State  *state.Store
	Logger *slog.Logger
}

// Pipeline — immutable после New.
type Pipeline struct {
	cfg  Config
	deps Dependencies
}

// New валидирует конфиг и возвращает готовый Pipeline.
func New(cfg Config, deps Dependencies) (*Pipeline, error) {
	if len(cfg.Hosts) == 0 {
		return nil, errors.New("Hosts пуст")
	}
	if len(cfg.Levels) == 0 {
		return nil, errors.New("Levels пуст")
	}
	if cfg.NoiseK < 1 {
		cfg.NoiseK = 1
	}
	if cfg.TopN < 1 {
		cfg.TopN = 20
	}
	if cfg.MaxExamples < 1 {
		cfg.MaxExamples = 3
	}
	if cfg.ReportExt == "" {
		cfg.ReportExt = "md"
	}
	if cfg.TZ == nil {
		loc, err := time.LoadLocation("Europe/Moscow")
		if err != nil {
			return nil, err
		}
		cfg.TZ = loc
	}
	if cfg.ParseMode == "" {
		cfg.ParseMode = "HTML"
	}
	if deps.VL == nil || deps.TG == nil || deps.Renderer == nil {
		return nil, errors.New("VL/TG/Renderer обязательны")
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return &Pipeline{cfg: cfg, deps: deps}, nil
}

// Result — итог одного Run.
type Result struct {
	Window      Window
	PerHost     map[string]HostResult
	CoverMsgID  int64
	MediaMsgIDs []int64
	Errors      []error // партиальные ошибки; не-nil Run по-прежнему возможен
}

type Window struct{ From, To time.Time }

type HostResult struct {
	Host          string
	TotalError    uint64
	TotalCritical uint64
	TotalRecords  uint64
	Incidents     int
	FilePath      string // путь к сохранённому .md
	Err           error  // ошибка конкретно по этому хосту (остальные могут пройти)
}

// Run исполняет один digest cycle за заданное окно.
//
// Поведение при ошибках:
//   - Per-host collect/render изолирован: падение одного хоста не валит
//     весь Run (NFR-R4). Ошибка запоминается в HostResult.Err.
//   - Если падают все хосты или рендер пустой — cover не отправляется,
//     Run возвращает ошибку.
//   - Cover/delivery ошибки — фатальны для Run (отчёт не доставлен).
func (p *Pipeline) Run(ctx context.Context, window Window) (*Result, error) {
	if !window.From.Before(window.To) {
		return nil, fmt.Errorf("window From (%s) must be < To (%s)",
			window.From.Format(time.RFC3339), window.To.Format(time.RFC3339))
	}

	// Идемпотентность через state (FR-12). Если store не задан — пропускаем
	// (режим `once` для оператора, осознанная потеря idempotency).
	var runID string
	if p.deps.State != nil {
		id, resumed, err := p.deps.State.BeginRun(ctx, window.From, window.To)
		if errors.Is(err, state.ErrAlreadyDelivered) {
			p.deps.Logger.Info("digest за окно уже доставлен — пропуск",
				slog.String("run_id", id),
				slog.Time("from", window.From),
				slog.Time("to", window.To),
			)
			return &Result{Window: window, PerHost: map[string]HostResult{}}, nil
		}
		if err != nil {
			return nil, fmt.Errorf("state BeginRun: %w", err)
		}
		runID = id
		if resumed {
			p.deps.Logger.Info("возобновляем незавершённый run",
				slog.String("run_id", runID))
		}
	}

	rules := p.cfg.FingerprintRules
	if rules == nil {
		rules = dedup.DefaultRules()
	}
	agg := dedup.NewAggregator(dedup.NewNormalizer(rules), p.cfg.MaxExamples)

	type collected struct {
		host          string
		totalRecords  uint64
		totalError    uint64
		totalCritical uint64
		err           error
	}
	results := make(chan collected, len(p.cfg.Hosts))

	// 1. Параллельный collect.
	var wg sync.WaitGroup
	for _, host := range p.cfg.Hosts {
		wg.Add(1)
		go func(host string) {
			defer wg.Done()
			var r collected
			r.host = host
			q := collector.Query{
				Stream: collector.StreamFilter{Host: host, Levels: p.cfg.Levels},
				From:   window.From, To: window.To,
			}
			err := p.deps.VL.StreamQuery(ctx, q, func(e collector.LogEntry) error {
				agg.Add(e)
				r.totalRecords++
				switch e.Level {
				case "error":
					r.totalError++
				case "critical":
					r.totalCritical++
				}
				return nil
			})
			r.err = err
			results <- r
		}(host)
	}
	wg.Wait()
	close(results)

	perHostStats := map[string]collected{}
	var partial []error
	for r := range results {
		if r.err != nil {
			partial = append(partial, fmt.Errorf("host=%s: %w", r.host, r.err))
			p.deps.Logger.Error("pipeline collect failed",
				slog.String("host", r.host), slog.String("err", r.err.Error()))
		}
		perHostStats[r.host] = r
	}

	// 2. Рендер per-host файлов.
	if err := os.MkdirAll(p.cfg.ReportsDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", p.cfg.ReportsDir, err)
	}
	date := window.To.In(p.cfg.TZ).Format("2006-01-02")
	res := &Result{
		Window:  window,
		PerHost: map[string]HostResult{},
		Errors:  partial,
	}
	var coverRows []render.CoverHostRow
	var totalErrAll, totalCritAll uint64
	var totalIncidents int
	var attachments []telegram.Attachment

	for _, host := range p.cfg.Hosts {
		stats := perHostStats[host]
		hostRes := HostResult{
			Host:          host,
			TotalError:    stats.totalError,
			TotalCritical: stats.totalCritical,
			TotalRecords:  stats.totalRecords,
			Incidents:     len(agg.IncidentsFor(host)),
			Err:           stats.err,
		}
		hostDL, err := p.deps.Grafana.ExploreURL(
			grafana.HostExpr(host, p.cfg.Levels), window.From, window.To)
		if err != nil {
			hostRes.Err = fmt.Errorf("deeplink: %w", err)
		}

		incs := agg.IncidentsFor(host)
		var top []render.IncidentView
		var below []render.IncidentView
		var belowRecords uint64
		for _, inc := range incs {
			dl, derr := p.incidentDeeplink(host, inc, window)
			if derr != nil {
				p.deps.Logger.Warn("incident deeplink failed",
					slog.String("host", host), slog.String("err", derr.Error()))
			}
			v := render.IncidentView{Incident: inc, Deeplink: dl}
			if inc.Count >= uint64(p.cfg.NoiseK) && len(top) < p.cfg.TopN {
				top = append(top, v)
			} else if inc.Count < uint64(p.cfg.NoiseK) {
				below = append(below, v)
				belowRecords += inc.Count
			}
		}

		report := render.HostReport{
			Host:           host,
			Label:          p.cfg.HostLabels[host],
			TZ:             p.cfg.TZ.String(),
			Window:         render.Window{From: window.From, To: window.To},
			GeneratedAt:    time.Now().In(p.cfg.TZ),
			TotalError:     stats.totalError,
			TotalCritical:  stats.totalCritical,
			TotalRecords:   stats.totalRecords,
			TotalIncidents: len(incs),
			AppTotals:      agg.AppTotalsFor(host),
			TopIncidents:   top,
			BelowIncidents: below,
			BelowRecords:   belowRecords,
			TopN:           len(top),
			NoiseK:         p.cfg.NoiseK,
			HostDeeplink:   hostDL,
		}
		path := filepath.Join(p.cfg.ReportsDir, fmt.Sprintf("%s_%s.%s", host, date, p.cfg.ReportExt))
		f, err := os.Create(path)
		if err != nil {
			hostRes.Err = fmt.Errorf("create file: %w", err)
			res.PerHost[host] = hostRes
			continue
		}
		if err := p.deps.Renderer.RenderHost(f, report); err != nil {
			f.Close()
			hostRes.Err = fmt.Errorf("render: %w", err)
			res.PerHost[host] = hostRes
			continue
		}
		f.Close()
		hostRes.FilePath = path

		res.PerHost[host] = hostRes
		totalErrAll += stats.totalError
		totalCritAll += stats.totalCritical
		totalIncidents += len(incs)

		topApp := ""
		if tot := agg.AppTotalsFor(host); len(tot) > 0 {
			topApp = tot[0].App
		}
		coverRows = append(coverRows, render.CoverHostRow{
			Host:          host,
			TotalError:    stats.totalError,
			TotalCritical: stats.totalCritical,
			TopApp:        topApp,
		})

		// Открываем файл заново для upload'а в TG.
		rf, err := os.Open(path)
		if err != nil {
			hostRes.Err = fmt.Errorf("open for upload: %w", err)
			res.PerHost[host] = hostRes
			continue
		}
		attachments = append(attachments, telegram.Attachment{
			Filename: filepath.Base(path),
			Content:  rf,
		})
	}
	// Если ни одного файла не удалось сгенерировать — фатал.
	if len(attachments) == 0 {
		return res, fmt.Errorf("нет файлов для отправки (все хосты упали): %w", errors.Join(partial...))
	}

	// 3. Cover.
	allExpr := grafana.AllHostsExpr(p.cfg.Hosts, p.cfg.Levels)
	allDL, _ := p.deps.Grafana.ExploreURL(allExpr, window.From, window.To)
	var coverBuf bytes.Buffer
	err := p.deps.Renderer.RenderCover(&coverBuf, render.CoverData{
		TZ:               p.cfg.TZ.String(),
		Window:           render.Window{From: window.From, To: window.To},
		Hosts:            coverRows,
		TotalError:       totalErrAll,
		TotalCritical:    totalCritAll,
		TotalIncidents:   totalIncidents,
		AllHostsDeeplink: allDL,
		PartialDelivery:  len(partial) > 0,
	})
	if err != nil {
		closeAttachments(attachments)
		return res, fmt.Errorf("render cover: %w", err)
	}

	coverMsg, err := p.deps.TG.SendMessage(ctx, telegram.SendMessageParams{
		ChatID:                p.cfg.ChatID,
		Text:                  coverBuf.String(),
		ParseMode:             p.cfg.ParseMode,
		DisableWebPagePreview: true,
	})
	if err != nil {
		closeAttachments(attachments)
		p.markFailed(ctx, runID, fmt.Sprintf("cover: %v", err))
		return res, fmt.Errorf("send cover: %w", err)
	}
	res.CoverMsgID = coverMsg.MessageID
	if runID != "" && p.deps.State != nil {
		if err := p.deps.State.MarkCoverSent(ctx, runID, coverMsg.MessageID); err != nil {
			p.deps.Logger.Warn("state MarkCoverSent failed",
				slog.String("err", err.Error()))
		}
	}

	// 4. Media group (или fallback).
	msgs, err := p.sendFiles(ctx, attachments)
	closeAttachments(attachments)
	if err != nil {
		p.markFailed(ctx, runID, fmt.Sprintf("files: %v", err))
		return res, fmt.Errorf("send files: %w", err)
	}
	for _, m := range msgs {
		res.MediaMsgIDs = append(res.MediaMsgIDs, m.MessageID)
	}

	if runID != "" && p.deps.State != nil {
		if err := p.deps.State.FinishRun(ctx, runID, len(msgs)); err != nil {
			p.deps.Logger.Warn("state FinishRun failed",
				slog.String("err", err.Error()))
		}
	}

	p.deps.Logger.Info("digest cycle ok",
		slog.String("run_id", runID),
		slog.Int64("cover_msg_id", coverMsg.MessageID),
		slog.Int("files", len(attachments)),
		slog.Int("partial_errors", len(partial)),
	)
	return res, nil
}

// markFailed — запись status=failed в state. Безопасна при nil-State/пустом runID.
func (p *Pipeline) markFailed(ctx context.Context, runID, msg string) {
	if runID == "" || p.deps.State == nil {
		return
	}
	// Используем фоновый контекст: даже если caller'ский ctx отменён,
	// мы хотим записать причину фейла.
	bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = ctx // ctx тут справочно
	if err := p.deps.State.FailRun(bg, runID, msg); err != nil {
		p.deps.Logger.Warn("state FailRun failed", slog.String("err", err.Error()))
	}
}

// sendFiles пробует sendMediaGroup; если документов меньше 2 или TG вернул
// любую ошибку — fallback на N-штучный sendDocument.
func (p *Pipeline) sendFiles(ctx context.Context, docs []telegram.Attachment) ([]telegram.Message, error) {
	if len(docs) >= 2 {
		msgs, err := p.deps.TG.SendMediaGroup(ctx, telegram.SendMediaGroupParams{
			ChatID:    p.cfg.ChatID,
			Documents: docs,
		})
		if err == nil {
			return msgs, nil
		}
		p.deps.Logger.Warn("sendMediaGroup failed, fallback на sendDocument",
			slog.String("err", err.Error()))
		// Для fallback нужно перечитать файлы — caller закрывает
		// предыдущие, мы открываем заново.
		if err := reopenAttachments(docs); err != nil {
			return nil, fmt.Errorf("reopen for fallback: %w (orig: %v)", err, err)
		}
	}
	var msgs []telegram.Message
	for i, d := range docs {
		m, err := p.deps.TG.SendDocument(ctx, telegram.SendDocumentParams{
			ChatID: p.cfg.ChatID,
			Doc:    d,
		})
		if err != nil {
			return msgs, fmt.Errorf("sendDocument[%d] %s: %w", i, d.Filename, err)
		}
		msgs = append(msgs, *m)
	}
	return msgs, nil
}

func (p *Pipeline) incidentDeeplink(host string, inc *dedup.Incident, w Window) (string, error) {
	expr := grafana.HostExpr(host, []string{inc.Level})
	if inc.App != "" {
		expr += fmt.Sprintf(` app:=%q`, inc.App)
	}
	from := inc.FirstSeen.Add(-time.Minute)
	to := inc.LastSeen.Add(time.Minute)
	if from.Before(w.From) {
		from = w.From
	}
	if to.After(w.To) {
		to = w.To
	}
	if !from.Before(to) {
		from, to = w.From, w.To
	}
	return p.deps.Grafana.ExploreURL(expr, from, to)
}

// closeAttachments — все Content, реализующие io.Closer, закрываются.
// Безопасно вызывать дважды (идёт через type assertion).
func closeAttachments(docs []telegram.Attachment) {
	for _, d := range docs {
		if c, ok := d.Content.(interface{ Close() error }); ok {
			_ = c.Close()
		}
	}
}

// reopenAttachments — для fallback re-читает файлы с диска, если Content
// был *os.File. Для других Reader'ов — недоступно (unit-тесты).
func reopenAttachments(docs []telegram.Attachment) error {
	for i, d := range docs {
		f, ok := d.Content.(*os.File)
		if !ok {
			return fmt.Errorf("docs[%d]: невозможно перечитать (не файл)", i)
		}
		fresh, err := os.Open(f.Name())
		if err != nil {
			return fmt.Errorf("reopen %s: %w", f.Name(), err)
		}
		docs[i].Content = fresh
	}
	return nil
}
