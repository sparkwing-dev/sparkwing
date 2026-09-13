package wingd

import (
	"math"
	"testing"
	"time"
)

func heldRoots(pids ...int) []OwnedRoot {
	roots := make([]OwnedRoot, 0, len(pids))
	for _, pid := range pids {
		roots = append(roots, OwnedRoot{PID: pid, HeldSince: time.Unix(0, 0)})
	}
	return roots
}

func TestOwnedCPU_ReapedChildIsNotCountedAgainThroughParent(t *testing.T) {
	previousAt := time.Unix(100, 0)
	now := previousAt.Add(time.Second)
	parent := processIdentity{pid: 10, startTicks: 1000}
	child := processIdentity{pid: 11, startTicks: 1001}
	previous := map[processIdentity]cpuSample{
		parent: {cpuSeconds: 1, at: previousAt},
		child:  {cpuSeconds: 5, at: previousAt},
	}
	processes := map[int]ownedProcess{
		10: {parentPID: 1, identity: parent, cpuSeconds: 2},
	}
	owners := ownedProcessOwners(heldRoots(10), processes)

	byRoot, _ := ownedCPUByRoot(previous, processes, owners, heldRoots(10), now.Add(-time.Second), now, 8)
	usage := sumOwnedCPU(byRoot)

	if math.Abs(usage-1) > 0.0001 {
		t.Fatalf("owned CPU = %v; want one parent core without re-counting the reaped child", usage)
	}
}

func TestOwnedCPU_PIDReuseNeedsANewBaseline(t *testing.T) {
	previousAt := time.Unix(100, 0)
	now := previousAt.Add(time.Second)
	previous := map[processIdentity]cpuSample{
		{pid: 10, startTicks: 1000}: {cpuSeconds: 1, at: previousAt},
	}
	processes := map[int]ownedProcess{
		10: {parentPID: 1, identity: processIdentity{pid: 10, startTicks: 2000}, cpuSeconds: 100},
	}
	owners := ownedProcessOwners(heldRoots(10), processes)

	byRoot, _ := ownedCPUByRoot(previous, processes, owners, heldRoots(10), now.Add(-time.Second), now, 8)
	usage := sumOwnedCPU(byRoot)

	if _, figure := byRoot[10]; figure || usage != 0 {
		t.Fatalf("recycled PID CPU = %v, root has a figure %v; want no figure for the new identity, which is what an absent key means",
			usage, figure)
	}
}

func TestOwnedCPU_NewChildIsCreditedBesideTheMeasuredParentDelta(t *testing.T) {
	previousAt := time.Unix(100, 0)
	now := previousAt.Add(time.Second)
	parent := processIdentity{pid: 10, startTicks: 1000}
	previous := map[processIdentity]cpuSample{
		parent: {cpuSeconds: 1, at: previousAt},
	}
	processes := map[int]ownedProcess{
		10: {parentPID: 1, identity: parent, cpuSeconds: 2},
		11: {parentPID: 10, identity: processIdentity{pid: 11, startTicks: 1001}, cpuSeconds: 4},
	}
	owners := ownedProcessOwners(heldRoots(10), processes)

	byRoot, next := ownedCPUByRoot(previous, processes, owners, heldRoots(10), now.Add(-time.Second), now, 8)
	usage := sumOwnedCPU(byRoot)

	if math.Abs(usage-5) > 0.0001 {
		t.Fatalf("owned CPU = %v; want the parent's measured 1 plus the new child's whole 4: a child the sampler has not seen before ran all of its CPU inside this window, so crediting it none is what charges a run's own work to the machine",
			usage)
	}
	if len(next) != 2 {
		t.Fatalf("next baselines = %d, want parent and new child", len(next))
	}
}

func TestOwnedCPU_AProcessOlderThanTheWindowIsNotCreditedToIt(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	cases := map[string]map[int]ownedProcess{
		"a long-running process adopted into a tree just being watched": {
			10: {parentPID: 1, identity: processIdentity{pid: 10, startTicks: 900}, cpuSeconds: 0.1},
			11: {parentPID: 10, identity: processIdentity{pid: 11, startTicks: 1}, cpuSeconds: 3600},
		},
		"a run reattaching after a restart, with no reading and a fresh hold": {
			10: {parentPID: 1, identity: processIdentity{pid: 10, startTicks: 7}, cpuSeconds: 900},
		},
	}
	for name, processes := range cases {
		roots := []OwnedRoot{{PID: 10, HeldSince: now.Add(-time.Second)}}
		owners := ownedProcessOwners(roots, processes)

		byRoot, _ := ownedCPUByRoot(nil, processes, owners, roots, now.Add(-time.Second), now, 8)

		if _, figure := byRoot[10]; figure {
			t.Errorf("%s: owned CPU by root = %v; want no figure at all: more CPU than the window could hold proves the process predates it, and crediting that total would understate external and over-admit",
				name, byRoot)
		}
	}
}

func TestOwnedCPU_AStaleParentPIDDoesNotResurrectAMissingRoot(t *testing.T) {
	processes := map[int]ownedProcess{
		9: {parentPID: 10, identity: processIdentity{pid: 9, startTicks: 400}},
	}

	owners := ownedProcessOwners(heldRoots(10), processes)

	if len(owners) != 0 {
		t.Fatalf("owners = %v; want none: pid 10 is gone from the process table, so a survivor still naming it as its parent belongs to no root this daemon holds",
			owners)
	}
}

func TestOwnedCPU_ARootWithoutItsOwnBaselineReportsNoFigure(t *testing.T) {
	previousAt := time.Unix(100, 0)
	now := previousAt.Add(time.Second)
	child := processIdentity{pid: 8, startTicks: 100}
	previous := map[processIdentity]cpuSample{
		child: {cpuSeconds: 7, at: previousAt},
	}
	processes := map[int]ownedProcess{
		7: {parentPID: 1, identity: processIdentity{pid: 7, startTicks: 900}, cpuSeconds: 5},
		8: {parentPID: 7, identity: child, cpuSeconds: 9},
	}
	owners := ownedProcessOwners(heldRoots(7), processes)

	byRoot, next := ownedCPUByRoot(previous, processes, owners, heldRoots(10), now.Add(-time.Second), now, 8)

	if _, figure := byRoot[7]; figure {
		t.Fatalf("owned CPU by root = %v; want no figure for a root that re-execed: its own process has no baseline, so the tree's sum covers only the surviving child and is silently short",
			byRoot)
	}
	if _, carried := next[processes[7].identity]; !carried {
		t.Fatal("the new root's baseline was not carried forward, so it would report no figure on the next reading either")
	}
}

func TestOwnedCPU_ACyclicParentTableTerminates(t *testing.T) {
	done := make(chan map[int]int, 1)
	go func() {
		done <- ownersByNearestRoot(map[int]int{10: 11, 11: 12, 12: 10, 13: 10}, map[int]struct{}{13: {}})
	}()
	select {
	case owners := <-done:
		if owners[13] != 13 {
			t.Fatalf("owners = %v; want the root outside the cycle still resolved", owners)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resolving a cyclic parent table did not terminate: the sample loop would spin on a bad process table")
	}
}

func TestOwnedCPU_SumsEveryMeasuredProcessUnderOneRoot(t *testing.T) {
	previousAt := time.Unix(100, 0)
	now := previousAt.Add(time.Second)
	root := processIdentity{pid: 10, startTicks: 1000}
	child := processIdentity{pid: 11, startTicks: 1001}
	previous := map[processIdentity]cpuSample{
		root:  {cpuSeconds: 1, at: previousAt},
		child: {cpuSeconds: 2, at: previousAt},
	}
	processes := map[int]ownedProcess{
		10: {parentPID: 1, identity: root, cpuSeconds: 2},
		11: {parentPID: 10, identity: child, cpuSeconds: 5},
	}
	owners := ownedProcessOwners(heldRoots(10), processes)

	byRoot, _ := ownedCPUByRoot(previous, processes, owners, heldRoots(10), now.Add(-time.Second), now, 8)

	if math.Abs(byRoot[10]-4) > 0.0001 {
		t.Fatalf("owned CPU by root = %v; want the root's own 1 core plus its child's 3 summed into one tree, not the last one read", byRoot)
	}
}

func TestOwnedCPU_OverlappingRootsCountTheirUnionOnce(t *testing.T) {
	processes := map[int]ownedProcess{
		10: {parentPID: 1, identity: processIdentity{pid: 10, startTicks: 1000}},
		11: {parentPID: 10, identity: processIdentity{pid: 11, startTicks: 1001}},
	}

	owners := ownedProcessOwners(heldRoots(10, 11), processes)

	if len(owners) != 2 {
		t.Fatalf("owned identities = %d, want the two-process union", len(owners))
	}
	if owners[processIdentity{pid: 10, startTicks: 1000}] != 10 || owners[processIdentity{pid: 11, startTicks: 1001}] != 11 {
		t.Fatalf("owners = %v; want each process charged to its nearest ancestor root, so the union is counted once", owners)
	}
}

func TestOwnedCPU_ReholdingATreeResumesFromTheFigureAlreadyTaken(t *testing.T) {
	firstAt := time.Unix(100, 0)
	releasedAt := firstAt.Add(10 * time.Second)
	reheldAt := releasedAt.Add(10 * time.Second)
	identity := processIdentity{pid: 10, startTicks: 1000}
	// The tree has burned 40 CPU-seconds before the daemon ever reads it and
	// burns one more core's worth across each ten-second reading.
	process := func(cpuSeconds float64) map[int]ownedProcess {
		return map[int]ownedProcess{10: {parentPID: 1, identity: identity, cpuSeconds: cpuSeconds}}
	}
	held := []OwnedRoot{{PID: 10, HeldSince: firstAt.Add(-time.Hour)}}

	processes := process(50)
	_, afterFirst := ownedCPUByRoot(
		map[processIdentity]cpuSample{identity: {cpuSeconds: 40, at: firstAt}},
		processes, ownedProcessOwners(held, processes), held, firstAt, releasedAt, 8)

	// The run is released, so the daemon owns nothing and asks about no roots.
	processes = process(60)
	_, afterRelease := ownedCPUByRoot(afterFirst, processes, nil, nil, releasedAt, reheldAt, 8)

	// The same tree is held again, inside this reading's window, which is the one
	// case the sampler will credit a tree it has no baseline for.
	rehold := []OwnedRoot{{PID: 10, HeldSince: reheldAt.Add(time.Second)}}
	processes = process(70)
	byRoot, _ := ownedCPUByRoot(
		afterRelease, processes, ownedProcessOwners(rehold, processes), rehold,
		reheldAt, reheldAt.Add(10*time.Second), 8)

	if math.Abs(byRoot[10]-1) > 0.0001 {
		t.Fatalf("re-held tree CPU = %v cores; want the one core it ran this window, not its whole lifetime spread over it",
			byRoot[10])
	}
}

func TestOwnedCPU_ATreeThatRanNothingReportsZeroRatherThanNoReading(t *testing.T) {
	previousAt := time.Unix(100, 0)
	now := previousAt.Add(time.Second)
	identity := processIdentity{pid: 10, startTicks: 1000}
	previous := map[processIdentity]cpuSample{identity: {cpuSeconds: 7, at: previousAt}}
	processes := map[int]ownedProcess{10: {parentPID: 1, identity: identity, cpuSeconds: 7}}
	owners := ownedProcessOwners(heldRoots(10), processes)

	byRoot, _ := ownedCPUByRoot(previous, processes, owners, heldRoots(10), previousAt, now, 8)

	figure, reported := byRoot[10]
	if !reported || figure != 0 {
		t.Fatalf("idle tree reports %v with a figure %v; want zero cores reported, because an absent key means no reading and would charge the host for a run that ran nothing",
			figure, reported)
	}
}
