package orchestrator

import (
	"context"
	"math"
	"runtime"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/nodemetrics"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func recordNodeUsage(ctx context.Context, backend StateBackend, runID, nodeID string, usage *runner.ResourceUsage) {
	if usage == nil || (usage.CPUTime <= 0 && usage.MaxRSSBytes <= 0) {
		return
	}
	st := canonicalLocalStore(backend)
	if st == nil {
		return
	}
	_ = st.AddNodeUsage(context.WithoutCancel(ctx), runID, nodeID, store.NodeUsage{
		CPUTime:     usage.CPUTime,
		MaxRSSBytes: usage.MaxRSSBytes,
		Wall:        usage.Wall,
	})
}

type runCharge struct {
	Cores       float64
	MemoryBytes int64
}

func recordRunProfile(ctx context.Context, st *store.Store, pipeline, runID string, pin *capacity.Pin, planHash string, charge runCharge, contended bool, execStart, execEnd time.Time) {
	if st == nil || pipeline == "" {
		return
	}
	nodes, err := st.ListNodes(ctx, runID)
	if err != nil {
		return
	}
	dominant := cacheDominant(nodes)
	cpuMeasured := nodemetrics.CPUAccountingAvailable()
	bucket := nodemetrics.Interval()
	intervals := map[int64]intervalTotal{}
	var cpuIntegral time.Duration
	var exactPeakMem int64
	measured := false
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
		var peakMem int64
		commandMem := map[int64]int64{}
		for _, s := range samples {
			observedCores = math.Max(observedCores, float64(s.CPUMillicores)/1000.0)
			if s.MemoryBytes > peakMem {
				peakMem = s.MemoryBytes
			}
			key := s.TS.Truncate(bucket).UnixNano()
			total := intervals[key]
			if s.OneShot() {
				total.cpuMillicores += bucketMillicores(s.CPUTime, bucket)
				commandMem[key] = max(commandMem[key], s.MemoryBytes)
			} else {
				total.cpuMillicores += s.CPUMillicores
				total.memoryBytes += s.MemoryBytes
			}
			intervals[key] = total
		}
		for key, mem := range commandMem {
			total := intervals[key]
			total.oneShotMemoryBytes += mem
			intervals[key] = total
		}
		cpuIntegral += exactCPU
		if exactMem > exactPeakMem {
			exactPeakMem = exactMem
		}
		if exactMem > peakMem {
			peakMem = exactMem
		}
		occupancy := nodeOccupancy(n, samples, exactWall)
		meanCores := exactMeanCores(exactCPU, occupancy)
		peakCores := capLocalPeakCores(ctx, pipeline, n.NodeID, math.Max(observedCores, meanCores))
		if contended {
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
	if dominant || !measured {
		return
	}
	runDur := execEnd.Sub(execStart)
	if runDur < 0 {
		runDur = 0
	}
	runMeanCores := exactMeanCores(cpuIntegral, runDur)
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
	if pin.Empty() {
		_ = st.SetProfilePin(ctx, pipeline, "", 0, 0)
		return
	}
	_ = st.SetProfilePin(ctx, pipeline, "", pin.Cores, pin.MemoryBytes)
}

type intervalTotal struct {
	cpuMillicores      int64
	memoryBytes        int64
	oneShotMemoryBytes int64
}

func (t intervalTotal) memory() int64 { return t.memoryBytes + t.oneShotMemoryBytes }

func bucketMillicores(cpu, window time.Duration) int64 {
	if cpu <= 0 || window <= 0 {
		return 0
	}
	return int64(cpu.Seconds() / window.Seconds() * 1000.0)
}

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
		cores = append(cores, float64(total.cpuMillicores)/1000.0)
	}
	return sustainedLevel(cores, meanCores)
}

func sustainedNodeCores(samples []store.MetricSample, meanCores float64) float64 {
	cores := make([]float64, len(samples))
	for i, s := range samples {
		cores[i] = float64(s.CPUMillicores) / 1000.0
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
