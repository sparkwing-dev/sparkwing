package wingd

import (
	"math"
	"testing"
	"time"
)

var (
	snapshotNow    = time.Unix(1_000_000, 0)
	snapshotLastAt = snapshotNow.Add(-10 * time.Second)
)

func watchedRoots(pids ...int) []OwnedRoot {
	roots := make([]OwnedRoot, 0, len(pids))
	for _, pid := range pids {
		roots = append(roots, OwnedRoot{PID: pid, HeldSince: snapshotNow.Add(-time.Hour)})
	}
	return roots
}

func TestDarwinCPUSnapshotPairsHostAndOwnedUnion(t *testing.T) {
	previous, ok := parseDarwinCPUSnapshot("1 0 0:00.00\n10 1 0:00.00\n11 10 0:00.00\n20 1 0:00.00\n")
	if !ok {
		t.Fatal("parse previous snapshot failed")
	}
	current, ok := parseDarwinCPUSnapshot("1 0 0:05.00\n10 1 0:10.00\n11 10 0:05.00\n20 1 0:10.00\n")
	if !ok {
		t.Fatal("parse current snapshot failed")
	}

	host, hostMeasured, byRoot, ownedMeasured := darwinCPUFromSnapshot(current, previous, 10, watchedRoots(10, 11), snapshotLastAt, snapshotNow, 8, 8)

	if !hostMeasured || math.Abs(host-3) > 0.0001 {
		t.Fatalf("host CPU = %v, measured %v; want 3 cores", host, hostMeasured)
	}
	owned := sumOwnedCPU(byRoot)
	if !ownedMeasured || math.Abs(owned-1.5) > 0.0001 {
		t.Fatalf("owned CPU = %v, measured %v; want overlapping roots' 1.5-core union", owned, ownedMeasured)
	}
	if math.Abs(byRoot[10]-1) > 0.0001 || math.Abs(byRoot[11]-0.5) > 0.0001 {
		t.Fatalf("owned CPU by root = %v; want root 11's own 0.5 split out of its parent root 10's 1.0, so neither is counted twice", byRoot)
	}
}

func sumOwnedCPU(byRoot map[int]float64) float64 {
	var total float64
	for _, fraction := range byRoot {
		total += fraction
	}
	return total
}

func TestDarwinCPUSnapshotCreditsNoCPUToAnIdleLongLivedProcess(t *testing.T) {
	previous, ok := parseDarwinCPUSnapshot("1 0 0:00.00\n99 1 9:59:59.00\n")
	if !ok {
		t.Fatal("parse previous snapshot failed")
	}
	current, ok := parseDarwinCPUSnapshot("1 0 0:00.00\n99 1 9:59:59.00\n")
	if !ok {
		t.Fatal("parse current snapshot failed")
	}

	host, hostMeasured, _, _ := darwinCPUFromSnapshot(current, previous, 10, nil, snapshotLastAt, snapshotNow, 8, 8)

	if !hostMeasured {
		t.Fatal("two readings must report measured")
	}
	if host != 0 {
		t.Fatalf("host CPU = %v; a process idle across the interval must book NOTHING, whatever its lifetime total", host)
	}
}

func TestDarwinCPUSnapshotFirstTickIsUnmeasured(t *testing.T) {
	current, ok := parseDarwinCPUSnapshot("1 0 5:00.00\n")
	if !ok {
		t.Fatal("parse snapshot failed")
	}

	host, hostMeasured, _, ownedMeasured := darwinCPUFromSnapshot(current, nil, 0, nil, snapshotLastAt, snapshotNow, 8, 8)

	if hostMeasured || ownedMeasured || host != 0 {
		t.Fatalf("first tick reported host=%v measured=%v; want an unmeasured reading", host, hostMeasured)
	}
}

func TestDarwinCPUSnapshotIgnoresABackwardsPID(t *testing.T) {
	previous, ok := parseDarwinCPUSnapshot("1 0 1:00.00\n")
	if !ok {
		t.Fatal("parse previous snapshot failed")
	}
	current, ok := parseDarwinCPUSnapshot("1 0 0:01.00\n")
	if !ok {
		t.Fatal("parse current snapshot failed")
	}

	host, hostMeasured, _, _ := darwinCPUFromSnapshot(current, previous, 10, nil, snapshotLastAt, snapshotNow, 8, 8)

	if !hostMeasured || host != 0 {
		t.Fatalf("host CPU = %v; a PID whose CPU went backwards was recycled and carries no usable interval", host)
	}
}

func TestDarwinCPUSnapshotCountsANewbornInHostCPU(t *testing.T) {
	previous, ok := parseDarwinCPUSnapshot("1 0 0:01.00\n")
	if !ok {
		t.Fatal("parse previous snapshot failed")
	}
	current, ok := parseDarwinCPUSnapshot("1 0 0:01.00\n77 1 0:20.00\n")
	if !ok {
		t.Fatal("parse current snapshot failed")
	}

	host, hostMeasured, _, _ := darwinCPUFromSnapshot(current, previous, 10, nil, snapshotLastAt, snapshotNow, 8, 8)

	if !hostMeasured || math.Abs(host-2) > 0.0001 {
		t.Fatalf("host CPU = %v, want 2: a process absent from the previous snapshot ran its 20 seconds of CPU inside this 10 second window, and leaving it out understates how busy the host was, which is the understatement that lets a run's own work exceed it",
			host)
	}
}

func TestDarwinCPUSnapshotSumsEveryProcessUnderOneRoot(t *testing.T) {
	previous, ok := parseDarwinCPUSnapshot("1 0 0:00.00\n10 1 0:00.00\n11 10 0:00.00\n12 11 0:00.00\n")
	if !ok {
		t.Fatal("parse previous snapshot failed")
	}
	current, ok := parseDarwinCPUSnapshot("1 0 0:00.00\n10 1 0:10.00\n11 10 0:20.00\n12 11 0:30.00\n")
	if !ok {
		t.Fatal("parse current snapshot failed")
	}

	_, _, byRoot, ownedMeasured := darwinCPUFromSnapshot(current, previous, 10, watchedRoots(10), snapshotLastAt, snapshotNow, 8, 8)

	if !ownedMeasured || math.Abs(byRoot[10]-6) > 0.0001 {
		t.Fatalf("owned CPU by root = %v, measured %v; want the root's 1 core plus its child's 2 and grandchild's 3 summed into one tree, not the last process read",
			byRoot, ownedMeasured)
	}
}

func TestDarwinCPUSnapshotWillNotCreditCPUTheWindowCouldNotHold(t *testing.T) {
	previous, ok := parseDarwinCPUSnapshot("1 0 0:00.00\n")
	if !ok {
		t.Fatal("parse previous snapshot failed")
	}
	current, ok := parseDarwinCPUSnapshot("1 0 0:00.00\n10 1 0:00.10\n11 10 60:00.00\n")
	if !ok {
		t.Fatal("parse current snapshot failed")
	}
	roots := []OwnedRoot{{PID: 10, HeldSince: snapshotNow.Add(-2 * time.Second)}}

	_, _, byRoot, _ := darwinCPUFromSnapshot(current, previous, 2, roots, snapshotLastAt, snapshotNow, 8, 8)

	if _, figure := byRoot[10]; figure {
		t.Fatalf("owned CPU by root = %v; want no figure: an hour of CPU cannot have been run inside a two-second window, so that process joined the tree rather than starting in it and its tree's figure cannot stand",
			byRoot)
	}
}

func TestDarwinCPUSnapshotGivesAnIdleRootAZeroFigure(t *testing.T) {
	previous, ok := parseDarwinCPUSnapshot("1 0 0:00.00\n10 1 0:05.00\n20 1 0:00.00\n")
	if !ok {
		t.Fatal("parse previous snapshot failed")
	}
	current, ok := parseDarwinCPUSnapshot("1 0 0:00.00\n10 1 0:05.00\n20 1 0:10.00\n")
	if !ok {
		t.Fatal("parse current snapshot failed")
	}

	_, _, byRoot, ownedMeasured := darwinCPUFromSnapshot(current, previous, 10, watchedRoots(10), snapshotLastAt, snapshotNow, 8, 8)

	figure, present := byRoot[10]
	if !ownedMeasured || !present || figure != 0 {
		t.Fatalf("owned CPU by root = %v, measured %v; want a zero figure for a root that ran nothing: no key would read as a run awaiting its first reading and warn for as long as it stays idle",
			byRoot, ownedMeasured)
	}
}

func TestDarwinCPUSnapshotStaleParentPIDDoesNotResurrectAMissingRoot(t *testing.T) {
	previous, ok := parseDarwinCPUSnapshot("1 0 0:00.00\n9 10 0:00.00\n")
	if !ok {
		t.Fatal("parse previous snapshot failed")
	}
	current, ok := parseDarwinCPUSnapshot("1 0 0:00.00\n9 10 0:10.00\n")
	if !ok {
		t.Fatal("parse current snapshot failed")
	}

	_, _, byRoot, _ := darwinCPUFromSnapshot(current, previous, 10, watchedRoots(10), snapshotLastAt, snapshotNow, 8, 8)

	if len(byRoot) != 0 {
		t.Fatalf("owned CPU by root = %v; want none: pid 10 is gone, so a survivor still naming it as its parent must not be credited to a root this daemon cannot see",
			byRoot)
	}
}

func TestDarwinCPUSnapshotCreditsNothingToARootItCannotSee(t *testing.T) {
	previous, ok := parseDarwinCPUSnapshot("1 0 0:00.00\n10 1 0:00.00\n20 1 0:00.00\n")
	if !ok {
		t.Fatal("parse previous snapshot failed")
	}
	current, ok := parseDarwinCPUSnapshot("1 0 0:00.00\n20 1 0:10.00\n")
	if !ok {
		t.Fatal("parse current snapshot failed")
	}

	_, _, byRoot, ownedMeasured := darwinCPUFromSnapshot(current, previous, 10, watchedRoots(10), snapshotLastAt, snapshotNow, 8, 8)

	if !ownedMeasured {
		t.Fatal("measured = false; want true: the snapshot was read, and one absent root is that root's key, not the whole reading")
	}
	if _, figure := byRoot[10]; figure {
		t.Fatalf("owned CPU by root = %v; want no key for a root absent from the snapshot, so pid 20's unrelated CPU is never credited to it", byRoot)
	}
}

func TestDarwinCPUSnapshotWithNoRootsReadsTheHostAndOwnsNothing(t *testing.T) {
	previous, ok := parseDarwinCPUSnapshot("1 0 0:00.00\n20 1 0:00.00\n")
	if !ok {
		t.Fatal("parse previous snapshot failed")
	}
	current, ok := parseDarwinCPUSnapshot("1 0 0:00.00\n20 1 0:10.00\n")
	if !ok {
		t.Fatal("parse current snapshot failed")
	}

	_, _, byRoot, ownedMeasured := darwinCPUFromSnapshot(current, previous, 10, nil, snapshotLastAt, snapshotNow, 8, 8)

	if !ownedMeasured || len(byRoot) != 0 {
		t.Fatalf("owned CPU = %v, measured %v; want a read that owns nothing: a daemon holding no run must not warn on every reading it takes",
			byRoot, ownedMeasured)
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

	_, _, byRoot, ownedMeasured := darwinCPUFromSnapshot(current, previous, 10, watchedRoots(10), snapshotLastAt, snapshotNow, 8, 8)

	if !ownedMeasured || len(byRoot) != 0 {
		t.Fatalf("owned CPU = %v, measured %v; want a read that credits the missing root nothing rather than refusing every other root's figure",
			byRoot, ownedMeasured)
	}
}

func TestDarwinCPUSnapshotKeepsTheRootsItStillSees(t *testing.T) {
	previous, ok := parseDarwinCPUSnapshot("1 0 0:00.00\n10 1 0:00.00\n20 1 0:00.00\n")
	if !ok {
		t.Fatal("parse previous snapshot failed")
	}
	current, ok := parseDarwinCPUSnapshot("1 0 0:00.00\n10 1 0:10.00\n")
	if !ok {
		t.Fatal("parse current snapshot failed")
	}

	_, _, byRoot, ownedMeasured := darwinCPUFromSnapshot(current, previous, 10, watchedRoots(10, 20), snapshotLastAt, snapshotNow, 8, 8)

	if !ownedMeasured || math.Abs(byRoot[10]-1) > 0.0001 {
		t.Fatalf("owned CPU by root = %v, measured %v; want root 10's measured core kept: a run that finished must not cost the daemon the measurement of the run still working",
			byRoot, ownedMeasured)
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

func TestDarwinCPUSnapshotGivesNoFigureForABackwardsCounter(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	lastAt := now.Add(-10 * time.Second)
	previous, _ := parseDarwinCPUSnapshot("1 0 0:00.00\n100 1 8:20.00\n")
	current, _ := parseDarwinCPUSnapshot("1 0 0:00.00\n100 1 0:03.00\n")
	roots := []OwnedRoot{{PID: 100, HeldSince: now.Add(-time.Hour)}}

	_, _, byRoot, _ := darwinCPUFromSnapshot(current, previous, 10, roots, lastAt, now, 8, 8)

	if figure, reported := byRoot[100]; reported {
		t.Fatalf("backwards counter reported %v cores; want no figure, because reporting zero subtracts nothing from external and admits against CPU the daemon never measured",
			figure)
	}
}
