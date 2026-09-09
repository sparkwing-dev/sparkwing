//go:build darwin

package procgroup

import (
	"context"
	"encoding/binary"
	"errors"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func exitingProcessRecord(pid, group int, exiting bool) []byte {
	var record unix.KinfoProc
	raw := make([]byte, unsafe.Sizeof(record))
	binary.NativeEndian.PutUint32(raw[unsafe.Offsetof(record.Proc)+unsafe.Offsetof(record.Proc.P_pid):], uint32(pid))
	binary.NativeEndian.PutUint32(raw[unsafe.Offsetof(record.Eproc)+unsafe.Offsetof(record.Eproc.Pgid):], uint32(group))
	raw[unsafe.Offsetof(record.Proc)+unsafe.Offsetof(record.Proc.P_stat)] = 2
	if exiting {
		binary.NativeEndian.PutUint32(raw[unsafe.Offsetof(record.Proc)+unsafe.Offsetof(record.Proc.P_flag):], 0x2000)
	}
	return raw
}

func TestExitingProcessSignalPreservesCleanupObligation(t *testing.T) {
	originalListing, originalSession, originalSignal := darwinProcessListing, processSessionID, processGroupSignal
	t.Cleanup(func() {
		darwinProcessListing, processSessionID, processGroupSignal = originalListing, originalSession, originalSignal
	})
	darwinProcessListing = func() ([]byte, error) { return exitingProcessRecord(12346, 12345, true), nil }
	processSessionID = func(int) (int, error) { return 12345, nil }
	processGroupSignal = func(int, syscall.Signal) error { return syscall.EPERM }
	if err := sendSignal(t.Context(), 12345, true, syscall.SIGTERM); err != nil {
		t.Errorf("signal exiting group: %v", err)
	}
	if err := signalSession(t.Context(), 12345, syscall.SIGTERM); err != nil {
		t.Errorf("signal exiting session: %v", err)
	}
	identity := SessionIdentity{LeaderPID: 12345, SessionID: 12345, BirthToken: "fixture-birth"}
	if empty, err := SessionQuiescent(identity); err != nil || empty {
		t.Fatalf("exiting session quiescent=%v error=%v, want pending cleanup", empty, err)
	}
	if empty, err := descendantsEmpty(t.Context(), 12345, true, false); err != nil || empty {
		t.Fatalf("exiting descendants empty=%v error=%v, want pending cleanup", empty, err)
	}
	darwinProcessListing = func() ([]byte, error) {
		return append(exitingProcessRecord(12346, 12345, true), exitingProcessRecord(12347, 12345, false)...), nil
	}
	if err := sendSignal(t.Context(), 12345, true, syscall.SIGTERM); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("mixed group signal error=%v, want permission failure", err)
	}
	darwinProcessListing = func() ([]byte, error) { return nil, syscall.EIO }
	if err := sendSignal(t.Context(), 12345, true, syscall.SIGTERM); !errors.Is(err, syscall.EPERM) || !errors.Is(err, syscall.EIO) {
		t.Fatalf("failed inspection error=%v, want permission and inspection causes", err)
	}
}

func TestExitingDescendantDeadlineRetainsLeader(t *testing.T) {
	group := startHelper(t, "short")
	originalListing, originalSignal := darwinProcessListing, processGroupSignal
	t.Cleanup(func() {
		darwinProcessListing, processGroupSignal = originalListing, originalSignal
		terminateForTest(t, group)
	})
	select {
	case <-group.LeaderExited():
	case <-time.After(3 * time.Second):
		t.Fatal("leader did not exit")
	}
	darwinProcessListing = func() ([]byte, error) { return exitingProcessRecord(group.ID()+1, group.ID(), true), nil }
	processGroupSignal = func(int, syscall.Signal) error { return syscall.EPERM }
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	err := group.Finish(ctx, 10*time.Millisecond)
	if !errors.Is(err, ErrCleanup) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("finish error=%v, want cleanup deadline", err)
	}
	if group.Reaped() {
		t.Fatal("leader reaped while exiting descendant remained")
	}
}
