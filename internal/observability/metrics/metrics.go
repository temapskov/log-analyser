// Package metrics — собственный Prometheus-registry + набор метрик для
// digest cycle. Custom registry (не default) — чтобы /metrics-endpoint не
// протекал через глобальное состояние и легко тестировался (FR-19 / ADR-0005).
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Metrics — инструменты, которые pipeline/scheduler вызывают при работе.
type Metrics struct {
	Registry *prometheus.Registry

	DigestCycleTotal           *prometheus.CounterVec
	DigestCycleDurationSeconds prometheus.Histogram
	DigestCyclePartialErrors   prometheus.Counter
	LastSuccessfulDeliveryTS   prometheus.Gauge
	HostRecordsTotal           *prometheus.CounterVec
	HostIncidentsLastCycle     *prometheus.GaugeVec
	ReadyzCheckDurationSeconds *prometheus.HistogramVec
	ReadyzCheckStatus          *prometheus.GaugeVec
}

// New создаёт полный набор метрик и registry с стандартными коллекторами
// (Go runtime + process + build info). Ничего не регистрирует глобально.
func New(version, commit string) *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		collectors.NewBuildInfoCollector(),
	)

	m := &Metrics{Registry: reg}

	m.DigestCycleTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "log_analyser_digest_cycle_total",
			Help: "Число digest-циклов, сгруппированных по статусу.",
		}, []string{"status"}, // ok | failed | skipped
	)
	m.DigestCycleDurationSeconds = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name: "log_analyser_digest_cycle_duration_seconds",
			Help: "Длительность одного digest cycle в секундах.",
			// Охватываем от быстрых (миллисекунды — idempotent skip) до
			// тяжёлых прогонов (до часа).
			Buckets: []float64{0.1, 0.5, 1, 5, 10, 30, 60, 120, 300, 600, 1200, 3600},
		})
	m.DigestCyclePartialErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "log_analyser_digest_cycle_partial_errors_total",
		Help: "Число partial-фейлов по хостам (один хост упал, остальные доставлены).",
	})
	m.LastSuccessfulDeliveryTS = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "log_analyser_last_successful_delivery_timestamp_seconds",
		Help: "Unix-время последнего успешного digest cycle. 0 до первого успеха.",
	})
	m.HostRecordsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "log_analyser_host_records_total",
			Help: "Cумма собранных записей по хостам и уровням.",
		}, []string{"host", "level"},
	)
	m.HostIncidentsLastCycle = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "log_analyser_host_incidents_last_cycle",
			Help: "Число уникальных инцидентов на хосте в последнем завершённом цикле.",
		}, []string{"host"},
	)
	m.ReadyzCheckDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "log_analyser_readyz_check_duration_seconds",
			Help:    "Длительность readyz-проверки по типу.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 2, 5, 10},
		}, []string{"check"},
	)
	m.ReadyzCheckStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "log_analyser_readyz_check_status",
			Help: "Статус последней readyz-проверки: 1 ok, 0 fail.",
		}, []string{"check"},
	)

	reg.MustRegister(
		m.DigestCycleTotal,
		m.DigestCycleDurationSeconds,
		m.DigestCyclePartialErrors,
		m.LastSuccessfulDeliveryTS,
		m.HostRecordsTotal,
		m.HostIncidentsLastCycle,
		m.ReadyzCheckDurationSeconds,
		m.ReadyzCheckStatus,
	)
	// build_info — кастомный (NewBuildInfoCollector отражает Go-версию и
	// модуль, а нам нужно version/commit из ldflags).
	buildInfo := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "log_analyser_build_info",
		Help: "Константа 1 с label'ами version/commit/go_version (см. internal/version).",
	}, []string{"version", "commit"})
	reg.MustRegister(buildInfo)
	buildInfo.WithLabelValues(version, commit).Set(1)

	return m
}
