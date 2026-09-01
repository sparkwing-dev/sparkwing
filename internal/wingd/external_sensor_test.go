package wingd

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

const ledgerMemory = 16 << 30

const capacityMinusReserve = 13743895348

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
