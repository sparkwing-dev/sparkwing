package wingd

import (
	"math"
	"testing"
	"time"
)

func TestOwnedCPU_SumsEveryFirstSeenProcessUnderOneRoot(t *testing.T) {
	lastAt := time.Unix(100, 0)
	now := lastAt.Add(time.Second)
	born := lastAt.Add(500 * time.Millisecond)
	roots := []OwnedRoot{{PID: 10, HeldSince: lastAt}}
	processes := map[int]ownedProcess{
		10: {parentPID: 1, identity: processIdentity{pid: 10, startTicks: 1000}, cpuSeconds: 0.4, startedAt: born},
		11: {parentPID: 10, identity: processIdentity{pid: 11, startTicks: 1001}, cpuSeconds: 0.6, startedAt: born},
	}
	owners := ownedProcessOwners(roots, processes)

	byRoot, _ := ownedCPUByRoot(nil, processes, owners, roots, lastAt, lastAt, now, 8)

	if math.Abs(byRoot[10]-1) > 0.0001 {
		t.Fatalf("owned CPU by root = %v; want the root's own 0.4 and its child's 0.6 summed into one core, not the last one read: a first-sight total that keeps only one process under-credits the tree and hands the difference to external, which admits against CPU this daemon did measure",
			byRoot)
	}
}

func TestFirstSightCredit_RefusesAWindowThatStoodStillEvenForATreeThatRanNothing(t *testing.T) {
	got, ok := firstSightCredit(0, 0, 4)
	if ok || got != 0 {
		t.Fatalf("firstSightCredit(0, 0, 4) = %v, %v; want 0, false: a window of zero divides into a figure no comparison can refuse, and that figure then travels as this tree's reading",
			got, ok)
	}
}

func TestClampCores_HoldsBothEndsOfTheRange(t *testing.T) {
	for name, tc := range map[string]struct {
		cores, totalCores, want float64
	}{
		"below the floor":   {-1, 8, 0},
		"above the ceiling": {9, 8, 8},
		"inside the range":  {3, 8, 3},
	} {
		if got := clampCores(tc.cores, tc.totalCores); got != tc.want {
			t.Errorf("%s: clampCores(%v, %v) = %v, want %v: a figure outside the range subtracts the wrong amount from external",
				name, tc.cores, tc.totalCores, got, tc.want)
		}
	}
}

func TestRefreshHeadroom_AnOwnedFigureLargerThanTheHostRanIsTreatedAsUnread(t *testing.T) {
	d := newAttributionDaemon(t, map[int]float64{4242: 9})

	d.refreshHeadroom()

	if math.Abs(d.smoothedExternal-8.5) > coresEpsilon {
		t.Errorf("external cores = %.2f, want 8.50: this daemon's runs cannot have outrun the 8.5 the host ran, so the reading measured nothing and the host is charged in full; trimming the figure instead leaves external at zero, which is the over-admission the bound exists to stop",
			d.smoothedExternal)
	}
	got := queueAttribution(t, d)
	if got.SamplerUnreadable != 1 || got.Samples != 1 {
		t.Errorf("attribution = %d unreadable of %d samples, want 1 of 1: an impossible figure is a sampler that read nothing, and counting it as attributed reports the daemon as measuring cleanly while it is not",
			got.SamplerUnreadable, got.Samples)
	}
}

func darwinRootHeldAt(pid int, heldSince time.Time) []OwnedRoot {
	return []OwnedRoot{{PID: pid, HeldSince: heldSince}}
}

func TestDarwinCPUSnapshot_CreditsOnSightOnlyARootHeldInsideTheWindow(t *testing.T) {
	previous, ok := parseDarwinCPUSnapshot("1 0 0:00.00\n")
	if !ok {
		t.Fatal("parse previous snapshot failed")
	}
	current, ok := parseDarwinCPUSnapshot("1 0 0:00.00\n10 1 0:02.00\n")
	if !ok {
		t.Fatal("parse current snapshot failed")
	}

	for name, tc := range map[string]struct {
		heldSince  time.Time
		wantFigure bool
	}{
		"held before the last reading": {snapshotLastAt.Add(-time.Second), false},
		"held after this reading":      {snapshotNow.Add(time.Second), false},
		"held inside the window":       {snapshotLastAt.Add(time.Second), true},
	} {
		_, _, byRoot, _ := darwinCPUFromSnapshot(
			current, previous, 10, darwinRootHeldAt(10, tc.heldSince), snapshotLastAt, snapshotNow, 8, 8)
		_, figure := byRoot[10]
		if figure != tc.wantFigure {
			t.Errorf("%s: root has a figure %v (%v), want %v: with no reading for the root itself only a run held since the last reading can have run all its CPU inside this window, and crediting an older one charges this window for CPU that ran outside it",
				name, figure, byRoot, tc.wantFigure)
		}
	}
}

func TestOwnedCPU_AMeasuredTreeLosesItsFigureWhenAFirstSeenChildOverrunsTheCeiling(t *testing.T) {
	previousAt := time.Unix(100, 0)
	now := previousAt.Add(time.Second)
	rootIdentity := processIdentity{pid: 10, startTicks: 100}
	childIdentity := processIdentity{pid: 11, startTicks: 200}
	held := []OwnedRoot{{PID: 10, HeldSince: previousAt.Add(-time.Hour)}}
	processes := map[int]ownedProcess{
		10: {parentPID: 1, identity: rootIdentity, cpuSeconds: 4, startedAt: previousAt.Add(-time.Hour)},
		11: {parentPID: 10, identity: childIdentity, cpuSeconds: 900, startedAt: previousAt.Add(500 * time.Millisecond)},
	}
	previous := map[processIdentity]cpuSample{rootIdentity: {cpuSeconds: 2, at: previousAt}}

	byRoot, _ := ownedCPUByRoot(
		previous, processes, ownedProcessOwners(held, processes), held,
		previousAt, previousAt, now, 8)

	if figure, reported := byRoot[10]; reported {
		t.Fatalf("tree reports %v cores; want no figure at all: the root's own delta is measured, but a figure short by however much the child ran still subtracts from external, and admission grants against the difference",
			figure)
	}
}
