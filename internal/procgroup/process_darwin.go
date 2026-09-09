//go:build darwin

package procgroup

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const darwinProcessFlagExiting = 0x00002000

var darwinProcessListing = func() ([]byte, error) {
	return unix.SysctlRaw("kern.proc.all")
}

func processTable(ctx context.Context, withSessions bool) ([]Info, error) {
	ctx, cancel := context.WithTimeout(ctx, processTableTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := readDarwinProcessListing(ctx)
	if err != nil {
		return nil, err
	}
	size := int(unsafe.Sizeof(unix.KinfoProc{}))
	if len(raw) == 0 || len(raw)%size != 0 {
		return nil, fmt.Errorf("kernel process listing has %d bytes for %d-byte records", len(raw), size)
	}
	processes := make([]Info, 0, len(raw)/size)
	for start := 0; start+size <= len(raw); start += size {
		// #nosec G103 -- decodes a fixed-size kernel struct from a bounds-checked buffer
		process := *(*unix.KinfoProc)(unsafe.Pointer(&raw[start]))
		pid := int(process.Proc.P_pid)
		if pid <= 0 {
			continue
		}
		sessionID := 0
		if withSessions {
			sessionID, err = processSessionID(pid)
			if errors.Is(err, syscall.ESRCH) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("inspect process %d session: %w", pid, err)
			}
		}
		processes = append(processes, Info{
			PID:     pid,
			Group:   int(process.Eproc.Pgid),
			Session: sessionID,
			State:   darwinProcessState(process.Proc.P_stat),
			Exiting: process.Proc.P_flag&darwinProcessFlagExiting != 0,
			Birth:   darwinBirthToken(process),
		})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return processes, nil
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

func readDarwinProcessListing(ctx context.Context) ([]byte, error) {
	for {
		raw, err := darwinProcessListing()
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, errors.Join(err, contextErr)
		}
		// SAFETY: Process creation can outgrow the size sampled by SysctlRaw.
		if errors.Is(err, syscall.ENOMEM) {
			continue
		}
		return raw, err
	}
}
