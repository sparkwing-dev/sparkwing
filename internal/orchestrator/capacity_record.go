package orchestrator

import (
	"context"
	"log/slog"
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
		err = st.SetPipelinePin(ctx, pipeline, "", 0, 0)
	} else {
		err = st.SetPipelinePin(ctx, pipeline, "", pin.Cores, pin.MemoryBytes)
	}
	if err != nil {
		slog.WarnContext(ctx, "record pipeline resource pin failed", "pipeline", pipeline, "err", err)
	}
	dominant := cacheDominant(nodes)
	cpuMeasured := nodemetrics.CPUAccountingAvailable()
	bucket := nodemetrics.Interval()
	intervals := map[int64]intervalTotal{}
	var cpuIntegral int64
	var exactPeakMem int64
	runValid := true
	add := func(total *int64, value int64) {
		if *total < 0 || value < 0 || *total > math.MaxInt64-value {
			runValid = false
			return
		}
		*total += value
	}
	measured := false
	hasRunCPU := false
	for _, n := range nodes {
		if n.Outcome == string(sparkwing.Cached) {
			continue
		}
		samples, err := st.ListNodeMetrics(ctx, runID, n.NodeID)
		if err != nil {
			continue
		}
		exactCPU, exactMem, exactWall := nodeUsage(n)
		if len(samples) == 0 && exactCPU == 0 && exactMem == 0 {
			continue
		}
		measured = true
		var observedCores float64
		hasNodeCPU := false
		unknown := false
		var peakMem int64
		commandMem := map[int64]int64{}
		for _, s := range samples {
			if s.Kind == store.MetricUnknown {
				unknown, runValid = true, false
			}
			if s.MemoryBytes > peakMem {
				peakMem = s.MemoryBytes
			}
			key := s.TS.Truncate(bucket).UnixNano()
			total := intervals[key]
			if s.OneShot() {
				commandMem[key] = max(commandMem[key], s.MemoryBytes)
			} else if s.Kind == store.MetricInterval {
				hasNodeCPU, hasRunCPU, total.hasCPU = true, true, true
				observedCores = math.Max(observedCores, float64(s.CPUMillicores)/1000.0)
				add(&total.cpuMillicores, s.CPUMillicores)
				add(&total.memoryBytes, s.MemoryBytes)
			}
			intervals[key] = total
		}
		for key, mem := range commandMem {
			total := intervals[key]
			add(&total.oneShotMemoryBytes, mem)
			intervals[key] = total
		}
		add(&cpuIntegral, int64(exactCPU))
		if exactMem > exactPeakMem {
			exactPeakMem = exactMem
		}
		if exactMem > peakMem {
			peakMem = exactMem
		}
		occupancy := nodeOccupancy(n, samples, exactWall)
		meanCores := exactMeanCores(exactCPU, occupancy)
		peakCores := capLocalPeakCores(ctx, pipeline, n.NodeID, math.Max(observedCores, meanCores))
		if !hasNodeCPU && exactCPU == 0 {
			runValid = false
			continue
		}
		if unknown || contended {
			continue
		}
		_ = st.RecordProfileObservation(ctx, pipeline, n.NodeID, store.ProfileObservation{
			Duration:        occupancy,
			PeakCores:       peakCores,
			SustainedCores:  math.Min(sustainedNodeCores(samples, meanCores), peakCores),
			PeakMemoryBytes: peakMem,
			CPUMeasured:     cpuMeasured,
			PlanHash:        planHash,
		})
	}
	for _, total := range intervals {
		if total.memoryBytes > math.MaxInt64-total.oneShotMemoryBytes {
			runValid = false
		}
	}
	if !runValid {
		slog.WarnContext(ctx, "run resource measurements are incomplete or exceed the supported range", "pipeline", pipeline, "run_id", runID)
		return
	}
	if dominant || !measured || (!hasRunCPU && cpuIntegral == 0) {
		return
	}
	runDur := execEnd.Sub(execStart)
	if runDur < 0 {
		runDur = 0
	}
	runMeanCores := exactMeanCores(time.Duration(cpuIntegral), runDur)
	runPeakCores, runPeakMem := peakProcessReading(ctx, pipeline, intervals, runMeanCores, exactPeakMem)
	runSustainedCores := math.Min(sustainedProcessCores(intervals, runMeanCores), runPeakCores)
	if contended {
		// safety: use peaks for the pre-graduation floor; contention suppresses
		// sustained readings and would create a closed deflation loop.
		floorCores := runPeakCores
		if charge.Cores > 0 && runPeakCores >= capacity.CeilingHitFraction*charge.Cores {
			floorCores = math.Max(floorCores, charge.Cores)
		}
		floorMem := runPeakMem
		if charge.MemoryBytes > 0 && float64(runPeakMem) >= capacity.CeilingHitFraction*float64(charge.MemoryBytes) {
			floorMem = max(floorMem, charge.MemoryBytes)
		}
		_ = st.RecordProfileObservation(ctx, pipeline, "", store.ProfileObservation{
			CPUMeasured:      cpuMeasured,
			PlanHash:         planHash,
			Contended:        true,
			FloorCores:       floorCores,
			FloorMemoryBytes: floorMem,
		})
		return
	}
	_ = st.RecordProfileObservation(ctx, pipeline, "", store.ProfileObservation{
		Duration:        runDur,
		PeakCores:       runPeakCores,
		SustainedCores:  runSustainedCores,
		PeakMemoryBytes: runPeakMem,
		CPUMeasured:     cpuMeasured,
		PlanHash:        planHash,
	})
}

type intervalTotal struct {
	hasCPU             bool
	cpuMillicores      int64
	memoryBytes        int64
	oneShotMemoryBytes int64
}

func (t intervalTotal) memory() int64 { return t.memoryBytes + t.oneShotMemoryBytes }

func peakProcessReading(ctx context.Context, pipeline string, intervals map[int64]intervalTotal, meanCores float64, meanMem int64) (float64, int64) {
	peakCores := meanCores
	peakMem := meanMem
	for _, total := range intervals {
		peakCores = math.Max(peakCores, float64(total.cpuMillicores)/1000.0)
		if mem := total.memory(); mem > peakMem {
			peakMem = mem
		}
	}
	return capLocalPeakCores(ctx, pipeline, "", peakCores), peakMem
}

func sustainedProcessCores(intervals map[int64]intervalTotal, meanCores float64) float64 {
	cores := make([]float64, 0, len(intervals))
	for _, total := range intervals {
		if !total.hasCPU {
			continue
		}
		cores = append(cores, float64(total.cpuMillicores)/1000.0)
	}
	return sustainedLevel(cores, meanCores)
}

func sustainedNodeCores(samples []store.MetricSample, meanCores float64) float64 {
	cores := make([]float64, 0, len(samples))
	for _, s := range samples {
		if s.Kind == store.MetricInterval {
			cores = append(cores, float64(s.CPUMillicores)/1000.0)
		}
	}
	return sustainedLevel(cores, meanCores)
}

func sustainedLevel(cores []float64, meanCores float64) float64 {
	if meanCores <= 0 {
		if len(cores) == 0 {
			return 0
		}
		var sum float64
		for _, c := range cores {
			sum += c
		}
		meanCores = sum / float64(len(cores))
	}
	rank := store.NearestRankPercentile(cores, capacity.SustainedPercentile)
	return math.Max(rank, meanCores)
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

func exactMeanCores(cpu, wall time.Duration) float64 {
	if cpu <= 0 || wall <= 0 {
		return 0
	}
	return cpu.Seconds() / wall.Seconds()
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
