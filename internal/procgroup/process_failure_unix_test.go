//go:build !windows

package procgroup

import (
	"errors"
	"reflect"
	"strconv"
	"syscall"
	"testing"
)

func TestSendSignalPreservesPermissionFailure(t *testing.T) {
	original := processGroupSignal
	t.Cleanup(func() { processGroupSignal = original })
	processGroupSignal = func(int, syscall.Signal) error { return syscall.EPERM }
	if err := sendSignal(12345, true, syscall.SIGKILL); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("signal error = %v, want permission failure", err)
	}
	processGroupSignal = func(int, syscall.Signal) error { return syscall.ESRCH }
	if err := sendSignal(12345, true, syscall.SIGKILL); err != nil {
		t.Fatalf("signal absent group: %v", err)
	}
}

func TestProcessTableRejectsMalformedRows(t *testing.T) {
	for _, row := range []string{"", "12 12", "invalid 12 S", "12 invalid S", "12 12 S extra"} {
		t.Run(row, func(t *testing.T) {
			_, err := parsePSProcessTable([]byte(row), false)
			if err == nil {
				t.Fatal("malformed process row was accepted")
			}
			if row == "invalid 12 S" || row == "12 invalid S" {
				var numberError *strconv.NumError
				if !errors.As(err, &numberError) {
					t.Fatalf("parse error = %v, want native number error", err)
				}
			}
		})
	}
}

func TestProcessTablePreservesSessionLookupFailure(t *testing.T) {
	original := processSessionID
	t.Cleanup(func() { processSessionID = original })
	processSessionID = func(int) (int, error) { return 0, syscall.EIO }
	if _, err := parsePSProcessTable([]byte("12 12 S"), true); !errors.Is(err, syscall.EIO) {
		t.Fatalf("session lookup error = %v, want I/O failure", err)
	}
	processSessionID = func(int) (int, error) { return 0, syscall.ESRCH }
	processes, err := parsePSProcessTable([]byte("12 12 S"), true)
	if err != nil || len(processes) != 0 {
		t.Fatalf("vanished process listing = %+v, %v", processes, err)
	}
}

func TestSignalSessionAttemptsEveryGroupInOrder(t *testing.T) {
	originalTable, originalSignal := sessionProcessTable, processGroupSignal
	t.Cleanup(func() {
		sessionProcessTable, processGroupSignal = originalTable, originalSignal
	})
	sessionProcessTable = func(bool) ([]Info, error) {
		return []Info{{Session: 12345, Group: 30303}, {Session: 12345, Group: 10101}, {Session: 12345, Group: 20202}}, nil
	}
	var signaled []int
	processGroupSignal = func(pid int, _ syscall.Signal) error {
		signaled = append(signaled, -pid)
		switch -pid {
		case 10101:
			return syscall.EPERM
		case 20202:
			return syscall.EIO
		default:
			return nil
		}
	}
	err := signalSession(12345, syscall.SIGKILL)
	if !errors.Is(err, syscall.EPERM) || !errors.Is(err, syscall.EIO) {
		t.Errorf("signal error = %v, want both native failures", err)
	}
	if !reflect.DeepEqual(signaled, []int{10101, 20202, 30303}) {
		t.Fatalf("signaled groups = %v, want every group in ascending order", signaled)
	}
}
