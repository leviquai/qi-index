// Package metrics exposes the Prometheus instrumentation for the indexer.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	BlocksApplied = promauto.NewCounter(prometheus.CounterOpts{
		Name: "qiindex_blocks_applied_total",
		Help: "Blocks applied to the canonical chain.",
	})
	Reorgs = promauto.NewCounter(prometheus.CounterOpts{
		Name: "qiindex_reorgs_total",
		Help: "Reorgs performed.",
	})
	ReorgDepth = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "qiindex_reorg_depth",
		Help:    "Blocks rolled back per reorg.",
		Buckets: []float64{1, 2, 3, 5, 8, 13, 21, 32},
	})
	TipHeight = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "qiindex_tip_height",
		Help: "Current canonical tip height.",
	})
	RPCErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "qiindex_rpc_errors_total",
		Help: "Failed RPC operations (fetch or ingest).",
	})
	WSReconnects = promauto.NewCounter(prometheus.CounterOpts{
		Name: "qiindex_ws_reconnects_total",
		Help: "WebSocket reconnect attempts.",
	})
)

// Serve exposes /metrics and /healthz on addr; it blocks.
func Serve(addr string) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	return http.ListenAndServe(addr, mux)
}
