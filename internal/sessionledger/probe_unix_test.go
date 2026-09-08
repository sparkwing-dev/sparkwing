//go:build unix

package sessionledger

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func startSleeperSession(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sh", "-c", "sleep 60 & sleep 60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	return cmd
}

func TestPlatformProbeEndsASessionWhoseOwnerIsGone(t *testing.T) {
	step := startSleeperSession(t)
	leader, err := procgroup.CaptureSession(step.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	// safety: the owner is a process that has already exited, so its pid is
	// certainly not alive with the recorded birth token.
	owner := exec.Command("true")
	if err := owner.Run(); err != nil {
		t.Fatal(err)
	}
	l := Open(filepath.Join(t.TempDir(), "sessions"))
	if _, err := l.Record(Record{
		Run: "run-1", Node: "build", OwnerPID: owner.Process.Pid, OwnerBirth: "gone",
		Handle:  Handle{Kind: "session", LeaderPID: leader.LeaderPID, SessionID: leader.SessionID, LeaderBirth: leader.BirthToken},
		Command: "sleep",
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
		t.Fatal("the step session leader outlived the sweep")
	}
	empty, err := procgroup.SessionEmpty(leader)
	if err != nil {
		t.Fatal(err)
	}
	if !empty {
		t.Fatal("the backgrounded sleep in the session outlived the sweep")
	}
}

func TestPlatformProbeLeavesALiveOwnerAlone(t *testing.T) {
	step := startSleeperSession(t)
	leader, err := procgroup.CaptureSession(step.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	birth, err := procgroup.ProcessBirth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	l := Open(filepath.Join(t.TempDir(), "sessions"))
	if _, err := l.Record(Record{
		Run: "run-1", Node: "build", OwnerPID: os.Getpid(), OwnerBirth: birth,
		Handle: Handle{Kind: "session", LeaderPID: leader.LeaderPID, SessionID: leader.SessionID, LeaderBirth: leader.BirthToken},
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
	if err := syscall.Kill(step.Process.Pid, 0); err != nil {
		t.Fatal("a live owner's session was killed")
	}
}

func TestPlatformProbeTreatsAReusedOwnerPIDAsGone(t *testing.T) {
	step := startSleeperSession(t)
	leader, err := procgroup.CaptureSession(step.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	l := Open(filepath.Join(t.TempDir(), "sessions"))
	if _, err := l.Record(Record{
		Run: "run-1", Node: "build", OwnerPID: os.Getpid(), OwnerBirth: "an-earlier-incarnation",
		Handle: Handle{Kind: "session", LeaderPID: leader.LeaderPID, SessionID: leader.SessionID, LeaderBirth: leader.BirthToken},
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
}
