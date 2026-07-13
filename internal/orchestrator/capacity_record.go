package orchestrator

import (
	"context"
	"math"
	"runtime"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/nodemetrics"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
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
		var observedCores float64
		var peakMem int64
		for _, s := range samples {
			observedCores = math.Max(observedCores, float64(s.CPUMillicores)/1000.0)
			if s.MemoryBytes > peakMem {
				peakMem = s.MemoryBytes
			}
		}
		peakCores := capLocalPeakCores(ctx, pipeline, n.NodeID, observedCores)
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

// capLocalPeakCores enforces the stored-profile invariant that a local
// profile's peak never exceeds host capacity: a measured peak above the host's
// core count is a sampler artifact (a reaped subtree's CPU landing in one
// interval), so the stored peak clamps to host cores while the raw observation
// stays in the metric samples. It logs a one-line note when it clamps so an
// overshoot is visible rather than silently swallowed.
func capLocalPeakCores(ctx context.Context, pipeline, node string, observedCores float64) float64 {
	hostCores := float64(runtime.NumCPU())
	if hostCores > 0 && observedCores > hostCores {
		sparkwing.Debug(ctx, "capacity: %s node %q observed %.1f cores over host %.1f; recording host capacity",
			pipeline, node, observedCores, hostCores)
		return hostCores
	}
	return observedCores
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
