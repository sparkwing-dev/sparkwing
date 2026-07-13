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

// sample finds the root process's descendants from kinfo_proc and sums
// the process tree's CPU percentage through ps, matching the operator
// view on macOS. It reports not-sampled when the process is gone or the
// process table cannot be read.
func (p *procSampler) sample(pid int) (ProcUsage, bool) {
	usages := p.sampleMany([]int{pid})
	usage, ok := usages[pid]
	return usage, ok
}

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
		tree := []int{pid}
		for i := 0; i < len(tree); i++ {
			tree = append(tree, children[tree[i]]...)
		}
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

func darwinProcessCPUFraction(pids []int) (float64, bool) {
	fractions, ok := darwinProcessCPUFractions(pids)
	if !ok {
		return 0, false
	}
	var total float64
	for _, fraction := range fractions {
		total += fraction
	}
	return total, len(fractions) > 0
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
		}
	}

	pageSize := uint64(unix.Getpagesize())
	if free, err := unix.SysctlUint32("vm.page_free_count"); err == nil {
		stat.FreeMemoryBytes = uint64(free) * pageSize
	}
	if spec, err := unix.SysctlUint32("vm.page_speculative_count"); err == nil {
		stat.FreeMemoryBytes += uint64(spec) * pageSize
	}

	// hack: page_free_count counts only truly free pages, so on macOS -- which
	// parks most of RAM in reclaimable cache and the compressor -- it reads as
	// near-zero even with gigabytes available, which would pin memory headroom
	// at zero and wedge every memory-costed admission. memorystatus_level is
	// the kernel's own percent-available figure (the darwin analog of Linux
	// MemAvailable), so prefer it when sane.
	if level, err := unix.SysctlUint32("kern.memorystatus_level"); err == nil && level > 0 && level <= 100 {
		stat.FreeMemoryBytes = uint64(float64(stat.TotalMemoryBytes) * float64(level) / 100.0)
	}

	if stat.TotalMemoryBytes == 0 {
		return stat, fmt.Errorf("wingd: hw.memsize unavailable")
	}
	return stat, nil
}
