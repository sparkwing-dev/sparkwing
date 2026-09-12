package wingd

import (
	"math"
	"testing"
	"time"
)

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
	owners := ownedProcessOwners([]int{10}, processes)

	byRoot, _ := ownedCPUByRoot(previous, processes, owners, now)
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
	owners := ownedProcessOwners([]int{10}, processes)

	byRoot, _ := ownedCPUByRoot(previous, processes, owners, now)
	usage := sumOwnedCPU(byRoot)

	if _, figure := byRoot[10]; figure || usage != 0 {
		t.Fatalf("recycled PID CPU = %v, root has a figure %v; want no figure for the new identity, which is what an absent key means",
			usage, figure)
	}
}

func TestOwnedCPU_NewChildDoesNotEraseMeasuredParentDelta(t *testing.T) {
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
	owners := ownedProcessOwners([]int{10}, processes)

	byRoot, next := ownedCPUByRoot(previous, processes, owners, now)
	usage := sumOwnedCPU(byRoot)

	if math.Abs(usage-1) > 0.0001 {
		t.Fatalf("owned CPU = %v; want the measured parent delta only", usage)
	}
	if len(next) != 2 {
		t.Fatalf("next baselines = %d, want parent and new child", len(next))
	}
}

func TestOwnedCPU_AStaleParentPIDDoesNotResurrectAMissingRoot(t *testing.T) {
	processes := map[int]ownedProcess{
		9: {parentPID: 10, identity: processIdentity{pid: 9, startTicks: 400}},
	}

	owners := ownedProcessOwners([]int{10}, processes)

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
	owners := ownedProcessOwners([]int{7}, processes)

	byRoot, next := ownedCPUByRoot(previous, processes, owners, now)

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
	owners := ownedProcessOwners([]int{10}, processes)

	byRoot, _ := ownedCPUByRoot(previous, processes, owners, now)

	if math.Abs(byRoot[10]-4) > 0.0001 {
		t.Fatalf("owned CPU by root = %v; want the root's own 1 core plus its child's 3 summed into one tree, not the last one read", byRoot)
	}
}

func TestOwnedCPU_OverlappingRootsCountTheirUnionOnce(t *testing.T) {
	processes := map[int]ownedProcess{
		10: {parentPID: 1, identity: processIdentity{pid: 10, startTicks: 1000}},
		11: {parentPID: 10, identity: processIdentity{pid: 11, startTicks: 1001}},
	}

	owners := ownedProcessOwners([]int{10, 11}, processes)

	if len(owners) != 2 {
		t.Fatalf("owned identities = %d, want the two-process union", len(owners))
	}
	if owners[processIdentity{pid: 10, startTicks: 1000}] != 10 || owners[processIdentity{pid: 11, startTicks: 1001}] != 11 {
		t.Fatalf("owners = %v; want each process charged to its nearest ancestor root, so the union is counted once", owners)
	}
}
