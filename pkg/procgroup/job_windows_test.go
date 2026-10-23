//go:build windows

package procgroup

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

const sleepTree = `start /b ping -n 60 127.0.0.1 >nul & ping -n 60 127.0.0.1 >nul`

func TestProcessBirthIsStableForALiveProcessAndAbsentForAMissingOne(t *testing.T) {
	first, err := ProcessBirth(os.Getpid())
	if err != nil || first == "" {
		t.Fatalf("own birth = %q, %v", first, err)
	}
	second, err := ProcessBirth(os.Getpid())
	if err != nil || second != first {
		t.Fatalf("own birth changed: %q -> %q (%v)", first, second, err)
	}
	if _, err := ProcessBirth(4_000_000_000 - 3); !errors.Is(err, ErrProcessAbsent) {
		t.Fatalf("absent pid err = %v, want ErrProcessAbsent", err)
	}
}

func startTreeInJob(t *testing.T) (*Job, *exec.Cmd) {
	t.Helper()
	job, err := NewJob(fmt.Sprintf(`Local\sparkwing-test-%d-%d`, os.Getpid(), time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = job.Close() })
	cmd := exec.Command("cmd", "/c", sleepTree)
	if err := job.Start(cmd); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		n, err := job.ActiveProcesses()
		if err != nil {
			t.Fatal(err)
		}
		if n >= 2 || time.Now().After(deadline) {
			if n < 2 {
				t.Fatalf("job holds %d processes, want the shell and its background ping", n)
			}
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return job, cmd
}

func TestTerminateJobByNameEndsEveryMember(t *testing.T) {
	job, cmd := startTreeInJob(t)
	if err := TerminateJobByName(job.Name); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case <-waited:
	case <-time.After(10 * time.Second):
		t.Fatal("the job leader outlived TerminateJobByName")
	}
	n, err := job.ActiveProcesses()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("job still holds %d processes after termination", n)
	}
}

func TestTerminateJobByNameTreatsAMissingJobAsAlreadyGone(t *testing.T) {
	if err := TerminateJobByName(`Local\sparkwing-test-nobody-holds-this`); err != nil {
		t.Fatalf("missing job err = %v, want nil", err)
	}
}

func TestClosingTheJobEndsItsMembers(t *testing.T) {
	job, cmd := startTreeInJob(t)
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case <-waited:
	case <-time.After(10 * time.Second):
		t.Fatal("the job leader outlived the closed job handle")
	}
	if err := TerminateJobByName(job.Name); err != nil {
		t.Fatalf("closed job err = %v, want nil because it no longer exists", err)
	}
}
