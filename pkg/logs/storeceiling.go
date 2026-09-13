package logs

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
)

// safety: the health route answers without a token, and the totals it carries
// are this service's own storage rather than any caller's content.
func (s *Server) storeCeilingHealth() (map[string]any, []string) {
	state := s.ceiling.State()
	summary := map[string]any{
		"enforced":               state.Enforced,
		"frozen":                 state.Frozen,
		"warning":                state.Warning,
		"bytes":                  state.Bytes,
		"objects":                state.Objects,
		"measurement_incomplete": state.Incomplete,
	}
	if !state.ReconciledAt.IsZero() {
		summary["reconciled_at"] = state.ReconciledAt.UTC().Format(rfc3339)
	}
	if !state.Enforced {
		return summary, nil
	}
	var problems []string
	switch {
	case state.Frozen:
		problems = append(problems, "store: the log store is at its "+string(state.FrozenReason)+
			" ceiling and appends are refused")
	case state.Warning:
		problems = append(problems, "store: the log store is past its warning mark")
	}
	if state.Incomplete {
		problems = append(problems, "store: the last store measurement did not finish, so the total is the running count")
	}
	return summary, problems
}

const rfc3339 = "2006-01-02T15:04:05Z07:00"

// safety: remeasuring runs on the caller's context so a deletion during shutdown
// does not walk the store after the server is gone.
func (s *Server) remeasureAfterDelete(ctx context.Context) {
	if !s.ceiling.Enforced() {
		return
	}
	if err := s.MeasureStore(ctx); err != nil {
		s.logger.Error("logs store", "op", "measure store", "err", err)
	}
}

var (
	// safety: one logs service runs per process, so the collector reads that
	// process's ceiling rather than carrying a label nothing distinguishes.
	metricsCeiling     atomic.Pointer[objectguard.Ceiling]
	registerCeilingOne sync.Once
)

var (
	storeBytesDesc = prometheus.NewDesc(
		"sparkwing_logs_store_bytes",
		"Bytes the log store holds, counted per append and replaced by each measurement.",
		nil, nil,
	)
	storeObjectsDesc = prometheus.NewDesc(
		"sparkwing_logs_store_objects",
		"Log files the store holds, counted per append and replaced by each measurement.",
		nil, nil,
	)
	storeCeilingDesc = prometheus.NewDesc(
		"sparkwing_logs_store_ceiling",
		"The configured store ceiling, by the unit it bounds. Absent while the store is unlimited.",
		[]string{"unit"}, nil,
	)
	storeCeilingFrozenDesc = prometheus.NewDesc(
		"sparkwing_logs_store_ceiling_frozen",
		"1 while the store is at its ceiling and appends are refused, 0 otherwise.",
		nil, nil,
	)
	storeCeilingWarningDesc = prometheus.NewDesc(
		"sparkwing_logs_store_ceiling_warning",
		"1 while the store is past its warning mark, 0 otherwise.",
		nil, nil,
	)
	storeCeilingIncompleteDesc = prometheus.NewDesc(
		"sparkwing_logs_store_ceiling_measurement_incomplete",
		"1 while the last store measurement did not finish, 0 otherwise.",
		nil, nil,
	)
	storeCeilingRefusedDesc = prometheus.NewDesc(
		"sparkwing_logs_store_ceiling_refused_total",
		"Appends the store ceiling refused.",
		nil, nil,
	)
)

type storeCeilingCollector struct{}

func (storeCeilingCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- storeBytesDesc
	ch <- storeObjectsDesc
	ch <- storeCeilingDesc
	ch <- storeCeilingFrozenDesc
	ch <- storeCeilingWarningDesc
	ch <- storeCeilingIncompleteDesc
	ch <- storeCeilingRefusedDesc
}

func (storeCeilingCollector) Collect(ch chan<- prometheus.Metric) {
	ceiling := metricsCeiling.Load()
	if ceiling == nil {
		return
	}
	state := ceiling.State()
	ch <- prometheus.MustNewConstMetric(storeBytesDesc, prometheus.GaugeValue, float64(state.Bytes))
	ch <- prometheus.MustNewConstMetric(storeObjectsDesc, prometheus.GaugeValue, float64(state.Objects))
	if state.MaxBytes > 0 {
		ch <- prometheus.MustNewConstMetric(storeCeilingDesc, prometheus.GaugeValue, float64(state.MaxBytes), "bytes")
	}
	if state.MaxObjects > 0 {
		ch <- prometheus.MustNewConstMetric(storeCeilingDesc, prometheus.GaugeValue, float64(state.MaxObjects), "objects")
	}
	ch <- prometheus.MustNewConstMetric(storeCeilingFrozenDesc, prometheus.GaugeValue, boolGauge(state.Frozen))
	ch <- prometheus.MustNewConstMetric(storeCeilingWarningDesc, prometheus.GaugeValue, boolGauge(state.Warning))
	ch <- prometheus.MustNewConstMetric(storeCeilingIncompleteDesc, prometheus.GaugeValue, boolGauge(state.Incomplete))
	ch <- prometheus.MustNewConstMetric(storeCeilingRefusedDesc, prometheus.CounterValue, float64(state.Refused))
}

func boolGauge(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func (s *Server) publishStoreCeiling(c *objectguard.Ceiling) {
	metricsCeiling.Store(c)
	registerCeilingOne.Do(func() {
		// safety: a duplicate registration means a second logs service in one process,
		// which a test does and a deployment does not, so the first collector stands and
		// the service serves without the gauges rather than refusing to start.
		var already prometheus.AlreadyRegisteredError
		if err := prometheus.Register(storeCeilingCollector{}); err != nil && !errors.As(err, &already) {
			s.logger.Error("logs store", "op", "register store ceiling metrics", "err", err)
		}
	})
}

func fileExists(root *os.Root, name string) bool {
	_, err := root.Stat(name)
	return err == nil
}
