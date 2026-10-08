//go:build !windows

package orchestrator

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/runners/local"
)

func TestWatchLiveness_EOFReapsOwnedSessionsEvenWhenCancelIsIgnored(t *testing.T) {
	step := exec.Command("sleep", "60")
	step.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := step.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-step.Process.Pid, syscall.SIGKILL) })
	defer procgroup.Own(step.Process.Pid)()

	r, closeWrite := livenessPipe(t)
	stop := watchLiveness(r, func() {}, time.Hour, func(int) {})
	defer stop()

	closeWrite()

	waited := make(chan error, 1)
	go func() { waited <- step.Wait() }()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("the step session outlived the node's dispatcher")
	}
	if status, ok := step.ProcessState.Sys().(syscall.WaitStatus); !ok || status.Signal() != syscall.SIGKILL {
		t.Fatalf("step ended with %v, want death by SIGKILL", step.ProcessState)
	}
}

func TestParentLivenessDescriptorStopsAtThisProcess(t *testing.T) {
	r, _ := livenessPipe(t)
	fd := int(r.Fd())
	if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_SETFD, 0); errno != 0 {
		t.Fatal(errno)
	}
	t.Setenv(local.ParentLivenessFDEnv, strconv.Itoa(fd))
	if openParentLivenessPipe() == nil {
		t.Fatal("the descriptor the host named was not opened")
	}
	if _, set := os.LookupEnv(local.ParentLivenessFDEnv); set {
		t.Fatal("the liveness variable stayed in the environment a step command inherits")
	}
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0)
	if errno != 0 || flags&syscall.FD_CLOEXEC == 0 {
		t.Fatalf("descriptor flags = %#x, %v; want close-on-exec", flags, errno)
	}
}
