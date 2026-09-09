//go:build !windows

package procgroup

import (
	"errors"
	"syscall"
	"testing"
)

func TestSignalPermissionFailureRequiresTerminatedGroupProof(t *testing.T) {
	for _, test := range []struct {
		name           string
		processes      []Info
		inspectionErr  error
		wantPermission bool
	}{
		{name: "terminated", processes: []Info{{PID: 12345, Group: 12345, State: "Z"}}},
		{name: "live", processes: []Info{{PID: 12345, Group: 12345, State: "S"}}, wantPermission: true},
		{name: "inspection failure", inspectionErr: syscall.EIO, wantPermission: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			originalTable, originalSignal := sessionProcessTable, processGroupSignal
			t.Cleanup(func() { sessionProcessTable, processGroupSignal = originalTable, originalSignal })
			signaled := false
			processGroupSignal = func(int, syscall.Signal) error { signaled = true; return syscall.EPERM }
			sessionProcessTable = func(bool) ([]Info, error) {
				if !signaled {
					return []Info{{PID: 12345, Group: 12345, State: "S"}}, nil
				}
				return test.processes, test.inspectionErr
			}
			err := sendSignal(12345, true, syscall.SIGTERM)
			if errors.Is(err, syscall.EPERM) != test.wantPermission {
				t.Fatalf("signal error = %v, permission failure wanted %v", err, test.wantPermission)
			}
			if test.inspectionErr != nil && !errors.Is(err, test.inspectionErr) {
				t.Fatalf("signal error = %v, want inspection cause %v", err, test.inspectionErr)
			}
			if !test.wantPermission && err != nil {
				t.Fatalf("terminated group signal: %v", err)
			}
		})
	}
}
