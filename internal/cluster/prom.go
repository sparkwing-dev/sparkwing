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

	"github.com/sparkwing-dev/sparkwing/v2/orchestrator"
)

// Same cardinality rule as pkg/controller/prom.go: pipeline + outcome
// are bounded, node_id and principal are not.
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
	// Plug the cluster-side prometheus histogram into the
	// orchestrator's consumer-safe hook. The consumer binary leaves
	// this nil (no prometheus import); sparkwing-runner wires it up
	// here so RunNodeOnce observations are recorded.
	orchestrator.MetricsHook = observeNodeExecution
}

// observeNodeExecution records a terminal node execution from any
// caller of RunNodeOnce. Skipped when pipeline is empty (caller
// couldn't resolve it) so the scrape never shows an empty-string
// label value.
func observeNodeExecution(pipeline, outcome string, d time.Duration) {
	if pipeline == "" || outcome == "" {
		return
	}
	if d <= 0 {
		return
	}
	nodeExecutionSeconds.WithLabelValues(pipeline, outcome).Observe(d.Seconds())
}

// observeClaimOutcome records the result of one claim attempt in the
// pool loop. Outcomes: "claimed" (got a node), "empty" (queue empty),
// "error" (HTTP / controller failure). Pipeline is omitted because
// empty / error outcomes don't have one; pipeline lives on the
// execution metric instead.
func observeClaimOutcome(outcome string) {
	runnerClaimsTotal.WithLabelValues(outcome).Inc()
}

// StartMetricsListener serves /metrics on addr from this process's
// custom registry (plus Go runtime + process collectors). Blocks
// until ctx cancels or the listener fails; returns nil on ctx
// cancellation so callers can start it in a goroutine and rely on
// errgroup semantics.
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
