//go:build darwin

package procgroup

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSessionIdentityIgnoresUnrelatedProcessChurn(t *testing.T) {
	original := darwinSessionProcess
	t.Cleanup(func() { darwinSessionProcess = original })
	pid := os.Getpid()
	process, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		t.Fatal(err)
	}
	darwinSessionProcess = func(name string, args ...int) ([]unix.KinfoProc, error) {
		if name == "kern.proc.all" {
			return nil, unix.ENOMEM
		}
		if name != "kern.proc.pid" || len(args) != 1 || args[0] != pid {
			t.Fatalf("unexpected process lookup: %q %v", name, args)
		}
		return original(name, args...)
	}
	sid, birth, err := sessionIdentity(pid)
	if err != nil {
		t.Fatalf("session identity during unrelated process churn: %v", err)
	}
	wantSID, err := unix.Getsid(pid)
	if err != nil {
		t.Fatal(err)
	}
	if sid != wantSID || birth != darwinBirthToken(*process) {
		t.Fatalf("identity = %d %q, want %d %q", sid, birth, wantSID, darwinBirthToken(*process))
	}
}

func TestSessionIdentityRefusesMissingProcess(t *testing.T) {
	original := darwinSessionProcess
	t.Cleanup(func() { darwinSessionProcess = original })
	darwinSessionProcess = func(string, ...int) ([]unix.KinfoProc, error) { return nil, nil }
	if _, _, err := sessionIdentity(os.Getpid()); !errors.Is(err, ErrProcessAbsent) {
		t.Fatalf("absent process error = %v", err)
	}
}

func TestSessionIdentityReportsKernelFailure(t *testing.T) {
	original := darwinSessionProcess
	t.Cleanup(func() { darwinSessionProcess = original })
	darwinSessionProcess = func(string, ...int) ([]unix.KinfoProc, error) { return nil, unix.EPERM }
	if _, _, err := sessionIdentity(os.Getpid()); !errors.Is(err, unix.EPERM) {
		t.Fatalf("kernel error = %v", err)
	}
}
