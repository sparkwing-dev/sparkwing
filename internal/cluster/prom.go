package cluster

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
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
			Help: "Claim attempts from the shared runner loop, by outcome.",
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

type metricsController struct{ runners.ClaimController }

func (c metricsController) Claim(ctx context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	response, err := c.ClaimController.Claim(ctx, request)
	outcome := "empty"
	if err != nil {
		outcome = "error"
	} else if response.Claim != nil {
		outcome = "claimed"
	}
	observeClaimOutcome(outcome)
	return response, err
}

func runRunner(ctx context.Context, runner *runners.ManagedRunner, address string) error {
	if runner.Loop.Controller != nil {
		runner.Loop.Controller = metricsController{runner.Loop.Controller}
	}
	if address == "" {
		return runner.Run(ctx)
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("runner metrics: %w", err)
	}
	return runWithMetrics(ctx, listener, runner.Run)
}

func runWithMetrics(ctx context.Context, listener net.Listener, run func(context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(metricsRegistry, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
	serving, shutdown := make(chan error, 1), make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		cancel()
		serving <- err
	}()
	go func() {
		<-ctx.Done()
		drain, stop := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer stop()
		shutdown <- errors.Join(server.Shutdown(drain), server.Close())
	}()
	err := run(ctx)
	cancel()
	return errors.Join(err, <-serving, <-shutdown)
}
