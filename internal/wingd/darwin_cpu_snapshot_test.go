package wingd

import (
	"math"
	"testing"
)

// Fixtures use the `pid ppid time` form printed by `ps -Ao pid=,ppid=,time=`.
// The third column is cumulative CPU time. Utilization is the difference
// between two of these over the wall time between them.

func TestDarwinCPUSnapshotPairsHostAndOwnedUnion(t *testing.T) {
	previous, ok := parseDarwinCPUSnapshot("1 0 0:00.00\n10 1 0:00.00\n11 10 0:00.00\n20 1 0:00.00\n")
	if !ok {
		t.Fatal("parse previous snapshot failed")
	}
	current, ok := parseDarwinCPUSnapshot("1 0 0:05.00\n10 1 0:10.00\n11 10 0:05.00\n20 1 0:10.00\n")
	if !ok {
		t.Fatal("parse current snapshot failed")
	}

	// 30 CPU-seconds burned across 10 wall seconds is 3 busy cores; the roots'
	// union burned 15, so 1.5.
	host, hostMeasured, owned, ownedMeasured := darwinCPUFromSnapshot(current, previous, 10, []int{10, 11}, 8)

	if !hostMeasured || math.Abs(host-3) > 0.0001 {
		t.Fatalf("host CPU = %v, measured %v; want 3 cores", host, hostMeasured)
	}
	if !ownedMeasured || math.Abs(owned-1.5) > 0.0001 {
		t.Fatalf("owned CPU = %v, measured %v; want overlapping roots' 1.5-core union", owned, ownedMeasured)
	}
}

func TestDarwinCPUSnapshotCreditsNoCPUToAnIdleLongLivedProcess(t *testing.T) {
	// A process that burned hours of CPU earlier and is now idle carries a large
	// cumulative total, so its lifetime average from `ps -o pcpu=` remains high.
	// Differencing two readings must credit it zero because it did no work between
	// them.
	previous, ok := parseDarwinCPUSnapshot("1 0 0:00.00\n99 1 9:59:59.00\n")
	if !ok {
		t.Fatal("parse previous snapshot failed")
	}
	current, ok := parseDarwinCPUSnapshot("1 0 0:00.00\n99 1 9:59:59.00\n")
	if !ok {
		t.Fatal("parse current snapshot failed")
	}

	host, hostMeasured, _, _ := darwinCPUFromSnapshot(current, previous, 10, nil, 8)

	if !hostMeasured {
		t.Fatal("two readings must report measured")
	}
	if host != 0 {
		t.Fatalf("host CPU = %v; a process idle across the interval must book NOTHING, whatever its lifetime total", host)
	}
}

func TestDarwinCPUSnapshotFirstTickIsUnmeasured(t *testing.T) {
	// With nothing to difference against, the result is "no reading",
	// never a since-start average. Reporting one would book cores against work
	// that finished before the daemon started.
	current, ok := parseDarwinCPUSnapshot("1 0 5:00.00\n")
	if !ok {
		t.Fatal("parse snapshot failed")
	}

	host, hostMeasured, _, ownedMeasured := darwinCPUFromSnapshot(current, nil, 0, nil, 8)

	if hostMeasured || ownedMeasured || host != 0 {
		t.Fatalf("first tick reported host=%v measured=%v; want an unmeasured reading", host, hostMeasured)
	}
}

func TestDarwinCPUSnapshotIgnoresABackwardsOrNewPID(t *testing.T) {
	// A PID reused by a shorter-lived process reads backwards, and a process
	// born since the last tick has no baseline. Crediting either imports CPU from
	// outside the interval.
	previous, ok := parseDarwinCPUSnapshot("1 0 1:00.00\n")
	if !ok {
		t.Fatal("parse previous snapshot failed")
	}
	current, ok := parseDarwinCPUSnapshot("1 0 0:01.00\n77 1 0:20.00\n")
	if !ok {
		t.Fatal("parse current snapshot failed")
	}

	host, hostMeasured, _, _ := darwinCPUFromSnapshot(current, previous, 10, nil, 8)

	if !hostMeasured || host != 0 {
		t.Fatalf("host CPU = %v; a backwards PID and a newborn both carry no usable interval", host)
	}
}

func TestDarwinCPUSnapshotMissingRootCreditsNoOwnedCPU(t *testing.T) {
	previous, ok := parseDarwinCPUSnapshot("1 0 0:00.00\n20 1 0:00.00\n")
	if !ok {
		t.Fatal("parse previous snapshot failed")
	}
	current, ok := parseDarwinCPUSnapshot("1 0 0:05.00\n20 1 0:10.00\n")
	if !ok {
		t.Fatal("parse current snapshot failed")
	}

	_, _, owned, ownedMeasured := darwinCPUFromSnapshot(current, previous, 10, []int{10}, 8)

	if ownedMeasured || owned != 0 {
		t.Fatalf("owned CPU = %v, measured %v; want no credit for a missing root", owned, ownedMeasured)
	}
}

func TestParseDarwinCPUTimeReadsEveryFormPSPrints(t *testing.T) {
	for _, tc := range []struct {
		field string
		want  float64
	}{
		{"0:01.00", 1},
		{"1:30.50", 90.5},
		{"2:03:04.00", 7384},
		{"1-00:00:00.00", 86400},
	} {
		got, ok := parseDarwinCPUTime(tc.field)
		if !ok || math.Abs(got-tc.want) > 0.0001 {
			t.Fatalf("parseDarwinCPUTime(%q) = (%v, %v); want %v", tc.field, got, ok, tc.want)
		}
	}
}

func TestDarwinCPUSnapshotMalformedInputIsUnmeasured(t *testing.T) {
	for _, input := range []string{"", "pid ppid cpu", "1 0 notatime", "1 0 90.0"} {
		if _, ok := parseDarwinCPUSnapshot(input); ok {
			t.Fatalf("snapshot %q parsed as measured", input)
		}
	}
}
