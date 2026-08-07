package wingd

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// ledgerMemory is the 16 GiB memory capacity newHeadroomDaemon sizes its
// ledger against, repeated here so the byte figures below are readable.
const ledgerMemory = 16 << 30

// capacityMinusReserve is what the queue view used to print as memory
// external whenever available floored at zero: the whole 16 GiB capacity
// minus the 20% reserve, which is 80.0% of the machine. This exact figure
// was reported byte-identical across reads twenty minutes apart while real
// demand measured by vm_stat fell from 9.41 to 8.23 GB.
const capacityMinusReserve = 13743895348

// queueRow pulls one resource row out of a queue state.
func queueRow(t *testing.T, qs wingwire.QueueState, key string) wingwire.ResourceState {
	t.Helper()
	for _, r := range qs.Resources {
		if r.Key == key {
			return r
		}
	}
	t.Fatalf("no %q row in %+v", key, qs.Resources)
	return wingwire.ResourceState{}
}

func queueState(t *testing.T, d *Daemon) wingwire.QueueState {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.buildQueueStateLocked()
}

// TestQueueState_ExternalIsTheReadingNotTheResidual pins the defect this
// package was fixed for. The queue view used to derive external as capacity -
// held - reserved - available, so once pressure pushed available to its zero
// floor the external column stopped depending on the sensor at all and printed
// capacity minus the reserve forever. Here the sampler measures 15 GiB of
// external memory (16 GiB total, 1 GiB free) and 8 cores of external load,
// both past the floor, and both columns must carry the measurement rather
// than the residual. The cores residual would be 6.4, which is the same
// 80.0%-of-capacity shape the memory one has.
func TestQueueState_ExternalIsTheReadingNotTheResidual(t *testing.T) {
	d := newHeadroomDaemon(t, 8, 0.2)
	d.applyHeadroom(HostStat{
		TotalCores:       8,
		TotalMemoryBytes: ledgerMemory,
		FreeMemoryBytes:  1 << 30,
		LoadAverage:      8,
		BusyCores:        8,
		LoadMeasured:     true,
		CPUMeasured:      true,
		MemoryMeasured:   true,
	})

	qs := queueState(t, d)
	mem := queueRow(t, qs, "memory")
	cores := queueRow(t, qs, "cores")

	if mem.Available != 0 || cores.Available != 0 {
		t.Fatalf("this reading must floor both dimensions: cores available %v, memory available %v",
			cores.Available, mem.Available)
	}
	if mem.External == capacityMinusReserve {
		t.Fatalf("memory external = %.0f, the capacity-minus-reserve residual, not a measurement", mem.External)
	}
	if want := float64(15 << 30); mem.External != want {
		t.Fatalf("memory external = %.0f, want the measured %.0f", mem.External, want)
	}
	if cores.External == 6.4 {
		t.Fatalf("cores external = %v, the capacity-minus-reserve residual, not a measurement", cores.External)
	}
	if cores.External != 8 {
		t.Fatalf("cores external = %v, want the measured 8", cores.External)
	}
}

// TestQueueState_UnmeasuredMemorySaysSoAndIsNotSubtracted covers the negative
// control. A macOS sampler that cannot read kern.memorystatus_level hands over
// a zero FreeMemoryBytes, which is byte-for-byte what a completely full
// machine looks like. The row has to name the state and admission has to
// charge nothing for it, because a run may not be held back by pressure
// nobody looked at.
func TestQueueState_UnmeasuredMemorySaysSoAndIsNotSubtracted(t *testing.T) {
	d := newHeadroomDaemon(t, 8, 0.2)
	d.applyHeadroom(HostStat{
		TotalCores:       8,
		TotalMemoryBytes: ledgerMemory,
		FreeMemoryBytes:  0,
		LoadAverage:      0,
		LoadMeasured:     true,
		MemoryMeasured:   false,
	})

	mem := queueRow(t, queueState(t, d), "memory")
	if mem.ExternalSource != wingwire.ExternalUnmeasured {
		t.Fatalf("memory external source = %q, want %q", mem.ExternalSource, wingwire.ExternalUnmeasured)
	}
	if mem.External != 0 {
		t.Fatalf("memory external = %.0f, want no figure at all for a dimension nobody read", mem.External)
	}
	if mem.Available != capacityMinusReserve {
		t.Fatalf("memory available = %.0f, want %d (capacity minus the reserve): an unread sensor must not consume the machine",
			mem.Available, capacityMinusReserve)
	}
}

// TestQueueState_UnmeasuredCPUSaysSoAndIsNotSubtracted is the cores half of
// the same rule: a sampler that could not read CPU utilization reports no
// external cores and says the dimension is unmeasured, rather than passing a
// zero off as an idle machine. The 7.5 figure here is the stale reading the
// sampler never confirmed, and none of it may reach admission.
func TestQueueState_UnmeasuredCPUSaysSoAndIsNotSubtracted(t *testing.T) {
	d := newHeadroomDaemon(t, 8, 0.2)
	d.applyHeadroom(HostStat{
		TotalCores:       8,
		TotalMemoryBytes: ledgerMemory,
		FreeMemoryBytes:  ledgerMemory,
		BusyCores:        7.5,
		CPUMeasured:      false,
		MemoryMeasured:   true,
	})

	cores := queueRow(t, queueState(t, d), "cores")
	if cores.ExternalSource != wingwire.ExternalUnmeasured {
		t.Fatalf("cores external source = %q, want %q", cores.ExternalSource, wingwire.ExternalUnmeasured)
	}
	if cores.External != 0 {
		t.Fatalf("cores external = %v, want no figure at all for a dimension nobody read", cores.External)
	}
	if cores.Available != 6.4 {
		t.Fatalf("cores available = %v, want 6.4 (8 minus the 20%% reserve)", cores.Available)
	}
}

// TestQueueState_MeasuredExternalIsLabeledMeasured keeps the label honest in
// the other direction, so "unmeasured" is a state the daemon actually chooses
// between rather than a constant.
func TestQueueState_MeasuredExternalIsLabeledMeasured(t *testing.T) {
	d := newHeadroomDaemon(t, 8, 0.2)
	d.applyHeadroom(HostStat{
		TotalCores:       8,
		TotalMemoryBytes: ledgerMemory,
		FreeMemoryBytes:  8 << 30,
		LoadAverage:      1,
		BusyCores:        1,
		LoadMeasured:     true,
		CPUMeasured:      true,
		MemoryMeasured:   true,
	})

	qs := queueState(t, d)
	for _, key := range []string{"cores", "memory"} {
		if got := queueRow(t, qs, key).ExternalSource; got != wingwire.ExternalMeasured {
			t.Fatalf("%s external source = %q, want %q", key, got, wingwire.ExternalMeasured)
		}
	}
	if qs.ExternalSampleAgeMS < 0 {
		t.Fatalf("external sample age = %d, want the age of the applied reading", qs.ExternalSampleAgeMS)
	}
}

// TestApplyHeadroom_MeasuredFlipReappliesPastTheDeadband pins that going
// blind is itself a change worth applying. The deadband only watches the
// headroom numbers, so a sensor that fails while the totals barely move would
// otherwise keep serving its last label and report a measurement it no longer
// has.
func TestApplyHeadroom_MeasuredFlipReappliesPastTheDeadband(t *testing.T) {
	d := newHeadroomDaemon(t, 8, 0.2)
	measured := HostStat{
		TotalCores:       8,
		TotalMemoryBytes: ledgerMemory,
		FreeMemoryBytes:  ledgerMemory,
		LoadMeasured:     true,
		CPUMeasured:      true,
		MemoryMeasured:   true,
	}
	d.applyHeadroom(measured)

	blind := measured
	blind.MemoryMeasured = false
	d.applyHeadroom(blind)

	if got := queueRow(t, queueState(t, d), "memory").ExternalSource; got != wingwire.ExternalUnmeasured {
		t.Fatalf("memory external source = %q after the sensor went blind, want %q", got, wingwire.ExternalUnmeasured)
	}
}

// TestHostBlockingReason_UnmeasuredExternalMakesNoClaim keeps the wait
// explanation from naming external load it did not measure. Waiters used to
// read "0B available (external load 16.0GiB)" on a 16 GiB machine, a figure
// that is impossible on its face and was never read from anything.
func TestHostBlockingReason_UnmeasuredExternalMakesNoClaim(t *testing.T) {
	available := map[string]wingwire.ResourceState{
		"memory": {
			Key:            "memory",
			Available:      0,
			External:       17179869184,
			ExternalSource: wingwire.ExternalUnmeasured,
		},
	}
	got := hostBlockingReason(0, 1<<30, available, "")
	if got == "" {
		t.Fatal("a blocked run must still say it is blocked")
	}
	if want := "external load"; strings.Contains(got, want) {
		t.Fatalf("blocking reason = %q, must not name %q for a dimension nobody measured", got, want)
	}
}
