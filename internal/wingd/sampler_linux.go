//go:build linux

package wingd

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const linuxClockTicks = 100.0

func (p *procSampler) sample(pid int) (ProcUsage, bool) {
	usages := p.sampleMany([]int{pid})
	usage, ok := usages[pid]
	return usage, ok
}

func (p *procSampler) sampleMany(pids []int) map[int]ProcUsage {
	procs, ok := linuxProcesses()
	if !ok {
		return nil
	}
	now := time.Now()

	children := map[int][]int{}
	for processID, proc := range procs {
		children[proc.parentPID] = append(children[proc.parentPID], processID)
	}

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
		usage := ProcUsage{HasDescendant: len(tree) > 1}
		var sampled bool
		p.pruneTreeLocked(pid, trackedPIDs(tree))
		for _, treePID := range tree {
			proc, ok := procs[treePID]
			if !ok {
				continue
			}
			current := cpuSample{
				cpuSeconds: proc.cpuSeconds,
				at:         now,
				startTicks: proc.startTicks,
			}
			nextSamples[treePID] = current
			prev, ok := previous[treePID]
			if !ok {
				continue
			}
			frac, ok := processCPUFraction(prev, current)
			if !ok {
				continue
			}
			if frac > 0 {
				usage.Fraction += frac
			}
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

func (s *ownedProcSampler) sampleOwned(roots []OwnedRoot, arbitratedCores float64) (map[int]float64, bool) {
	scanStart := time.Now()
	procs, ok := linuxProcesses()
	if !ok {
		return nil, false
	}
	now := time.Now()
	uptime := linuxUptime()
	processes := make(map[int]ownedProcess, len(procs))
	for processID, proc := range procs {
		processes[processID] = ownedProcess{
			parentPID:  proc.parentPID,
			identity:   processIdentity{pid: processID, startTicks: proc.startTicks},
			cpuSeconds: proc.selfCPUSeconds,
			startedAt:  linuxProcessStart(now, uptime, proc.startTicks),
		}
	}
	owners := ownedProcessOwners(roots, processes)
	s.mu.Lock()
	defer s.mu.Unlock()
	byRoot, next := ownedCPUByRoot(s.last, processes, owners, roots, s.lastAt, s.seenSince, now, arbitratedCores)
	s.last, s.lastAt, s.seenSince = next, now, scanStart
	return byRoot, true
}

type linuxProc struct {
	parentPID      int
	startTicks     uint64
	selfCPUSeconds float64
	cpuSeconds     float64
}

func linuxProcesses() (map[int]linuxProc, bool) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, false
	}
	procs := map[int]linuxProc{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		proc, ok := linuxProcess(pid)
		if ok {
			procs[pid] = proc
		}
	}
	return procs, true
}

func linuxProcess(pid int) (linuxProc, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return linuxProc{}, false
	}
	return parseLinuxProcessStat(string(data))
}

func parseLinuxProcessStat(line string) (linuxProc, bool) {
	rparen := strings.LastIndexByte(line, ')')
	if rparen < 0 || rparen+2 >= len(line) {
		return linuxProc{}, false
	}
	fields := strings.Fields(line[rparen+2:])
	if len(fields) < 20 {
		return linuxProc{}, false
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return linuxProc{}, false
	}
	utime, err1 := strconv.ParseFloat(fields[11], 64)
	stime, err2 := strconv.ParseFloat(fields[12], 64)
	cutime, err3 := strconv.ParseFloat(fields[13], 64)
	cstime, err4 := strconv.ParseFloat(fields[14], 64)
	startTicks, err5 := strconv.ParseUint(fields[19], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil {
		return linuxProc{}, false
	}
	return linuxProc{
		parentPID:      parent,
		startTicks:     startTicks,
		selfCPUSeconds: (utime + stime) / linuxClockTicks,
		cpuSeconds:     (utime + stime + cutime + cstime) / linuxClockTicks,
	}, true
}

func sampleHost() (HostStat, error) {
	stat := HostStat{TotalCores: float64(runtime.NumCPU())}

	var si unix.Sysinfo_t
	if err := unix.Sysinfo(&si); err != nil {
		return stat, err
	}
	unit := uint64(si.Unit)
	if unit == 0 {
		unit = 1
	}
	stat.TotalMemoryBytes = uint64(si.Totalram) * unit
	stat.FreeMemoryBytes = uint64(si.Freeram) * unit
	stat.LoadAverage = float64(si.Loads[0]) / 65536.0
	stat.LoadMeasured = true
	stat.MemoryMeasured = true

	if avail, ok := readMemAvailable(); ok {
		stat.FreeMemoryBytes = avail
	}
	return stat, nil
}

func readMemAvailable() (uint64, bool) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if !bytes.HasPrefix([]byte(line), []byte("MemAvailable:")) {
			continue
		}
		fields := bytes.Fields([]byte(line))
		if len(fields) < 2 {
			return 0, false
		}
		kb, err := strconv.ParseUint(string(fields[1]), 10, 64)
		if err != nil {
			return 0, false
		}
		return kb * 1024, true
	}
	return 0, false
}

func linuxProcessStart(now time.Time, uptimeSeconds float64, startTicks uint64) time.Time {
	// safety: the returned time keeps now's monotonic reading, which is what makes
	// a later comparison survive a clock adjustment. Rebuilding it from a wall
	// value, or routing it through UTC or Round, strips that and nothing goes red.
	if uptimeSeconds <= 0 {
		return time.Time{}
	}
	age := uptimeSeconds - float64(startTicks)/linuxClockTicks
	if age < 0 {
		return time.Time{}
	}
	return now.Add(-time.Duration(age * float64(time.Second)))
}

func linuxUptime() float64 {
	// safety: a zero return leaves every process undated, so a tree the daemon
	// has no reading for goes unmeasured rather than credited on a bad date.
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	seconds, ok := parseProcUptime(string(data))
	if !ok {
		return 0
	}
	return seconds
}
