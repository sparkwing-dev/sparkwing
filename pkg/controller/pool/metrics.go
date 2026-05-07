package pool

import (
	"github.com/sparkwing-dev/sparkwing/v2/otelutil"
	"go.opentelemetry.io/otel/metric"
)

var (
	poolReconcileDur metric.Float64Histogram
	poolWarmDur      metric.Float64Histogram
	PoolCheckouts    metric.Int64Counter
	PoolReturns      metric.Int64Counter
)

// InitMetrics registers OTel instruments for pool management.
// Call this once during controller startup.
func InitMetrics() {
	meter := otelutil.Meter("sparkwing-controller")
	poolReconcileDur, _ = meter.Float64Histogram("sparkwing.pool.reconcile_duration",
		metric.WithDescription("PVC reconcile loop duration"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(0.1, 0.5, 1, 2, 5, 10, 30))
	poolWarmDur, _ = meter.Float64Histogram("sparkwing.pool.warm_duration",
		metric.WithDescription("PVC warming duration"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(1, 5, 10, 30, 60, 120, 300))
	PoolCheckouts, _ = meter.Int64Counter("sparkwing.pool.checkouts",
		metric.WithDescription("Total PVC checkouts"),
		metric.WithUnit("{checkout}"))
	PoolReturns, _ = meter.Int64Counter("sparkwing.pool.returns",
		metric.WithDescription("Total PVC returns"),
		metric.WithUnit("{return}"))
}
