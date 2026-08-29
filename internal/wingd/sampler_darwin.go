//go:build darwin

package wingd

import (
	"context"
	"encoding/binary"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func (p *procSampler) sample(pid int) (ProcUsage, bool) {
	usages := p.sampleMany([]int{pid})
	usage, ok := usages[pid]
	return usage, ok
}

// hack: kinfo_proc's own P_pctcpu field is unmaintained on current macOS
// releases -- it reads zero even for a pegged process -- so the CPU
// percentages come from ps while the sysctl supplies only the tree shape.
func (p *procSampler) sampleMany(pids []int) map[int]ProcUsage {
	procs, ok := darwinProcesses()
	if !ok {
		return nil
	}
	children := map[int][]int{}
	byPID := map[int]unix.KinfoProc{}
	for _, kp := range procs {
		processID := int(kp.Proc.P_pid)
		byPID[processID] = kp
		children[int(kp.Eproc.Ppid)] = append(children[int(kp.Eproc.Ppid)], processID)
	}

	trees := make(map[int][]int, len(pids))
	seen := map[int]struct{}{}
	allPIDs := make([]int, 0, len(pids))
	for _, pid := range pids {
		if _, ok := byPID[pid]; !ok {
			continue
		}
		tree := collectSubtree(pid, children)
		trees[pid] = tree
		for _, treePID := range tree {
			if _, ok := seen[treePID]; ok {
				continue
			}
			seen[treePID] = struct{}{}
			allPIDs = append(allPIDs, treePID)
		}
	}
	cpu, ok := darwinProcessCPUFractions(allPIDs)
	if !ok {
		return nil
	}

	usages := make(map[int]ProcUsage, len(trees))
	for pid, tree := range trees {
		usage := ProcUsage{HasDescendant: len(tree) > 1}
		var sampled bool
		for _, treePID := range tree {
			fraction, ok := cpu[treePID]
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
	return usages
}

func (s *ownedProcSampler) sampleOwned(roots []int) (float64, bool) {
	if len(roots) == 0 {
		return 0, true
	}
	procs, ok := darwinProcesses()
	if !ok {
		return 0, false
	}
	children := map[int][]int{}
	byPID := map[int]struct{}{}
	for _, proc := range procs {
		processID := int(proc.Proc.P_pid)
		byPID[processID] = struct{}{}
		children[int(proc.Eproc.Ppid)] = append(children[int(proc.Eproc.Ppid)], processID)
	}
	owned := map[int]struct{}{}
	for _, root := range roots {
		if _, ok := byPID[root]; !ok {
			continue
		}
		for _, processID := range collectSubtree(root, children) {
			owned[processID] = struct{}{}
		}
	}
	pids := make([]int, 0, len(owned))
	for processID := range owned {
		pids = append(pids, processID)
	}
	cpu, ok := darwinProcessCPUFractions(pids)
	if !ok {
		return 0, false
	}
	var fraction float64
	for _, usage := range cpu {
		fraction += usage
	}
	return fraction, true
}

func (p *platformSampler) SampleWithOwned(roots []int) (HostStat, float64, bool, error) {
	stat, err := sampleHost()
	if err != nil {
		return stat, 0, false, err
	}
	snapshot, ok := darwinProcessCPUSnapshot()
	if !ok {
		return stat, 0, false, nil
	}

	now := time.Now()
	previous, previousAt := p.darwinPrev, p.darwinPrevAt
	p.darwinPrev, p.darwinPrevAt = snapshot, now
	elapsedSeconds := 0.0
	if !previousAt.IsZero() {
		elapsedSeconds = now.Sub(previousAt).Seconds()
	}
	host, hostMeasured, owned, ownedMeasured := darwinCPUFromSnapshot(
		snapshot,
		previous,
		elapsedSeconds,
		roots,
		stat.TotalCores,
	)
	stat.BusyCores = host
	stat.CPUMeasured = hostMeasured
	return stat, owned, ownedMeasured, nil
}

func darwinProcessCPUSnapshot() (map[int]darwinCPUProcess, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ps", "-Ao", "pid=,ppid=,time=").Output()
	if err != nil {
		return nil, false
	}
	return parseDarwinCPUSnapshot(string(output))
}

func darwinProcesses() ([]unix.KinfoProc, bool) {
	raw, err := unix.SysctlRaw("kern.proc.all")
	if err != nil {
		return nil, false
	}
	size := int(unsafe.Sizeof(unix.KinfoProc{}))
	if size == 0 || len(raw) < size {
		return nil, false
	}
	count := len(raw) / size
	procs := make([]unix.KinfoProc, 0, count)
	for i := 0; i < count; i++ {
		start := i * size
		procs = append(procs, *(*unix.KinfoProc)(unsafe.Pointer(&raw[start])))
	}
	return procs, true
}

func darwinProcessCPUFractions(pids []int) (map[int]float64, bool) {
	if len(pids) == 0 {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	args := []string{"-o", "pid=", "-o", "pcpu=", "-p", darwinPIDList(pids)}
	out, err := exec.CommandContext(ctx, "ps", args...).Output()
	if err != nil {
		return nil, false
	}
	fractions := map[int]float64{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		percent, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		fractions[pid] = percent / 100.0
	}
	return fractions, len(fractions) > 0
}

func darwinPIDList(pids []int) string {
	parts := make([]string, 0, len(pids))
	for _, pid := range pids {
		parts = append(parts, strconv.Itoa(pid))
	}
	return strings.Join(parts, ",")
}

func sampleHost() (HostStat, error) {
	stat := HostStat{TotalCores: float64(runtime.NumCPU())}

	if mem, err := unix.SysctlUint64("hw.memsize"); err == nil {
		stat.TotalMemoryBytes = mem
	}

	if raw, err := unix.SysctlRaw("vm.loadavg"); err == nil && len(raw) >= 24 {
		ldavg := binary.LittleEndian.Uint32(raw[0:4])
		fscale := binary.LittleEndian.Uint64(raw[16:24])
		if fscale > 0 {
			stat.LoadAverage = float64(ldavg) / float64(fscale)
			stat.LoadMeasured = true
		}
	}

	level, err := unix.SysctlUint32("kern.memorystatus_level")
	stat.FreeMemoryBytes, stat.MemoryMeasured = darwinFreeMemory(stat.TotalMemoryBytes, level, err == nil)

	if stat.TotalMemoryBytes == 0 {
		return stat, fmt.Errorf("wingd: hw.memsize unavailable")
	}
	return stat, nil
}

func darwinFreeMemory(total uint64, level uint32, read bool) (uint64, bool) {
	if !read || level > 100 {
		return 0, false
	}
	return uint64(float64(total) * float64(level) / 100.0), true
}
