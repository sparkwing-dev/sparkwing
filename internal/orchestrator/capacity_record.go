package orchestrator

import (
	"context"
	"math"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/nodemetrics"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// recordRunProfile folds one finished run's measured node metrics into the
// pipeline's stored profiles: a per-(pipeline, node) row plus the
// pipeline-level rollup that admission and ETA read. It is best-effort --
// a profile write never fails a run -- and skips runs with no samples, so
// the measured profile only ever reflects real observations. execStart is
// the moment admission granted the run, not its submission: the rollup
// duration measures execution and excludes any admission queue wait, so a
// busy box cannot inflate its own ETAs by folding past congestion into the
// profile.
func recordRunProfile(ctx context.Context, st *store.Store, pipeline, runID string, pin *capacity.Pin, execStart, execEnd time.Time) {
	if st == nil || pipeline == "" {
		return
	}
	nodes, err := st.ListNodes(ctx, runID)
	if err != nil {
		return
	}
	cpuMeasured := nodemetrics.CPUAccountingAvailable()
	var runPeakCores float64
	var runPeakMem int64
	measured := false
	for _, n := range nodes {
		samples, err := st.ListNodeMetrics(ctx, runID, n.NodeID)
		if err != nil || len(samples) == 0 {
			continue
		}
		measured = true
		var peakCores float64
		var peakMem int64
		for _, s := range samples {
			peakCores = math.Max(peakCores, float64(s.CPUMillicores)/1000.0)
			if s.MemoryBytes > peakMem {
				peakMem = s.MemoryBytes
			}
		}
		_ = st.RecordProfileObservation(ctx, pipeline, n.NodeID, store.ProfileObservation{
			Duration:        nodeDuration(n, samples),
			PeakCores:       peakCores,
			PeakMemoryBytes: peakMem,
			CPUMeasured:     cpuMeasured,
		})
		runPeakCores = math.Max(runPeakCores, peakCores)
		if peakMem > runPeakMem {
			runPeakMem = peakMem
		}
	}
	if !measured {
		return
	}
	runDur := execEnd.Sub(execStart)
	if runDur < 0 {
		runDur = 0
	}
	_ = st.RecordProfileObservation(ctx, pipeline, "", store.ProfileObservation{
		Duration:        runDur,
		PeakCores:       runPeakCores,
		PeakMemoryBytes: runPeakMem,
		CPUMeasured:     cpuMeasured,
	})
	if !pin.Empty() {
		_ = st.SetProfilePin(ctx, pipeline, "", pin.Cores, pin.MemoryBytes)
	}
}

// nodeDuration is a node's wall time: its recorded start-to-finish span
// when both timestamps exist, else the span its metric samples cover.
func nodeDuration(n *store.Node, samples []store.MetricSample) time.Duration {
	if n.StartedAt != nil && n.FinishedAt != nil {
		if d := n.FinishedAt.Sub(*n.StartedAt); d > 0 {
			return d
		}
	}
	if len(samples) >= 2 {
		return samples[len(samples)-1].TS.Sub(samples[0].TS)
	}
	return 0
}
