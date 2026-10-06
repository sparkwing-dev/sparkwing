//go:build windows

package sparkwing

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/sessionledger"
)

func TestExec_WindowsCancellationHelper(t *testing.T) {
	marker := os.Getenv("SPARKWING_EXEC_READY")
	if marker == "" {
		return
	}
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		os.Exit(11)
	}
	if err := os.WriteFile(marker, []byte("ready"), 0o600); err != nil {
		os.Exit(12)
	}
	_, _ = windows.WaitForSingleObject(event, windows.INFINITE)
	os.Exit(13)
}

func TestExec_WindowsNaturalFailureIsNotCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := exec.CommandContext(ctx, "cmd", "/c", "exit 7")
	job, err := startStepCommand(command, "cmd /c exit 7")
	if err != nil {
		t.Fatal(err)
	}
	defer job.close()
	process, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(command.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(process)
	state, err := windows.WaitForSingleObject(process, 8000)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("leader did not exit: %v %v", state, err)
	}
	if err := finishWindowsStepCancellation(process, job.job); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("termination racing with natural exit = %v, want os.ErrProcessDone", err)
	}
	cancel()
	if err := command.Cancel(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("cancellation after leader exit = %v, want os.ErrProcessDone", err)
	}
	err = command.Wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("natural failure = %v, want exit 7", err)
	}
	if job.wasCancelled() {
		t.Fatal("natural leader exit was recorded as cancellation")
	}
	if reason := terminationReason(ctx, exit, job.wasCancelled()); reason != "" {
		t.Fatalf("natural failure with cancelled context = %q", reason)
	}
}

func TestExec_CancelTerminatesTheStepJobAndClearsItsRecord(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_RUN_ID", "run-w")
	t.Setenv("SPARKWING_NODE_ID", "build")
	p := paths.PathsAt(home)
	ledger := sessionledger.Open(p.SessionLedgerDir())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, _ = execCmd(ctx, "cmd", []string{"/c", `start /b ping -n 60 127.0.0.1 >nul & ping -n 60 127.0.0.1 >nul`}, t.TempDir(), nil)
	}()

	var jobName string
	deadline := time.Now().Add(10 * time.Second)
	for jobName == "" {
		if time.Now().After(deadline) {
			t.Fatal("the running step was never recorded")
		}
		recs, _ := ledger.List()
		if len(recs) == 1 {
			jobName = recs[0].Handle.JobName
		}
		time.Sleep(20 * time.Millisecond)
	}
	if jobName == "" {
		t.Fatal("record carries no job name")
	}

	cancel()
	select {
	case <-finished:
	case <-time.After(15 * time.Second):
		t.Fatal("cancelled step did not return")
	}
	if recs, _ := ledger.List(); len(recs) != 0 {
		t.Fatalf("records after the step returned: %+v", recs)
	}
	if err := procgroup.TerminateJobByName(jobName); err != nil {
		t.Fatalf("job %s still reachable after the step returned: %v", jobName, err)
	}
}
