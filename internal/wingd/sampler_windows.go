//go:build windows

package wingd

import (
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const windowsFiletimeTicksPerSecond = 10_000_000

var globalMemoryStatusEx = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

type windowsMemoryStatusEx struct {
	length                   uint32
	memoryLoad               uint32
	totalPhysical            uint64
	availablePhysical        uint64
	totalPageFile            uint64
	availablePageFile        uint64
	totalVirtual             uint64
	availableVirtual         uint64
	availableExtendedVirtual uint64
}

type windowsProc struct {
	parentPID  int
	startTicks uint64
	cpuSeconds float64
	measured   bool
}

func sampleHost() (HostStat, error) {
	stat := HostStat{TotalCores: float64(runtime.NumCPU())}
	status := windowsMemoryStatusEx{length: uint32(unsafe.Sizeof(windowsMemoryStatusEx{}))}
	result, _, _ := globalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status)))
	if result == 0 {
		return stat, nil
	}
	stat.TotalMemoryBytes = status.totalPhysical
	stat.FreeMemoryBytes = status.availablePhysical
	stat.MemoryMeasured = status.totalPhysical > 0 && status.availablePhysical <= status.totalPhysical
	return stat, nil
}

func (p *procSampler) sample(pid int) (ProcUsage, bool) {
	usages := p.sampleMany([]int{pid})
	usage, ok := usages[pid]
	return usage, ok
}

func (p *procSampler) sampleMany(pids []int) map[int]ProcUsage {
	procs, ok := windowsProcesses()
	if !ok {
		return nil
	}
	now := time.Now()
	children := windowsProcessChildren(procs)
	trees := make(map[int][]int, len(pids))
	for _, pid := range pids {
		if _, ok := procs[pid]; !ok {
			p.forget(pid)
			continue
		}
		trees[pid] = collectSubtree(pid, children)
	}

	usages := make(map[int]ProcUsage, len(trees))
	p.mu.Lock()
	defer p.mu.Unlock()
	previous := make(map[int]cpuSample, len(p.last))
	for pid, sample := range p.last {
		previous[pid] = sample
	}
	nextSamples := map[int]cpuSample{}
	for pid, tree := range trees {
		p.pruneTreeLocked(pid, trackedPIDs(tree))
		if !windowsTreeMeasured(tree, procs) {
			for _, treePID := range tree {
				delete(p.last, treePID)
			}
			continue
		}
		usage := ProcUsage{HasDescendant: len(tree) > 1}
		var sampled bool
		for _, treePID := range tree {
			proc := procs[treePID]
			current := cpuSample{
				cpuSeconds: proc.cpuSeconds,
				at:         now,
				startTicks: proc.startTicks,
			}
			nextSamples[treePID] = current
			prior, seen := previous[treePID]
			if !seen {
				continue
			}
			fraction, ok := processCPUFraction(prior, current)
			if !ok {
				continue
			}
			usage.Fraction += fraction
			sampled = true
		}
		if sampled {
			usages[pid] = usage
		}
	}
	for pid, sample := range nextSamples {
		p.last[pid] = sample
	}
	return usages
}

func (s *ownedProcSampler) sampleOwned(roots []int) (float64, bool) {
	if len(roots) == 0 {
		return 0, true
	}
	procs, ok := windowsProcesses()
	if !ok {
		return 0, false
	}
	children := windowsProcessChildren(procs)
	for _, root := range roots {
		if _, ok := procs[root]; !ok {
			continue
		}
		if !windowsTreeMeasured(collectSubtree(root, children), procs) {
			s.mu.Lock()
			s.last = map[processIdentity]cpuSample{}
			s.mu.Unlock()
			return 0, false
		}
	}
	processes := windowsOwnedProcesses(procs, children)
	owned := ownedProcessIdentities(roots, processes)
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	fraction, measured, next := ownedCPUFromProcesses(s.last, processes, owned, now)
	s.last = next
	return fraction, measured
}

func windowsProcesses() (map[int]windowsProc, bool) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, false
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil, false
	}
	processes := map[int]windowsProc{}
	for {
		if entry.ProcessID > 0 && uint64(entry.ProcessID) <= uint64(int(^uint(0)>>1)) {
			processID := int(entry.ProcessID)
			processes[processID] = windowsProcess(processID, int(entry.ParentProcessID))
		}
		err := windows.Process32Next(snapshot, &entry)
		if err == windows.ERROR_NO_MORE_FILES {
			break
		}
		if err != nil {
			return nil, false
		}
	}
	return processes, true
}

func windowsProcess(processID, parentPID int) windowsProc {
	proc := windowsProc{parentPID: parentPID}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(processID))
	if err != nil {
		return proc
	}
	defer windows.CloseHandle(handle)
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return proc
	}
	proc.startTicks = windowsFiletimeTicks(creation)
	proc.cpuSeconds = float64(windowsFiletimeTicks(kernel)+windowsFiletimeTicks(user)) / windowsFiletimeTicksPerSecond
	proc.measured = true
	return proc
}

func windowsProcessChildren(processes map[int]windowsProc) map[int][]int {
	children := map[int][]int{}
	for processID, proc := range processes {
		parent, parentPresent := processes[proc.parentPID]
		// safety: Toolhelp retains the numeric parent after it exits. Birth order keeps
		// an orphan from becoming the child of a later process that reused the PID.
		if parentPresent && parent.measured && proc.measured && proc.startTicks < parent.startTicks {
			continue
		}
		children[proc.parentPID] = append(children[proc.parentPID], processID)
	}
	return children
}

func windowsOwnedProcesses(processes map[int]windowsProc, children map[int][]int) map[int]ownedProcess {
	parents := make(map[int]int, len(processes))
	for parentPID, childPIDs := range children {
		for _, childPID := range childPIDs {
			parents[childPID] = parentPID
		}
	}
	ownedProcesses := make(map[int]ownedProcess, len(processes))
	for processID, proc := range processes {
		if !proc.measured {
			continue
		}
		ownedProcesses[processID] = ownedProcess{
			parentPID:  parents[processID],
			identity:   processIdentity{pid: processID, startTicks: proc.startTicks},
			cpuSeconds: proc.cpuSeconds,
		}
	}
	return ownedProcesses
}

func windowsTreeMeasured(tree []int, processes map[int]windowsProc) bool {
	for _, processID := range tree {
		if !processes[processID].measured {
			return false
		}
	}
	return true
}

func windowsFiletimeTicks(value windows.Filetime) uint64 {
	return uint64(value.HighDateTime)<<32 | uint64(value.LowDateTime)
}
