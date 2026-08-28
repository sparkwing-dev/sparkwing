//go:build darwin

package procgroup

import (
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

var darwinProcessListing = func() ([]byte, error) {
	return unix.SysctlRaw("kern.proc.all")
}

var (
	nativeFallbackOnce sync.Once
	nativeFallbackLog  = func(err error) {
		slog.Warn("kernel process listing unavailable; falling back to a ps fork per listing", "err", err)
	}
)

func reportNativeFallback(err error) {
	nativeFallbackOnce.Do(func() { nativeFallbackLog(err) })
}

func nativeProcessTable(withSessions bool) ([]Info, bool) {
	raw, err := darwinProcessListing()
	if err != nil {
		reportNativeFallback(err)
		return nil, false
	}
	size := int(unsafe.Sizeof(unix.KinfoProc{}))
	if len(raw) < size {
		reportNativeFallback(fmt.Errorf("kernel process listing returned %d bytes, short of one %d-byte record", len(raw), size))
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
			// safety: exit between sysctl and this call yields session zero, which
			// callers treat as outside the guarded session.
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
