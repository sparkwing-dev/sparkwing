package wingd

import (
	"math"
	"testing"

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
	d.ownedSampler = &perRootOwnedSampler{byRoot: byRoot, measured: true}
	d.byRun["holder"] = &conn{runID: "holder", role: roleHolder, pid: 4242}
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
		t.Errorf("external cores = %.2f, want 2.00: 8.5 busy less the 6.5 this daemon's holders ran", cores.External)
	}
	if math.Abs(d.appliedCores-6) > coresEpsilon {
		t.Errorf("grantable cores = %.2f, want 6.00: 10 total less the 2.0 reserve and 2.0 external", d.appliedCores)
	}
}

func TestRefreshHeadroom_ChurnAroundALongRunDoesNotSpendTheBudget(t *testing.T) {
	d := newAttributionDaemon(t, map[int]float64{4242: 6.5})

	for i := range 40 {
		d.byRun["short"] = &conn{runID: "short", role: roleHolder, pid: 5150 + i}
		d.refreshHeadroom()
		delete(d.byRun, "short")
		d.refreshHeadroom()
	}

	if math.Abs(d.appliedCores-6) > coresEpsilon {
		t.Errorf("grantable cores = %.2f, want 6.00: short runs starting and finishing around a long one must not charge that long run's 6.5 cores to the machine, however long the churn lasts",
			d.appliedCores)
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
}

func TestRefreshHeadroom_CountsEveryAttributedSample(t *testing.T) {
	d := newAttributionDaemon(t, map[int]float64{4242: 6.5})

	for range 3 {
		d.refreshHeadroom()
	}

	got := queueAttribution(t, d)
	if got.Samples != 3 {
		t.Errorf("samples = %d, want 3: the denominator counts every readable host sample", got.Samples)
	}
	if got.OwnedUnreadable != 0 || got.HolderUnidentified != 0 {
		t.Errorf("attribution = %+v, want no unreadable or unidentified samples", got)
	}
}

func TestRefreshHeadroom_CountsAnUnreadableOwnedSampler(t *testing.T) {
	d := newAttributionDaemon(t, nil)
	d.ownedSampler = &perRootOwnedSampler{measured: false}

	d.refreshHeadroom()

	got := queueAttribution(t, d)
	if got.OwnedUnreadable != 1 || got.Samples != 1 {
		t.Errorf("attribution = %+v, want 1 unreadable of 1 sample", got)
	}
	cores := queueRow(t, queueState(t, d), "cores")
	if math.Abs(cores.External-8.5) > coresEpsilon {
		t.Errorf("external cores = %.2f, want the whole 8.50 charged when the owned sampler reads nothing", cores.External)
	}
}

func TestRefreshHeadroom_CountsAHolderThatReportsNoProcess(t *testing.T) {
	d := newAttributionDaemon(t, map[int]float64{4242: 6.5})
	d.byRun["quiet"] = &conn{runID: "quiet", role: roleHolder}

	d.refreshHeadroom()

	got := queueAttribution(t, d)
	if got.HolderUnidentified != 1 {
		t.Errorf("holder-unidentified = %d, want 1: a holder with no process id has CPU this daemon cannot measure", got.HolderUnidentified)
	}
	if got.OwnedUnreadable != 0 {
		t.Errorf("owned-unreadable = %d, want 0: the sampler read the holders it could identify", got.OwnedUnreadable)
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
