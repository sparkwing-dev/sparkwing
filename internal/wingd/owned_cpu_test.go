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
	owned := ownedProcessIdentities([]int{10}, processes)

	usage, measured, _ := ownedCPUFromProcesses(previous, processes, owned, now)

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
	owned := ownedProcessIdentities([]int{10}, processes)

	usage, measured, _ := ownedCPUFromProcesses(previous, processes, owned, now)

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
	owned := ownedProcessIdentities([]int{10}, processes)

	usage, measured, next := ownedCPUFromProcesses(previous, processes, owned, now)

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

	owned := ownedProcessIdentities([]int{10, 11}, processes)

	if len(owned) != 2 {
		t.Fatalf("owned identities = %d, want the two-process union", len(owned))
	}
}
