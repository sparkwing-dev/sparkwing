package wingd

import (
	"math"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
)

// coresEpsilon absorbs the float error in a reserve computed as a fraction
// of the core count and carried through the ledger's milli-core integers.
const coresEpsilon = 0.01

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

// TestApplyHeadroom_ExternalCoresDiscountWhatLeasesHold keeps the term to
// load the daemon did not admit. Its own holders are already charged
// against capacity, so counting their CPU again would bill them twice.
func TestApplyHeadroom_ExternalCoresDiscountWhatLeasesHold(t *testing.T) {
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
	if cores.External != 1 {
		t.Errorf("cores external = %v, want 1: of 4 busy cores the daemon's own lease holds 3", cores.External)
	}
}
