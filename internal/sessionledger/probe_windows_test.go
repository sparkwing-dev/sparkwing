//go:build windows

package sessionledger

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func TestPlatformProbeEndsAJobWhoseOwnerIsGone(t *testing.T) {
	job, err := procgroup.NewJob(fmt.Sprintf(`Local\sparkwing-ledger-test-%d-%d`, os.Getpid(), time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = job.Close() })
	step := exec.Command("cmd", "/c", `ping -n 60 127.0.0.1 >nul`)
	if err := job.Start(step); err != nil {
		t.Fatal(err)
	}
	gone := exec.Command("cmd", "/c", "exit 0")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	l := Open(filepath.Join(t.TempDir(), "sessions"))
	if _, err := l.Record(Record{
		Run: "run-1", Node: "build", OwnerPID: gone.Process.Pid, OwnerBirth: "earlier",
		Handle:  Handle{Kind: "job", LeaderPID: step.Process.Pid, JobName: job.Name},
		Command: "ping",
	}); err != nil {
		t.Fatal(err)
	}
	out, err := l.Sweep(context.Background(), SweepOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Verdict != VerdictReaped {
		t.Fatalf("outcomes = %+v", out)
	}
	waited := make(chan error, 1)
	go func() { waited <- step.Wait() }()
	select {
	case <-waited:
	case <-time.After(10 * time.Second):
		t.Fatal("the step outlived the sweep")
	}
}

func TestPlatformProbeLeavesALiveOwnerAlone(t *testing.T) {
	birth, err := procgroup.ProcessBirth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	l := Open(filepath.Join(t.TempDir(), "sessions"))
	if _, err := l.Record(Record{
		Run: "run-1", Node: "build", OwnerPID: os.Getpid(), OwnerBirth: birth,
		Handle: Handle{Kind: "job", JobName: `Local\sparkwing-ledger-test-live`},
	}); err != nil {
		t.Fatal(err)
	}
	out, err := l.Sweep(context.Background(), SweepOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Verdict != VerdictLive {
		t.Fatalf("outcomes = %+v", out)
	}
}
