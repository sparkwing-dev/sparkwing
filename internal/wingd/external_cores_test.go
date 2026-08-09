package wingd

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
)

type countingHostSampler struct {
	stat  HostStat
	calls int
}

type blockingOwnedCPUSampler struct {
	started  chan []int
	release  chan struct{}
	fraction float64
}

func (s *blockingOwnedCPUSampler) CPUUsage(pids []int) (float64, bool) {
	s.started <- append([]int(nil), pids...)
	<-s.release
	return s.fraction, true
}

type fixedOwnedCPUSampler struct {
	fraction float64
	measured bool
	roots    []int
}

type pairedHostSampler struct {
	stat      HostStat
	owned     float64
	measured  bool
	pairCalls int
	hostCalls int
}

func (s *pairedHostSampler) Sample() (HostStat, error) {
	s.hostCalls++
	return s.stat, nil
}

func (s *pairedHostSampler) SampleWithOwned([]int) (HostStat, float64, bool, error) {
	s.pairCalls++
	return s.stat, s.owned, s.measured, nil
}

func (s *fixedOwnedCPUSampler) CPUUsage(pids []int) (float64, bool) {
	s.roots = append([]int(nil), pids...)
	return s.fraction, s.measured
}

func (s *countingHostSampler) Sample() (HostStat, error) {
	s.calls++
	return s.stat, nil
}

// coresEpsilon absorbs the float error in a reserve computed as a fraction
// of the core count and carried through the ledger's milli-core integers.
const coresEpsilon = 0.01

func TestCapacityRefreshReusesTheHeadroomSample(t *testing.T) {
	d := newHeadroomDaemon(t, 8, 0.2)
	sampler := &countingHostSampler{stat: HostStat{
		TotalCores:       8,
		TotalMemoryBytes: ledgerMemory,
		FreeMemoryBytes:  ledgerMemory,
		LoadMeasured:     true,
		CPUMeasured:      true,
		MemoryMeasured:   true,
	}}
	d.sampler = sampler

	d.refreshHostSample(true)

	if sampler.calls != 1 {
		t.Fatalf("capacity and headroom refresh sampled %d times, want one shared reading", sampler.calls)
	}
}

// TestApplyHeadroom_ExternalCoresAreCoresNotQueuedThreads pins the defect
// this sensor was added for. Admission subtracted the run-queue load
// average from the machine's core count, but load average counts threads
// runnable or parked on uninterruptible I/O and only some of those hold a
// core. Eight threads queued on an eight-core box while two cores' worth of
// instructions execute is the ordinary shape of an I/O-bound build;
// charging the queue length erases the machine and admits nothing.
func TestApplyHeadroom_ExternalCoresAreCoresNotQueuedThreads(t *testing.T) {
	d := newHeadroomDaemon(t, 8, 0.2)
	d.applyHeadroom(HostStat{
		TotalCores:       8,
		TotalMemoryBytes: ledgerMemory,
		FreeMemoryBytes:  ledgerMemory,
		LoadAverage:      8,
		BusyCores:        2,
		LoadMeasured:     true,
		CPUMeasured:      true,
		MemoryMeasured:   true,
	})

	cores := queueRow(t, queueState(t, d), "cores")
	if cores.External != 2 {
		t.Errorf("cores external = %v, want the measured 2: the run queue held 8 threads, 2 cores were busy",
			cores.External)
	}
	if want := 8 - 0.2*8 - 2.0; math.Abs(cores.Available-want) > coresEpsilon {
		t.Errorf("cores available = %v, want %v", cores.Available, want)
	}
}

// TestApplyHeadroom_ExternalCoresStillChargeABusyMachine is the other half
// of the same contract: a box whose cores are genuinely consumed must lose
// that headroom. Here the load average is the same 8 as above and the CPUs
// are actually saturated, so admission subtracts nearly the whole machine.
func TestApplyHeadroom_ExternalCoresStillChargeABusyMachine(t *testing.T) {
	d := newHeadroomDaemon(t, 8, 0.2)
	d.applyHeadroom(HostStat{
		TotalCores:       8,
		TotalMemoryBytes: ledgerMemory,
		FreeMemoryBytes:  ledgerMemory,
		LoadAverage:      8,
		BusyCores:        7.5,
		LoadMeasured:     true,
		CPUMeasured:      true,
		MemoryMeasured:   true,
	})

	cores := queueRow(t, queueState(t, d), "cores")
	if cores.External != 7.5 {
		t.Errorf("cores external = %v, want the measured 7.5", cores.External)
	}
	if cores.Available != 0 {
		t.Errorf("cores available = %v, want 0 on a machine whose cores are consumed", cores.Available)
	}
}

// TestApplyHeadroom_UnreadCPUSubtractsNothing holds the blind-sensor rule
// the memory dimension already follows. A sampler that could not read
// utilization reports no measurement, and a machine reported full by a
// sensor that never looked is one no run could ever enter.
func TestApplyHeadroom_UnreadCPUSubtractsNothing(t *testing.T) {
	d := newHeadroomDaemon(t, 8, 0.2)
	d.applyHeadroom(HostStat{
		TotalCores:       8,
		TotalMemoryBytes: ledgerMemory,
		FreeMemoryBytes:  ledgerMemory,
		LoadAverage:      8,
		LoadMeasured:     true,
		MemoryMeasured:   true,
	})

	cores := queueRow(t, queueState(t, d), "cores")
	if cores.External != 0 {
		t.Errorf("cores external = %v, want 0 when utilization was never read", cores.External)
	}
	if cores.ExternalSource != "unmeasured" {
		t.Errorf("cores external source = %q, want \"unmeasured\"", cores.ExternalSource)
	}
	if want := 8 - 0.2*8; math.Abs(cores.Available-want) > coresEpsilon {
		t.Errorf("cores available = %v, want the whole machine less the reserve, %v", cores.Available, want)
	}
}

func TestApplyHeadroom_MeasuredThenBlindCPUResetsEffectiveExternal(t *testing.T) {
	d := newHeadroomDaemon(t, 8, 0.2)
	stat := HostStat{
		TotalCores:       8,
		TotalMemoryBytes: ledgerMemory,
		FreeMemoryBytes:  ledgerMemory,
		BusyCores:        4,
		CPUMeasured:      true,
		MemoryMeasured:   true,
	}
	d.applyHeadroom(stat)
	stat.CPUMeasured = false
	d.applyHeadroom(stat)

	cores := queueRow(t, queueState(t, d), "cores")
	if cores.ExternalSource != "unmeasured" {
		t.Fatalf("external source = %q, want unmeasured", cores.ExternalSource)
	}
	if cores.External != 0 {
		t.Fatalf("external cores = %v, want zero after the host sensor went blind", cores.External)
	}
	if want := 8 - 0.2*8; math.Abs(cores.Available-want) > coresEpsilon {
		t.Fatalf("available cores = %v, want %v so telemetry and effective headroom agree", cores.Available, want)
	}
}

// TestApplyHeadroom_LeaseCapacityIsNotExecution proves a reservation cannot
// stand in for measured process usage. Without an owned-process reading the
// host's measured busy cores remain external for this sample.
func TestApplyHeadroom_LeaseCapacityIsNotExecution(t *testing.T) {
	d := newHeadroomDaemon(t, 8, 0.2)
	dec, _, err := d.ledger.Submit(admission.Request{ID: "holder", Cores: 3})
	if err != nil {
		t.Fatalf("submit holder: %v", err)
	}
	if dec.Kind != admission.DecisionGranted {
		t.Fatalf("holder = %s, want %s", dec.Kind, admission.DecisionGranted)
	}
	d.applyHeadroom(HostStat{
		TotalCores:       8,
		TotalMemoryBytes: ledgerMemory,
		FreeMemoryBytes:  ledgerMemory,
		LoadAverage:      8,
		BusyCores:        4,
		LoadMeasured:     true,
		CPUMeasured:      true,
		MemoryMeasured:   true,
	})

	cores := queueRow(t, queueState(t, d), "cores")
	if cores.External != 4 {
		t.Errorf("cores external = %v, want 4: the 3-core lease is capacity, not measured execution", cores.External)
	}
}

func TestRefreshHeadroomSubtractsMeasuredHolderUsageNotLeaseCapacity(t *testing.T) {
	tests := []struct {
		name          string
		hostBusy      float64
		ownedBusy     float64
		ownedMeasured bool
		wantExternal  float64
	}{
		{name: "idle oversized lease", hostBusy: 3, ownedMeasured: true, wantExternal: 3},
		{name: "working holder", hostBusy: 4, ownedBusy: 1, ownedMeasured: true, wantExternal: 3},
		{name: "holder sensor unavailable", hostBusy: 3, wantExternal: 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := newHeadroomDaemon(t, 8, 0)
			d.sampler = &countingHostSampler{stat: HostStat{
				TotalCores:       8,
				TotalMemoryBytes: ledgerMemory,
				FreeMemoryBytes:  ledgerMemory,
				BusyCores:        tc.hostBusy,
				CPUMeasured:      true,
				MemoryMeasured:   true,
			}}
			owned := &fixedOwnedCPUSampler{fraction: tc.ownedBusy, measured: tc.ownedMeasured}
			d.ownedSampler = owned
			dec, _, err := d.ledger.Submit(admission.Request{ID: "holder", Cores: 4})
			if err != nil || dec.Kind != admission.DecisionGranted {
				t.Fatalf("submit holder = %s, %v", dec.Kind, err)
			}
			d.byRun["holder"] = &conn{runID: "holder", role: roleHolder, pid: 4242}

			d.refreshHeadroom()

			cores := queueRow(t, queueState(t, d), "cores")
			if cores.External != tc.wantExternal {
				t.Errorf("external cores = %v, want %v", cores.External, tc.wantExternal)
			}
			if len(owned.roots) != 1 || owned.roots[0] != 4242 {
				t.Errorf("owned sampler roots = %v, want [4242]", owned.roots)
			}
		})
	}
}

func TestRefreshHeadroomDiscardsOwnedCPUAcrossSamePIDHolderReplacement(t *testing.T) {
	d := newHeadroomDaemon(t, 8, 0)
	d.sampler = &countingHostSampler{stat: HostStat{
		TotalCores:       8,
		TotalMemoryBytes: ledgerMemory,
		FreeMemoryBytes:  ledgerMemory,
		BusyCores:        3,
		CPUMeasured:      true,
		MemoryMeasured:   true,
	}}
	owned := &blockingOwnedCPUSampler{
		started:  make(chan []int, 1),
		release:  make(chan struct{}),
		fraction: 3,
	}
	d.ownedSampler = owned
	d.byRun["first"] = &conn{runID: "first", role: roleHolder, pid: 4242}
	done := make(chan struct{})
	go func() {
		d.refreshHeadroom()
		close(done)
	}()

	select {
	case roots := <-owned.started:
		if len(roots) != 1 || roots[0] != 4242 {
			t.Fatalf("sampled roots = %v, want [4242]", roots)
		}
	case <-time.After(time.Second):
		t.Fatal("owned CPU sampling did not start")
	}
	d.mu.Lock()
	delete(d.byRun, "first")
	d.byRun["second"] = &conn{runID: "second", role: roleHolder, pid: 4242}
	d.mu.Unlock()
	close(owned.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("headroom refresh did not finish")
	}

	cores := queueRow(t, queueState(t, d), "cores")
	if cores.External != 3 {
		t.Errorf("external cores = %v, want 3 after the sampled holder was replaced", cores.External)
	}
}

func TestRefreshHeadroomUsesOnePairedHostAndOwnedReading(t *testing.T) {
	d := newHeadroomDaemon(t, 8, 0)
	paired := &pairedHostSampler{
		stat: HostStat{
			TotalCores:       8,
			TotalMemoryBytes: ledgerMemory,
			FreeMemoryBytes:  ledgerMemory,
			BusyCores:        4,
			CPUMeasured:      true,
			MemoryMeasured:   true,
		},
		owned:    1,
		measured: true,
	}
	owned := &fixedOwnedCPUSampler{fraction: 8, measured: true}
	d.sampler = paired
	d.ownedSampler = owned
	d.byRun["holder"] = &conn{runID: "holder", role: roleHolder, pid: 4242}

	d.refreshHeadroom()

	cores := queueRow(t, queueState(t, d), "cores")
	if cores.External != 3 {
		t.Fatalf("external cores = %v, want paired host 4 minus owned 1", cores.External)
	}
	if paired.pairCalls != 1 || paired.hostCalls != 0 {
		t.Fatalf("paired calls = %d, ordinary host calls = %d; want one paired snapshot", paired.pairCalls, paired.hostCalls)
	}
	if len(owned.roots) != 0 {
		t.Fatalf("separate owned sampler received roots %v after paired sampling", owned.roots)
	}
}

func TestNewRejectsPairedHostWithExplicitOwnedCPUSampler(t *testing.T) {
	_, err := New(Config{
		Home:            t.TempDir(),
		Sampler:         &pairedHostSampler{},
		OwnedCPUSampler: &fixedOwnedCPUSampler{},
	})
	if err == nil {
		t.Fatal("New accepted two configured owned CPU authorities")
	}
	for _, field := range []string{"Sampler", "OwnedCPUSampler"} {
		if !strings.Contains(err.Error(), field) {
			t.Fatalf("New error = %q, want conflicting field %q named", err, field)
		}
	}
}
