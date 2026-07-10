// Package metrics exposes Prometheus instrumentation for the scanner worker.
package metrics

import (
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	once sync.Once

	ScanCyclesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kalshi_scan_cycles_total",
		Help: "Scan cycles by result",
	}, []string{"result"})

	ScanDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "kalshi_scan_duration_seconds",
		Help:    "Duration of a full scan cycle",
		Buckets: prometheus.ExponentialBuckets(0.5, 2, 12),
	})

	EventsFetched = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "kalshi_events_fetched",
		Help: "Open events fetched in the last scan cycle",
	})

	AlertsSent = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kalshi_alerts_sent_total",
		Help: "Alert digests sent",
	}, []string{"channel"})

	InboundProcessed = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kalshi_inbound_processed_total",
		Help: "Inbound messages processed",
	}, []string{"result"})

	KalshiAPILatency = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "kalshi_api_latency_seconds",
		Help:    "Kalshi API call latency",
		Buckets: prometheus.DefBuckets,
	}, []string{"endpoint"})

	LeaderGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "kalshi_scanner_leader",
		Help: "1 if this instance holds the scanner leader lock",
	})

	FilterReasons = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kalshi_filter_reasons_total",
		Help: "Markets filtered by reason",
	}, []string{"reason"})
)

// Register installs collectors with the default registry (idempotent).
func Register() {
	once.Do(func() {
		prometheus.MustRegister(
			ScanCyclesTotal, ScanDuration, EventsFetched, AlertsSent,
			InboundProcessed, KalshiAPILatency, LeaderGauge, FilterReasons,
		)
	})
}

// Handler returns the Prometheus scrape handler.
func Handler() http.Handler {
	Register()
	return promhttp.Handler()
}

// ObserveScan records one scan cycle.
func ObserveScan(duration time.Duration, events int, success bool) {
	Register()
	ScanDuration.Observe(duration.Seconds())
	EventsFetched.Set(float64(events))
	if success {
		ScanCyclesTotal.WithLabelValues("success").Inc()
	} else {
		ScanCyclesTotal.WithLabelValues("error").Inc()
	}
}
