//go:build darwin

package procusage

import (
	"math"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func (*Tree) Read() Snapshot {
	snapshot := Snapshot{Start: time.Now(), Processes: make(map[int]Process)}
	before, ok := processTable()
	if !ok {
		snapshot.End = time.Now()
		return snapshot
	}
	frequency, _ := unix.SysctlUint64("hw.tbfrequency")
	for pid, info := range before {
		var usage [18]uint64
		// #nosec G103 -- rusage_info_v1 is eighteen 64-bit words, including its 16-byte UUID.
		_, _, errno := unix.Syscall6(336, 9, uintptr(pid), 1, 0, uintptr(unsafe.Pointer(&usage)), 0)
		process := Process{Parent: int(info.Eproc.Ppid), ReadAt: time.Now()}
		process.PID = pid
		if errno == 0 {
			process.Birth = usage[10]
			process.IdentityAvailable = process.Birth != 0
			if usage[2] <= math.MaxUint64-usage[3] {
				process.CPU, process.CPUAvailable = ticksDuration(usage[2]+usage[3], frequency)
			}
			if usage[12] <= math.MaxUint64-usage[13] {
				process.ReapedCPU, process.ReapedAvailable = ticksDuration(usage[12]+usage[13], frequency)
			}
			if usage[8] <= math.MaxInt64 {
				process.RSS, process.RSSAvailable = int64(usage[8]), true
			}
		}
		snapshot.Processes[pid] = process
	}
	after, ok := processTable()
	for pid, process := range snapshot.Processes {
		first, last := before[pid], after[pid]
		process.AncestryAvailable = ok && last.Proc.P_pid == first.Proc.P_pid &&
			first.Proc.P_starttime == last.Proc.P_starttime && first.Eproc.Ppid == last.Eproc.Ppid && process.IdentityAvailable
		if !process.AncestryAvailable {
			process.CPUAvailable, process.RSSAvailable, process.ReapedAvailable = false, false, false
		}
		snapshot.Processes[pid] = process
	}
	snapshot.Available = ok
	snapshot.End = time.Now()
	return snapshot
}

func processTable() (map[int]unix.KinfoProc, bool) {
	raw, err := unix.SysctlRaw("kern.proc.all")
	size := int(unsafe.Sizeof(unix.KinfoProc{}))
	if err != nil || len(raw) == 0 || len(raw)%size != 0 {
		return nil, false
	}
	result := make(map[int]unix.KinfoProc, len(raw)/size)
	for offset := 0; offset < len(raw); offset += size {
		// #nosec G103 -- the kernel buffer contains complete, size-checked kinfo_proc records.
		entry := *(*unix.KinfoProc)(unsafe.Pointer(&raw[offset]))
		result[int(entry.Proc.P_pid)] = entry
	}
	return result, true
}
