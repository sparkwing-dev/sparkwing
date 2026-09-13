//go:build windows

package wingd

import (
	"os"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsMemoryStatusLayoutMatchesAPI(t *testing.T) {
	if got := unsafe.Sizeof(windowsMemoryStatusEx{}); got != 64 {
		t.Fatalf("MEMORYSTATUSEX size = %d, want 64", got)
	}
}

func TestWindowsFiletimeTicksPreservesBothWords(t *testing.T) {
	value := windows.Filetime{HighDateTime: 0x01234567, LowDateTime: 0x89abcdef}
	if got, want := windowsFiletimeTicks(value), uint64(0x0123456789abcdef); got != want {
		t.Fatalf("FILETIME ticks = %#x, want %#x", got, want)
	}
}

func TestWindowsHostMemoryIsMeasured(t *testing.T) {
	stat, err := sampleHost()
	if err != nil {
		t.Fatal(err)
	}
	if !stat.MemoryMeasured || stat.TotalMemoryBytes == 0 || stat.FreeMemoryBytes > stat.TotalMemoryBytes {
		t.Fatalf("host memory = %#v, want a bounded physical-memory measurement", stat)
	}
}

func TestWindowsProcessSnapshotMeasuresThisProcess(t *testing.T) {
	processes, ok := windowsProcesses()
	if !ok {
		t.Fatal("process snapshot was unavailable")
	}
	process, ok := processes[os.Getpid()]
	if !ok || !process.measured || process.startTicks == 0 {
		t.Fatalf("this process = %#v, present %v; want an identified measurement", process, ok)
	}
}

func TestWindowsProcessChildrenRejectsAReusedParentPID(t *testing.T) {
	processes := map[int]windowsProc{
		10: {startTicks: 200, measured: true},
		11: {parentPID: 10, startTicks: 100, measured: true},
		12: {parentPID: 10, startTicks: 300, measured: true},
	}
	children := windowsProcessChildren(processes)
	if got := children[10]; len(got) != 1 || got[0] != 12 {
		t.Fatalf("children of reused PID = %v, want only the process born after it", got)
	}
}

func TestWindowsTreeWithAnUnreadableProcessIsUnmeasured(t *testing.T) {
	processes := map[int]windowsProc{
		10: {measured: true},
		11: {parentPID: 10},
	}
	if windowsTreeMeasured([]int{10, 11}, processes) {
		t.Fatal("a tree containing an unreadable process was reported as measured")
	}
}

func TestWindowsOwnedCPUDoesNotReattachAnOrphanToAReusedRootPID(t *testing.T) {
	firstAt := time.Unix(100, 0)
	rootIdentity := processIdentity{pid: 10, startTicks: 200}
	childIdentity := processIdentity{pid: 12, startTicks: 300}
	first := map[int]windowsProc{
		10: {startTicks: 200, cpuSeconds: 1, measured: true},
		11: {parentPID: 10, startTicks: 100, cpuSeconds: 20, measured: true},
		12: {parentPID: 10, startTicks: 300, cpuSeconds: 2, measured: true},
	}
	firstOwned := windowsOwnedProcesses(first, windowsProcessChildren(first))
	identities := ownedProcessOwners(heldRoots(10), firstOwned)
	if _, present := identities[processIdentity{pid: 11, startTicks: 100}]; present {
		t.Fatal("the older orphan was attached to a root that reused its parent PID")
	}
	byRoot, previous := ownedCPUByRoot(nil, firstOwned, identities, heldRoots(10), firstAt.Add(-time.Second), firstAt.Add(-time.Second), firstAt, 8)
	if _, figure := byRoot[10]; figure {
		t.Fatal("the first sample reported CPU for a root with no baseline")
	}

	second := map[int]windowsProc{
		10: {startTicks: 200, cpuSeconds: 2, measured: true},
		11: {parentPID: 10, startTicks: 100, cpuSeconds: 120, measured: true},
		12: {parentPID: 10, startTicks: 300, cpuSeconds: 4, measured: true},
	}
	secondOwned := windowsOwnedProcesses(second, windowsProcessChildren(second))
	identities = ownedProcessOwners(heldRoots(10), secondOwned)
	byRoot, _ = ownedCPUByRoot(previous, secondOwned, identities, heldRoots(10), firstAt.Add(time.Second).Add(-time.Second), firstAt.Add(time.Second).Add(-time.Second), firstAt.Add(time.Second), 8)
	if byRoot[10] != 3 {
		t.Fatalf("owned CPU by root = %v; want root plus valid child only", byRoot)
	}
	if _, present := identities[rootIdentity]; !present {
		t.Fatal("the reused root identity was lost")
	}
	if _, present := identities[childIdentity]; !present {
		t.Fatal("the valid newer child identity was lost")
	}
}
