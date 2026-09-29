package orchestrator

import (
	"context"
	"math"
	"runtime"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/nodemetrics"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func recordNodeUsage(ctx context.Context, backends Backends, runID, nodeID string, usage *runner.ResourceUsage) {
	if usage == nil || (usage.CPUTime <= 0 && usage.MaxRSSBytes <= 0) {
		return
	}
	if !backends.LocalCoordination {
		return
	}
	_ = backends.State.AddNodeUsage(context.WithoutCancel(ctx), runID, nodeID, store.NodeUsage{
		CPUTime:     usage.CPUTime,
		MaxRSSBytes: usage.MaxRSSBytes,
		Wall:        usage.Wall,
	})
}

type runCharge struct {
	Cores       float64
	MemoryBytes int64
}

func withAdmittedCharge(ctx context.Context, charge runCharge) context.Context {
	if charge.Cores <= 0 && charge.MemoryBytes <= 0 {
		return ctx
	}
	return sparkwingruntime.WithAdmission(ctx, sparkwing.Admission{
		Cores:       charge.Cores,
		MemoryBytes: charge.MemoryBytes,
	})
}

func recordRunProfile(ctx context.Context, st RunCoordination, pipeline, runID string, pin *capacity.Pin, planHash string, charge runCharge, contended bool, execStart, execEnd time.Time) {
	if st == nil || pipeline == "" {
		return
	}
	nodes, err := st.ListNodes(ctx, runID)
	if err != nil {
		return
	}
	if pin.Empty() {
		_ = st.SetPipelinePin(ctx, pipeline, "", 0, 0)
	} else {
		_ = st.SetPipelinePin(ctx, pipeline, "", pin.Cores, pin.MemoryBytes)
	}
	dominant := cacheDominant(nodes)
	bucket := nodemetrics.Interval()
	intervals := map[int64]store.MetricSample{}
	runEligible := true
	for _, n := range nodes {
		if n.Outcome == string(sparkwing.Cached) {
			continue
		}
		samples, err := st.ListNodeMetrics(ctx, runID, n.NodeID)
		if err != nil {
			runEligible = false
			continue
		}
		resources := capacity.SummarizeIntervals(samples)
		if resources.PeakCores == nil || resources.PeakMemoryBytes == nil {
			runEligible = false
			continue
		}
		for _, sample := range samples {
			if sample.Kind != store.MetricInterval {
				continue
			}
			key := sample.TS.Truncate(bucket).UnixNano()
			total := intervals[key]
			total.Kind = store.MetricInterval
			total.CPUAvailable = true
			total.MemoryAvailable = true
			total.CPUMillicores += sample.CPUMillicores
			total.MemoryBytes += sample.MemoryBytes
			intervals[key] = total
		}
		if contended {
			continue
		}
		_, _, wall := nodeUsage(n)
		peak := capLocalPeakCores(ctx, pipeline, n.NodeID, *resources.PeakCores)
		sustained := math.Min(*resources.SustainedCores, peak)
		_ = st.RecordProfileObservation(ctx, pipeline, n.NodeID, store.ProfileObservation{
			Duration: nodeOccupancy(n, samples, wall), PeakCores: peak, SustainedCores: &sustained,
			PeakMemoryBytes: *resources.PeakMemoryBytes, CPUMeasured: true, PlanHash: planHash,
		})
	}
	if dominant || !runEligible || len(intervals) == 0 {
		return
	}
	totals := make([]store.MetricSample, 0, len(intervals))
	for _, total := range intervals {
		totals = append(totals, total)
	}
	resources := capacity.SummarizeIntervals(totals)
	runPeakCores := capLocalPeakCores(ctx, pipeline, "", *resources.PeakCores)
	runPeakMem := *resources.PeakMemoryBytes
	if contended {
		floorCores := runPeakCores
		if charge.Cores > 0 && runPeakCores >= capacity.CeilingHitFraction*charge.Cores {
			floorCores = math.Max(floorCores, charge.Cores)
		}
		floorMem := runPeakMem
		if charge.MemoryBytes > 0 && float64(runPeakMem) >= capacity.CeilingHitFraction*float64(charge.MemoryBytes) {
			floorMem = max(floorMem, charge.MemoryBytes)
		}
		_ = st.RecordProfileObservation(ctx, pipeline, "", store.ProfileObservation{CPUMeasured: true, PlanHash: planHash, Contended: true, FloorCores: floorCores, FloorMemoryBytes: floorMem})
		return
	}
	sustained := math.Min(*resources.SustainedCores, runPeakCores)
	_ = st.RecordProfileObservation(ctx, pipeline, "", store.ProfileObservation{
		Duration: max(execEnd.Sub(execStart), 0), PeakCores: runPeakCores, SustainedCores: &sustained,
		PeakMemoryBytes: runPeakMem, CPUMeasured: true, PlanHash: planHash,
	})
}

func nodeUsage(n *store.Node) (time.Duration, int64, time.Duration) {
	if n == nil {
		return 0, 0, 0
	}
	return time.Duration(n.CPUNanos), n.MaxRSSBytes, time.Duration(n.ProcessWallNanos)
}

func nodeOccupancy(n *store.Node, samples []store.MetricSample, processWall time.Duration) time.Duration {
	if processWall > 0 {
		return processWall
	}
	return nodeDuration(n, samples)
}

func cacheDominant(nodes []*store.Node) bool {
	cached, total := 0, 0
	for _, n := range nodes {
		if n.Outcome == "" {
			continue
		}
		total++
		if n.Outcome == string(sparkwing.Cached) {
			cached++
		}
	}
	return total > 0 && float64(cached) >= capacity.CacheDominantFraction*float64(total)
}

func capLocalPeakCores(ctx context.Context, pipeline, node string, observedCores float64) float64 {
	hostCores := float64(runtime.NumCPU())
	if hostCores > 0 && observedCores > hostCores {
		sparkwing.Debug(ctx, "capacity: %s node %q observed %.1f cores over host %.1f; recording host capacity",
			pipeline, node, observedCores, hostCores)
		return hostCores
	}
	return observedCores
}

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
