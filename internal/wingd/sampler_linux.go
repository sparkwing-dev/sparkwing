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

// linuxClockTicks is the kernel's USER_HZ. It is fixed at 100 on every
// mainstream Linux build, so the stall heuristic reads /proc directly
// rather than pulling in a sysconf dependency for a threshold check.
const linuxClockTicks = 100.0

// sample derives a process tree's CPU as a fraction of one core from the
// change in cumulative user+system time between two /proc readings. The
// first reading for every live pid has no baseline; sample reports once
// at least one process in the tree has two readings.
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
		tree := []int{pid}
		for i := 0; i < len(tree); i++ {
			tree = append(tree, children[tree[i]]...)
		}
		trees[pid] = tree
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
			nextSamples[treePID] = cpuSample{cpuSeconds: proc.cpuSeconds, at: now}
			prev, ok := previous[treePID]
			if !ok {
				continue
			}
			wall := now.Sub(prev.at).Seconds()
			if wall <= 0 {
				continue
			}
			frac := (proc.cpuSeconds - prev.cpuSeconds) / wall
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

func linuxProcessCPUSeconds(pid int) (float64, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	line := string(data)
	rparen := strings.LastIndexByte(line, ')')
	if rparen < 0 || rparen+2 >= len(line) {
		return 0, false
	}
	fields := strings.Fields(line[rparen+2:])
	if len(fields) < 15 {
		return 0, false
	}
	utime, err1 := strconv.ParseFloat(fields[11], 64)
	stime, err2 := strconv.ParseFloat(fields[12], 64)
	cutime, err3 := strconv.ParseFloat(fields[13], 64)
	cstime, err4 := strconv.ParseFloat(fields[14], 64)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return 0, false
	}
	return (utime + stime + cutime + cstime) / linuxClockTicks, true
}

type linuxProc struct {
	parentPID  int
	cpuSeconds float64
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
	line := string(data)
	rparen := strings.LastIndexByte(line, ')')
	if rparen < 0 || rparen+2 >= len(line) {
		return linuxProc{}, false
	}
	fields := strings.Fields(line[rparen+2:])
	if len(fields) < 15 {
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
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return linuxProc{}, false
	}
	return linuxProc{parentPID: parent, cpuSeconds: (utime + stime + cutime + cstime) / linuxClockTicks}, true
}

func linuxProcessTree(root int) ([]int, bool) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, false
	}
	children := map[int][]int{}
	rootFound := false
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		if pid == root {
			rootFound = true
		}
		parent, ok := linuxParentPID(pid)
		if ok {
			children[parent] = append(children[parent], pid)
		}
	}
	if !rootFound {
		return nil, false
	}
	tree := []int{root}
	for i := 0; i < len(tree); i++ {
		tree = append(tree, children[tree[i]]...)
	}
	return tree, true
}

func linuxParentPID(pid int) (int, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	line := string(data)
	rparen := strings.LastIndexByte(line, ')')
	if rparen < 0 || rparen+2 >= len(line) {
		return 0, false
	}
	fields := strings.Fields(line[rparen+2:])
	if len(fields) < 2 {
		return 0, false
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false
	}
	return parent, true
}

// forget drops a dead pid's baseline so the map does not grow without
// bound as runs come and go.
func (p *procSampler) forget(pid int) {
	p.mu.Lock()
	delete(p.last, pid)
	delete(p.tree, pid)
	p.mu.Unlock()
}

func trackedPIDs(pids []int) map[int]struct{} {
	tracked := make(map[int]struct{}, len(pids))
	for _, pid := range pids {
		tracked[pid] = struct{}{}
	}
	return tracked
}

func (p *procSampler) pruneTreeLocked(root int, live map[int]struct{}) {
	for pid := range p.tree[root] {
		if _, ok := live[pid]; !ok {
			delete(p.last, pid)
		}
	}
	p.tree[root] = live
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
