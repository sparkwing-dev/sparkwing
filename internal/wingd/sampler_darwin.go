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
func (p *procSampler) sample(pid int) (float64, bool) {
	procs, ok := darwinProcesses()
	if !ok {
		return 0, false
	}
	children := map[int][]int{}
	byPID := map[int]unix.KinfoProc{}
	for _, kp := range procs {
		processID := int(kp.Proc.P_pid)
		byPID[processID] = kp
		children[int(kp.Eproc.Ppid)] = append(children[int(kp.Eproc.Ppid)], processID)
	}
	if _, ok := byPID[pid]; !ok {
		return 0, false
	}
	tree := []int{pid}
	for i := 0; i < len(tree); i++ {
		tree = append(tree, children[tree[i]]...)
	}
	if len(tree) > 1 {
		return 1, true
	}
	return darwinProcessCPUFraction(tree)
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	args := []string{"-o", "pcpu=", "-p", darwinPIDList(pids)}
	out, err := exec.CommandContext(ctx, "ps", args...).Output()
	if err != nil {
		return 0, false
	}
	var total float64
	var sampled bool
	for _, field := range strings.Fields(string(out)) {
		percent, err := strconv.ParseFloat(field, 64)
		if err != nil {
			continue
		}
		total += percent / 100.0
		sampled = true
	}
	return total, sampled
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
