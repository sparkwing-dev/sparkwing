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

// recordNodeUsage persists what the kernel charged a node's process onto
// the node row, where the end-of-run fold reads it. Called by whoever holds
// a runner.Result that carries the accounting; a nil usage (every runner
// that supervises no process of its own) writes nothing. An auto-retried
// node reaches here once per attempt, and the store accumulates: the machine
// paid for every attempt it ran.
//
// The write is best-effort and goes straight to the local store rather than
// through StateBackend: these figures exist only for a process this machine
// reaped, and the same fold that reads them is already local-store-only. A
// run whose state lives elsewhere records nothing rather than growing a
// wire call for a measurement that path cannot produce. It survives run
// cancellation for the same reason the terminal row does -- the process
// still cost what it cost.
//
// Object-store state now supervises node processes too, so this machine does
// reap them and the figures do exist. They are still dropped, because
// recordRunProfile -- the only reader -- reads the local store. Storing them
// where nothing folds them would claim a capacity story Mode 2 does not have;
// enabling that fold is its own decision, not a side effect of moving where
// nodes execute.
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

// runCharge is the host cost a run was admitted at, threaded to the profile
// fold so a contended run's ceiling hit can be recognized: a run that
// consumed essentially its whole charge under contention proves it wanted at
// least that much, escalating the demand floor.
type runCharge struct {
	Cores       float64
	MemoryBytes int64
}

// recordRunProfile folds one finished run's measured node metrics into the
// pipeline's stored profiles: a per-(pipeline, node) row plus the
// pipeline-level rollup that admission and ETA read. It is best-effort --
// a profile write never fails a run -- and skips runs nothing measured, so
// the measured profile only ever reflects real observations. execStart is
// the moment admission granted the run, not its submission: the rollup
// duration measures execution and excludes any admission queue wait, so a
// busy box cannot inflate its own ETAs by folding past congestion into the
// profile.
//
// A node's profile summarizes that node's own measurements, while the
// rollup summarizes what the machine drew at each moment -- the whole run
// is charged what every node running at once cost, not what its widest node
// cost. Each records two core figures: the sustained level, which cores are
// charged from once the version graduates, and the peak, which stays on
// display and is the statistic the contended-run demand floor is measured
// on.
//
// Two measurements feed the fold. The sampler's per-interval readings say
// how the draw was shaped over time but see nothing between ticks and
// nothing at all in a node that lived under one; the kernel's exit
// accounting (nodes.cpu_nanos, nodes.max_rss_bytes, nodes.process_wall_nanos,
// written by whichever runner supervised the node's process) is exact but
// says only what the node cost in total. Where a node carries both, the exact
// figures are the floor sampling cannot fall below -- the kernel's high-water
// RSS replaces a sampled maximum that missed a spike between two ticks, and
// its CPU integral over the process's own life replaces the mean of the
// readings -- and a node carrying only exit accounting is still recorded
// rather than skipped. A cluster node carries no exit accounting at all: its
// columns stay zero, which every reader here treats as absent rather than as
// a measured zero.
//
// The two measurements are never added: a node's samples and its exit
// accounting describe the same CPU from different angles, so each figure
// takes the larger of the two rather than their sum.
//
// planHash versions the rollup: a structural change re-measures the pipeline
// rather than pricing it on the predecessor's samples. A contended run
// measured its allocation, not its demand, so it never folds into the clean
// window or per-node peaks -- it only feeds the rollup's demand floor,
// escalated to its whole charge when it hit the ceiling. pipeline is the
// stored profile key (repo-scoped for a run inside a git repo), the same
// identity the admission read resolved the charge from.
func recordRunProfile(ctx context.Context, st *store.Store, pipeline, runID string, pin *capacity.Pin, planHash string, charge runCharge, contended bool, execStart, execEnd time.Time) {
	if st == nil || pipeline == "" {
		return
	}
	nodes, err := st.ListNodes(ctx, runID)
	if err != nil {
		return
	}
	if cacheDominant(nodes) {
		return
	}
	cpuMeasured := nodemetrics.CPUAccountingAvailable()
	bucket := nodemetrics.Interval()
	intervals := map[int64]intervalTotal{}
	var cpuIntegral time.Duration
	var exactPeakMem int64
	measured := false
	for _, n := range nodes {
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
	if !measured {
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
		// safety: the floor is measured on peaks, not on the sustained level
		// cores are priced from. A contended run's sustained reading is its
		// contention-suppressed allocation, so a floor fed from it would
		// decay toward that allocation and never clear the ceiling-hit
		// fraction of its own SafetyMultiple charge -- a closed deflation
		// loop on a saturated box. A burst still gets scheduled somewhere on
		// a busy box, so the peak is the statistic contention cannot
		// suppress. Nothing is lost by the conservatism: the floor prices
		// only pre-graduation versions, where erring high is the point,
		// while sustained pricing applies to graduated measured profiles.
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

// intervalTotal is what one sampling interval read across every node that was
// running in it.
//
// One-shot memory is kept apart from the ticks' because the two measure
// different things. A tick is what a node's process held at that instant, and
// concurrent nodes' instants add up to the machine. A per-command report is
// the high-water of a command's whole life, so one node's several reports
// inside one window are usually the same memory handed back and taken again;
// adding them would invent a footprint the box never carried. The window's
// memory is therefore the ticks' sum plus, per node, that node's largest
// command mark -- summed across nodes, which really were running at once.
type intervalTotal struct {
	cpuMillicores      int64
	memoryBytes        int64
	oneShotMemoryBytes int64
}

// memory is what the window held: every concurrently sampled node's reading,
// plus one command high-water per node that reported any.
func (t intervalTotal) memory() int64 { return t.memoryBytes + t.oneShotMemoryBytes }

// bucketMillicores is the rate a one-shot contributes to the window it landed
// in: the CPU it measured spread over the whole window.
//
// The sample's own cpu_millicores is that CPU spread over the command's span
// instead, which is the right figure for showing what the command drew while
// it ran and the wrong one for summing: four 400ms commands at two cores,
// run back to back inside one two-second window, drew 1.6 cores of that
// window between them, not the eight their rates add to.
func bucketMillicores(cpu, window time.Duration) int64 {
	if cpu <= 0 || window <= 0 {
		return 0
	}
	return int64(cpu.Seconds() / window.Seconds() * 1000.0)
}

// peakProcessReading returns the run's heaviest interval as cores and bytes,
// floored by what the kernel's exit accounting proves the run drew.
//
// Samples are grouped into windows one nodemetrics.Interval wide rather than
// by exact timestamp. Every node samples itself in its own process, so the
// nodes of a parallel stage stamp the same tick microseconds apart; grouping
// on the timestamp would put each of them in a bucket of its own and reduce
// the run's peak to its widest node's reading -- half a machine for a pair,
// a quarter for a fan-out of four, always in the direction that over-admits.
// A sample carrying its own timestamp, as a per-command report does, lands
// in the window it happened in and sums with the ticks beside it, which is
// what it measured: that CPU really was drawn while those nodes ran.
//
// meanCores and meanMem are the floors the exit accounting sets: a run whose
// nodes all lived under a single tick has no interval to take a maximum
// from, yet it did draw its own CPU integral over its wall time and did hold
// its heaviest node's peak RSS. Neither reconstructs a burst -- an integral
// cannot -- so they only ever raise a peak that has nothing behind it. The
// core figure clamps to host capacity for the reason capLocalPeakCores
// clamps a node's.
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

// sustainedProcessCores returns the process-wide core level the run is
// priced at: the sustainedLevel of the same per-interval totals
// peakProcessReading takes its maximum from, with the run's measured CPU
// integral over its execution window standing in for the sampled mean
// whenever the exit accounting supplies one. A run of a few intervals yields
// its own maximum, so short work prices exactly as it did.
//
// It applies no host clamp of its own. The result never exceeds the raw
// peak, so taking the minimum against the already-clamped peak enforces the
// same invariant and spares a second clamp log line for one overshoot.
func sustainedProcessCores(intervals map[int64]intervalTotal, meanCores float64) float64 {
	cores := make([]float64, 0, len(intervals))
	for _, total := range intervals {
		cores = append(cores, float64(total.cpuMillicores)/1000.0)
	}
	return sustainedLevel(cores, meanCores)
}

// sustainedNodeCores is the node-level counterpart: the sustainedLevel of
// one node's own sample readings, clamped by its caller against that node's
// peak for the reason sustainedProcessCores gives. The sampler's ticks are
// equal-cadence and a per-command one-shot reports once, so treating the
// readings unweighted approximates duration weighting closely enough to
// price from and avoids carrying per-sample spans through the fold.
//
// meanCores is the node's exact CPU integral over its wall time when the
// kernel reported one, and zero otherwise. It replaces the sampled mean
// rather than joining it: both answer the same question -- what did this
// node draw on average -- and only one of them counts the CPU the sampler
// never saw.
func sustainedNodeCores(samples []store.MetricSample, meanCores float64) float64 {
	cores := make([]float64, len(samples))
	for i, s := range samples {
		cores[i] = float64(s.CPUMillicores) / 1000.0
	}
	return sustainedLevel(cores, meanCores)
}

// sustainedLevel is the core figure a series of interval readings prices at:
// the nearest-rank capacity.SustainedPercentile, guarded from below by the
// mean draw. The rank finds the plateau and keeps an idle tail from diluting
// it, but in a tail-heavy run the few hot intervals carry most of the CPU
// integral and the rank alone would price the run below its own average draw
// -- and summed average draws are the load a box carries, so a charge below
// the mean over-admits chronically, not just for a burst.
//
// meanCores is the measured mean when the kernel's exit accounting supplied
// one; a non-positive value falls back to the mean of the readings, which is
// all a sampler-only node (a pod) has. A node measured exactly but sampled
// never has no readings at all, and prices at its measured mean.
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

// nodeUsage returns the kernel's exit accounting for a node's process: the
// CPU it burned, the peak RSS it held, and the wall time it existed for.
// Every column is zero for a node no runner supervised a process for -- a
// cluster pod, a node executed in the dispatcher's own process -- and zero
// is read as absent, never as a measured zero, because no process that ran
// can have cost literally nothing.
func nodeUsage(n *store.Node) (time.Duration, int64, time.Duration) {
	if n == nil {
		return 0, 0, 0
	}
	return time.Duration(n.CPUNanos), n.MaxRSSBytes, time.Duration(n.ProcessWallNanos)
}

// nodeOccupancy is how long the node held the machine: the supervised
// process's whole life when a runner timed one, else the node's own
// start-to-finish span.
//
// The distinction is the difference between a truthful price and a
// nonsensical one. A node process spends real time on runtime startup,
// rebuilding the plan, and teardown, none of which is inside the
// started_at..finished_at window the node stamps from within itself, yet all
// of the CPU those phases burn is in the process's exit accounting. Dividing
// the whole process's CPU by the inner window prices a millisecond of work
// at host capacity. It is also the better ETA input: the box was busy for
// the whole span, not only for the part the node called its own.
func nodeOccupancy(n *store.Node, samples []store.MetricSample, processWall time.Duration) time.Duration {
	if processWall > 0 {
		return processWall
	}
	return nodeDuration(n, samples)
}

// exactMeanCores is the core level a measured CPU integral held over a
// span: the one figure sampling cannot produce for work shorter than a
// tick. Zero when either half is missing, which is the caller's signal to
// price from the samples alone.
func exactMeanCores(cpu, wall time.Duration) float64 {
	if cpu <= 0 || wall <= 0 {
		return 0
	}
	return cpu.Seconds() / wall.Seconds()
}

// cacheDominant reports whether a finished run's completed nodes were
// predominantly cache hits -- at or above capacity.CacheDominantFraction of
// them served from cache. Such a run measured the cache, not the work: its
// rollup wall time collapses to milliseconds and its CPU is near zero, so
// folding it would collapse the pipeline's p50 and age its real peaks out of
// the window. It is excluded from learning entirely, like a contended run, but
// without raising a demand floor -- a cached run proves no demand.
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

// capLocalPeakCores enforces the stored-profile invariant that a local
// profile's core figures never exceed host capacity: a reading above the
// host's core count is a sampler artifact (a reaped subtree's CPU landing in
// one interval), so what is stored clamps to host cores while the raw
// observation stays in the metric samples. It logs a one-line note when it
// clamps so an overshoot is visible rather than silently swallowed.
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
