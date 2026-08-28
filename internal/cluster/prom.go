package cluster

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
)

var (
	metricsRegistry = prometheus.NewRegistry()

	nodeExecutionSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "sparkwing_node_execution_seconds",
			Help:    "Wall time for a single node from claim-accepted to terminal state.",
			Buckets: []float64{0.25, 0.5, 1, 5, 10, 30, 60, 300, 900},
		},
		[]string{"pipeline", "outcome"},
	)

	runnerClaimsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "sparkwing_runner_claims_total",
			Help: "Node-claim attempts from a pool / agent claim loop, by outcome.",
		},
		[]string{"outcome"},
	)
)

func init() {
	metricsRegistry.MustRegister(
		nodeExecutionSeconds,
		runnerClaimsTotal,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	orchestrator.MetricsHook = observeNodeExecution
}

func observeNodeExecution(pipeline, outcome string, d time.Duration) {
	if pipeline == "" || outcome == "" {
		return
	}
	if d <= 0 {
		return
	}
	nodeExecutionSeconds.WithLabelValues(pipeline, outcome).Observe(d.Seconds())
}

func observeClaimOutcome(outcome string) {
	runnerClaimsTotal.WithLabelValues(outcome).Inc()
}

func StartMetricsListener(ctx context.Context, addr string, logger *slog.Logger) error {
	if addr == "" {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(metricsRegistry, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("metrics listener started", "addr", addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
