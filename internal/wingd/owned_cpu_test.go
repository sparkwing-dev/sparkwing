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

	byRoot, measured, _ := ownedCPUByRoot(previous, processes, owners, now)
	usage := sumOwnedCPU(byRoot)

	if !measured || math.Abs(usage-1) > 0.0001 {
		t.Fatalf("owned CPU = %v, measured %v; want one parent core without re-counting the reaped child", usage, measured)
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

	byRoot, measured, _ := ownedCPUByRoot(previous, processes, owners, now)
	usage := sumOwnedCPU(byRoot)

	if measured || usage != 0 {
		t.Fatalf("recycled PID CPU = %v, measured %v; want an unmeasured new identity", usage, measured)
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

	byRoot, measured, next := ownedCPUByRoot(previous, processes, owners, now)
	usage := sumOwnedCPU(byRoot)

	if !measured || math.Abs(usage-1) > 0.0001 {
		t.Fatalf("owned CPU = %v, measured %v; want the measured parent delta only", usage, measured)
	}
	if len(next) != 2 {
		t.Fatalf("next baselines = %d, want parent and new child", len(next))
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
