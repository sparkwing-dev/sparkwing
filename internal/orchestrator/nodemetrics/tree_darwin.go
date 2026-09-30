package nodemetrics

import (
	"math"
	"unsafe"

	"golang.org/x/sys/unix"
)

func processSnapshot(root int) (map[int]processSample, bool) {
	entries, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	frequency, freqErr := unix.SysctlUint64("hw.tbfrequency")
	if err != nil || freqErr != nil || frequency == 0 {
		return nil, false
	}
	processes := make(map[int]processSample, len(entries))
	zombies := make(map[int]bool)
	for _, entry := range entries {
		pid := int(entry.Proc.P_pid)
		processes[pid] = processSample{parent: int(entry.Eproc.Ppid)}
		zombies[pid] = entry.Proc.P_stat == 5
	}
	for pid, p := range treeProcesses(root, processes) {
		var usage [18]uint64
		// #nosec G103 -- rusage_info_v1 contains eighteen 64-bit words, including its UUID.
		_, _, errno := unix.Syscall6(336, 9, uintptr(pid), 1, 0, uintptr(unsafe.Pointer(&usage)), 0)
		if errno != 0 || usage[2] > math.MaxUint64-usage[3] || usage[12] > math.MaxUint64-usage[13] || usage[8] > math.MaxInt64 {
			return nil, false
		}
		var cpuOK, reapedOK bool
		p.cpu, cpuOK = cpuTicks(usage[2]+usage[3], frequency)
		p.reaped, reapedOK = cpuTicks(usage[12]+usage[13], frequency)
		p.birth = usage[10]
		p.valid = cpuOK && reapedOK && p.birth != 0
		if !zombies[pid] && usage[11] == 0 {
			p.rss = int64(usage[8])
		}
		processes[pid] = p
	}
	return processes, true
}
