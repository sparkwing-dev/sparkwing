//go:build darwin

package procgroup

import (
	"strconv"
	"unsafe"

	"golang.org/x/sys/unix"
)

// nativeProcessTable reads the process table straight from the kernel.
// The `ps` fork it replaces costs about twenty milliseconds of wall time
// per listing; this sysctl costs a few hundred microseconds and carries
// the leader birth times too, so a guard sweep can answer identity from
// the same snapshot it counts members in -- one kernel view per sweep,
// consistent with itself, instead of one per session plus a fork.
func nativeProcessTable(withSessions bool) ([]Info, bool) {
	raw, err := unix.SysctlRaw("kern.proc.all")
	if err != nil {
		return nil, false
	}
	size := int(unsafe.Sizeof(unix.KinfoProc{}))
	if size == 0 || len(raw) < size {
		return nil, false
	}
	processes := make([]Info, 0, len(raw)/size)
	for start := 0; start+size <= len(raw); start += size {
		process := *(*unix.KinfoProc)(unsafe.Pointer(&raw[start]))
		pid := int(process.Proc.P_pid)
		if pid <= 0 {
			continue
		}
		sid := 0
		if withSessions {
			// safety: a process that exits between the sysctl and this call reports no session, exactly as the `ps` path's ignored error did; the caller treats session zero as "not in the guarded session".
			sid, _ = unix.Getsid(pid)
		}
		processes = append(processes, Info{
			PID:     pid,
			Group:   int(process.Eproc.Pgid),
			Session: sid,
			State:   darwinProcessState(process.Proc.P_stat),
			Birth:   darwinBirthToken(process),
		})
	}
	return processes, true
}

// darwinProcessState maps a kernel process state onto the single-letter
// codes `ps` prints, which is what [processTerminated] reads.
func darwinProcessState(stat int8) string {
	switch stat {
	case 1:
		return "I"
	case 2:
		return "R"
	case 3:
		return "S"
	case 4:
		return "T"
	case 5:
		return "Z"
	default:
		return "?"
	}
}

func darwinBirthToken(process unix.KinfoProc) string {
	return strconv.FormatInt(process.Proc.P_starttime.Sec, 10) + ":" +
		strconv.FormatInt(int64(process.Proc.P_starttime.Usec), 10)
}
