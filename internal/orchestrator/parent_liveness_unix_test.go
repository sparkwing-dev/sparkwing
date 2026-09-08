//go:build !windows

package orchestrator

import (
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
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
