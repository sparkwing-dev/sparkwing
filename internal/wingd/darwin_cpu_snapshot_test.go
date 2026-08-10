package wingd

import (
	"math"
	"testing"
)

func TestDarwinCPUSnapshotPairsHostAndOwnedUnion(t *testing.T) {
	snapshot, ok := parseDarwinCPUSnapshot("1 0 50.0\n10 1 100.0\n11 10 50.0\n20 1 100.0\n")
	if !ok {
		t.Fatal("parse snapshot failed")
	}

	host, hostMeasured, owned, ownedMeasured := darwinCPUFromSnapshot(snapshot, []int{10, 11}, 8)

	if !hostMeasured || math.Abs(host-3) > 0.0001 {
		t.Fatalf("host CPU = %v, measured %v; want 3 cores", host, hostMeasured)
	}
	if !ownedMeasured || math.Abs(owned-1.5) > 0.0001 {
		t.Fatalf("owned CPU = %v, measured %v; want overlapping roots' 1.5-core union", owned, ownedMeasured)
	}
}

func TestDarwinCPUSnapshotMissingRootCreditsNoOwnedCPU(t *testing.T) {
	snapshot, ok := parseDarwinCPUSnapshot("1 0 50.0\n20 1 100.0\n")
	if !ok {
		t.Fatal("parse snapshot failed")
	}

	_, _, owned, ownedMeasured := darwinCPUFromSnapshot(snapshot, []int{10}, 8)

	if ownedMeasured || owned != 0 {
		t.Fatalf("owned CPU = %v, measured %v; want no credit for a missing root", owned, ownedMeasured)
	}
}

func TestDarwinCPUSnapshotMalformedInputIsUnmeasured(t *testing.T) {
	for _, input := range []string{"", "pid ppid cpu", "1 0 -1"} {
		if _, ok := parseDarwinCPUSnapshot(input); ok {
			t.Fatalf("snapshot %q parsed as measured", input)
		}
	}
}
