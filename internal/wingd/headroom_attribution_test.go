package wingd

import (
	"math"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func attributionHost(totalCores, busy float64) HostStat {
	return HostStat{
		TotalCores:       totalCores,
		TotalMemoryBytes: ledgerMemory,
		FreeMemoryBytes:  ledgerMemory,
		BusyCores:        busy,
		LoadAverage:      busy,
		LoadMeasured:     true,
		CPUMeasured:      true,
		MemoryMeasured:   true,
	}
}

type perRootOwnedSampler struct {
	byRoot   map[int]float64
	measured bool
	roots    []int
}

func (s *perRootOwnedSampler) CPUUsage(pids []int) (map[int]float64, bool) {
	s.roots = append([]int(nil), pids...)
	return s.byRoot, s.measured
}

func newAttributionDaemon(t *testing.T, byRoot map[int]float64) *Daemon {
	t.Helper()
	d := newHeadroomDaemon(t, 10, 0.2)
	d.sampler = &countingHostSampler{stat: attributionHost(10, 8.5)}
	reported := map[int]float64{9999: 5}
	for pid, cores := range byRoot {
		reported[pid] = cores
	}
	d.ownedSampler = &perRootOwnedSampler{byRoot: reported, measured: true}
	d.byRun["holder"] = &conn{runID: "holder", role: roleHolder, pid: 4242}
	d.byRun["watcher"] = &conn{runID: "watcher", role: roleWaiter, pid: 7777}
	return d
}

func queueAttribution(t *testing.T, d *Daemon) wingwire.ExternalAttribution {
	t.Helper()
	if a := queueState(t, d).ExternalAttribution; a != nil {
		return *a
	}
	return wingwire.ExternalAttribution{}
}

func TestRefreshHeadroom_ChargesOnlyUnownedCPUAsExternal(t *testing.T) {
	d := newAttributionDaemon(t, map[int]float64{4242: 6.5})

	d.refreshHeadroom()

	cores := queueRow(t, queueState(t, d), "cores")
	if math.Abs(cores.External-2) > coresEpsilon {
		t.Errorf("external cores = %.2f, want 2.00: 8.5 busy less the 6.5 this daemon's holders ran, crediting only the runs that hold and not every tree the sampler reported",
			cores.External)
	}
	if math.Abs(d.appliedCores-6) > coresEpsilon {
		t.Errorf("grantable cores = %.2f, want 6.00: 10 total less the 2.0 reserve and 2.0 external", d.appliedCores)
	}
}

func TestRefreshHeadroom_HolderThatStartsIsNotYetCredited(t *testing.T) {
	d := newAttributionDaemon(t, map[int]float64{4242: 6.5})
	d.refreshHeadroom()
	d.byRun["second"] = &conn{runID: "second", role: roleHolder, pid: 5150}

	d.refreshHeadroom()

	if math.Abs(d.smoothedExternal-2) > coresEpsilon {
		t.Errorf("external cores = %.2f, want 2.00: a run that has just started has run no CPU yet, and crediting it none must not disturb what the working runs are credited",
			d.smoothedExternal)
	}
}

func TestRefreshHeadroom_DepartedHolderStopsBeingCredited(t *testing.T) {
	d := newAttributionDaemon(t, map[int]float64{4242: 6.5})
	d.refreshHeadroom()
	delete(d.byRun, "holder")

	for range 30 {
		d.refreshHeadroom()
	}

	if math.Abs(d.smoothedExternal-8.5) > coresEpsilon {
		t.Errorf("external cores = %.2f, want 8.50: with no run holding, every busy core belongs to the rest of the machine",
			d.smoothedExternal)
	}
	if got := queueAttribution(t, d); got.SamplerUnreadable != 0 {
		t.Errorf("attribution = %+v, want clean: a daemon holding nothing has nothing it failed to measure", got)
	}
	if src := queueRow(t, queueState(t, d), "cores").ExternalSource; src != wingwire.ExternalMeasured {
		t.Errorf("cores external source = %q, want %q: a daemon holding nothing must not warn on every reading it takes", src, wingwire.ExternalMeasured)
	}
}

func TestRefreshHeadroom_CountsEveryAttributedSample(t *testing.T) {
	d := newAttributionDaemon(t, map[int]float64{4242: 6.5})

	for range 3 {
		d.refreshHeadroom()
	}

	got := queueAttribution(t, d)
	if got.Samples != 3 {
		t.Errorf("samples = %d, want 3: the denominator counts every readable host reading", got.Samples)
	}
	if got.SamplerUnreadable != 0 || got.RunsWithoutProcess != 0 || got.RunsAwaitingMeasure != 0 {
		t.Errorf("attribution = %+v, want every fault count at zero", got)
	}
	if src := queueRow(t, queueState(t, d), "cores").ExternalSource; src != wingwire.ExternalMeasured {
		t.Errorf("cores external source = %q, want %q: every holding run's CPU was measured", src, wingwire.ExternalMeasured)
	}
}

func TestRefreshHeadroom_DoesNotCountAnUnreadableHost(t *testing.T) {
	d := newAttributionDaemon(t, map[int]float64{4242: 6.5})
	blind := attributionHost(10, 8.5)
	blind.CPUMeasured = false
	d.sampler = &countingHostSampler{stat: blind}

	d.refreshHeadroom()

	if got := queueAttribution(t, d); got.Samples != 0 {
		t.Errorf("samples = %d, want 0: a reading that measured no host CPU is no denominator for the counts read against it", got.Samples)
	}
}

func TestRefreshHeadroom_CountsAnUnreadableSampler(t *testing.T) {
	d := newAttributionDaemon(t, nil)
	d.ownedSampler = &perRootOwnedSampler{measured: false}

	d.refreshHeadroom()

	got := queueAttribution(t, d)
	if got.SamplerUnreadable != 1 || got.Samples != 1 {
		t.Errorf("attribution = %+v, want 1 unreadable of 1 reading", got)
	}
	if src := queueRow(t, queueState(t, d), "cores").ExternalSource; src != wingwire.ExternalUnattributed {
		t.Errorf("cores external source = %q, want %q: the figure carries CPU this daemon could not separate out", src, wingwire.ExternalUnattributed)
	}
	cores := queueRow(t, queueState(t, d), "cores")
	if math.Abs(cores.External-8.5) > coresEpsilon {
		t.Errorf("external cores = %.2f, want the whole 8.50 charged when the sampler reads nothing", cores.External)
	}
}

func TestRefreshHeadroom_CountsAHolderThatReportsNoProcess(t *testing.T) {
	d := newAttributionDaemon(t, map[int]float64{4242: 6.5})
	d.byRun["quiet"] = &conn{runID: "quiet", role: roleHolder}

	d.refreshHeadroom()

	if roots := d.ownedSampler.(*perRootOwnedSampler).roots; len(roots) != 1 || roots[0] != 4242 {
		t.Errorf("sampled roots = %v, want [4242]: a run with no process id gives the sampler nothing to walk, and a waiter is not this daemon's work either", roots)
	}
	got := queueAttribution(t, d)
	if got.RunsWithoutProcess != 1 {
		t.Errorf("runs-without-process = %d, want 1: a run with no process id has CPU this daemon cannot locate", got.RunsWithoutProcess)
	}
	if got.SamplerUnreadable != 0 || got.RunsAwaitingMeasure != 0 {
		t.Errorf("attribution = %+v, want the other fault counts at zero", got)
	}
	if src := queueRow(t, queueState(t, d), "cores").ExternalSource; src != wingwire.ExternalUnattributed {
		t.Errorf("cores external source = %q, want %q: the figure carries CPU this daemon could not separate out", src, wingwire.ExternalUnattributed)
	}
	cores := queueRow(t, queueState(t, d), "cores")
	if math.Abs(cores.External-2) > coresEpsilon {
		t.Errorf("external cores = %.2f, want 2.00: one run without a process id costs its own CPU, not every other run's measurement",
			cores.External)
	}
}

func TestRefreshHeadroom_CountsAHolderWithNoReadingYet(t *testing.T) {
	d := newAttributionDaemon(t, map[int]float64{4242: 6.5})
	d.refreshHeadroom()
	d.byRun["fresh"] = &conn{runID: "fresh", role: roleHolder, pid: 5150}

	d.refreshHeadroom()

	got := queueAttribution(t, d)
	if got.RunsAwaitingMeasure != 1 {
		t.Errorf("runs-awaiting-measure = %d, want 1: a run the sampler returned no figure for is charged to the machine, and saying so is the difference between a known gap and a silent one",
			got.RunsAwaitingMeasure)
	}
}

func TestRefreshHeadroom_CountsOneHolderPerProcessID(t *testing.T) {
	d := newAttributionDaemon(t, map[int]float64{4242: 6.5})
	d.byRun["second"] = &conn{runID: "second", role: roleHolder, pid: 4242}

	d.refreshHeadroom()

	cores := queueRow(t, queueState(t, d), "cores")
	if math.Abs(cores.External-2) > coresEpsilon {
		t.Errorf("external cores = %.2f, want 2.00: two runs sharing a process tree own that tree's CPU once", cores.External)
	}
}

func TestRefreshHeadroom_CountsAHolderWhoseProcessDied(t *testing.T) {
	d := newAttributionDaemon(t, map[int]float64{4242: 6.5})
	d.refreshHeadroom()

	d.ownedSampler = &perRootOwnedSampler{byRoot: map[int]float64{9999: 5}, measured: true}
	d.refreshHeadroom()

	got := queueAttribution(t, d)
	if got.RunsProcessGone != 1 {
		t.Errorf("runs-process-gone = %d, want 1: a run this daemon had already measured, now holding with no process to measure, died without releasing",
			got.RunsProcessGone)
	}
	if got.RunsAwaitingMeasure != 0 {
		t.Errorf("runs-awaiting-measure = %d, want 0: a dead run is a fault, and filing it under the count documented as the expected shape hides it",
			got.RunsAwaitingMeasure)
	}
}

func TestRefreshHeadroom_ForgetsARunItNoLongerHolds(t *testing.T) {
	d := newAttributionDaemon(t, map[int]float64{4242: 6.5})
	d.refreshHeadroom()
	delete(d.byRun, "holder")
	d.refreshHeadroom()

	d.byRun["holder"] = &conn{runID: "holder", role: roleHolder, pid: 4242}
	d.ownedSampler = &perRootOwnedSampler{byRoot: map[int]float64{}, measured: true}
	d.refreshHeadroom()

	if got := queueAttribution(t, d); got.RunsProcessGone != 0 {
		t.Errorf("runs-process-gone = %d, want 0: a pid that released and came back is a fresh run awaiting its first figure, not a death, and remembering it forever would leak a pid per run",
			got.RunsProcessGone)
	}
}

func TestRefreshHeadroom_VerdictOutlastsTheReadingThatCausedIt(t *testing.T) {
	now := time.Now()
	d := newAttributionDaemon(t, map[int]float64{4242: 6.5})
	d.cfg.Now = func() time.Time { return now }
	tick := func() {
		now = now.Add(d.cfg.headroomMaxAge())
		d.refreshHeadroom()
	}
	d.byRun["quiet"] = &conn{runID: "quiet", role: roleHolder}
	tick()
	delete(d.byRun, "quiet")

	readingsUntilWeightSpent := 0
	for weight := 1.0; weight >= unattributedResidual; weight *= 1 - loadEMAAlpha {
		readingsUntilWeightSpent++
	}

	for range readingsUntilWeightSpent - 1 {
		tick()
		if src := queueRow(t, queueState(t, d), "cores").ExternalSource; src != wingwire.ExternalUnattributed {
			t.Fatalf("cores external source = %q, want %q: the bad reading still holds more than the residual share of the figure, so the verdict must not clear before the figure does",
				src, wingwire.ExternalUnattributed)
		}
	}

	tick()
	if src := queueRow(t, queueState(t, d), "cores").ExternalSource; src != wingwire.ExternalMeasured {
		t.Errorf("cores external source = %q, want %q: the bad reading's weight is spent, so the figure no longer carries it",
			src, wingwire.ExternalMeasured)
	}
}

func TestRefreshHeadroom_ABlindSamplerClearsTheVerdictWithTheFigure(t *testing.T) {
	d := newAttributionDaemon(t, map[int]float64{4242: 6.5})
	d.byRun["quiet"] = &conn{runID: "quiet", role: roleHolder}
	d.refreshHeadroom()
	delete(d.byRun, "quiet")

	blind := attributionHost(10, 8.5)
	blind.CPUMeasured = false
	d.sampler = &countingHostSampler{stat: blind}
	d.refreshHeadroom()
	d.sampler = &countingHostSampler{stat: attributionHost(10, 4)}
	d.refreshHeadroom()

	if src := queueRow(t, queueState(t, d), "cores").ExternalSource; src != wingwire.ExternalMeasured {
		t.Errorf("cores external source = %q, want %q: losing the host sensor throws the smoothed figure away, so the verdict on it cannot outlive it",
			src, wingwire.ExternalMeasured)
	}
}

type baselineOwnedSampler struct {
	cores map[int]float64
	seen  map[int]bool
}

func (s *baselineOwnedSampler) CPUUsage(pids []int) (map[int]float64, bool) {
	byRoot := make(map[int]float64, len(pids))
	for _, pid := range pids {
		if s.seen[pid] {
			byRoot[pid] = s.cores[pid]
		}
		s.seen[pid] = true
	}
	return byRoot, true
}

func TestRefreshHeadroom_ChurnDoesNotCostTheRunsAlreadyMeasured(t *testing.T) {
	d := newHeadroomDaemon(t, 10, 0.2)
	d.sampler = &countingHostSampler{stat: attributionHost(10, 8.5)}
	d.ownedSampler = &baselineOwnedSampler{cores: map[int]float64{4242: 6.5}, seen: map[int]bool{}}
	d.byRun["long"] = &conn{runID: "long", role: roleHolder, pid: 4242}
	d.refreshHeadroom()

	for i := range 40 {
		d.byRun["short"] = &conn{runID: "short", role: roleHolder, pid: 5150 + i}
		d.refreshHeadroom()
		delete(d.byRun, "short")
		d.refreshHeadroom()
	}

	if math.Abs(d.smoothedExternal-2) > coresEpsilon {
		t.Errorf("external cores = %.2f, want 2.00: short runs arriving without a CPU figure yet must not cost the long run the 6.5 cores already measured for it",
			d.smoothedExternal)
	}
	if got := queueAttribution(t, d); got.RunsAwaitingMeasure == 0 {
		t.Error("runs-awaiting-measure = 0, want the arrivals counted: a run charged to the machine because it has no figure yet must be visible, not silent")
	}
}
