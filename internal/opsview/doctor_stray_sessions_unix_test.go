//go:build !windows

package opsview_test

import (
	"context"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/opsview"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/sessionledger"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func plantStraySession(t *testing.T, p paths.Paths) (*exec.Cmd, procgroup.SessionIdentity) {
	t.Helper()
	step := exec.Command("sh", "-c", "sleep 60 & sleep 60")
	step.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := step.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-step.Process.Pid, syscall.SIGKILL) })
	leader, err := procgroup.CaptureSession(step.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := sessionledger.Open(p.SessionLedgerDir()).Record(sessionledger.Record{
		Run: "run-known", Node: "build", OwnerPID: gone.Process.Pid, OwnerBirth: "earlier",
		Handle:  sessionledger.Handle{Kind: "session", LeaderPID: leader.LeaderPID, SessionID: leader.SessionID, LeaderBirth: leader.BirthToken},
		Command: "go build ./...",
	}); err != nil {
		t.Fatal(err)
	}
	return step, leader
}

func TestDiagnose_EndsStrayStepSessionsAndRecordsThem(t *testing.T) {
	home := shortHome(t)
	ownHome(t, home, "")
	p, _ := seedRunDirHomeAt(t, home, time.Hour)
	step, leader := plantStraySession(t, p)

	report, err := opsview.Diagnose(context.Background(), p, p.Root, "v1.0.0", false)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if len(report.StrayStepSessions) != 1 || report.StrayStepSessions[0].Verdict != string(sessionledger.VerdictReaped) {
		t.Fatalf("stray step sessions = %+v", report.StrayStepSessions)
	}
	if report.Clean() {
		t.Fatal("a report that ended a session claims the home was clean")
	}
	waited := make(chan error, 1)
	go func() { waited <- step.Wait() }()
	select {
	case <-waited:
	case <-time.After(10 * time.Second):
		t.Fatal("the stray session outlived doctor")
	}
	if empty, err := procgroup.SessionEmpty(leader); err != nil || !empty {
		t.Fatalf("session empty = %v, %v", empty, err)
	}
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	events, err := st.ListEventsAfter(context.Background(), "run-known", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range events {
		if e.Kind == sessionledger.EventKind && e.NodeID == "build" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no %s event recorded; events = %+v", sessionledger.EventKind, events)
	}
}

func TestDiagnose_DryRunReportsStraySessionsWithoutEndingThem(t *testing.T) {
	home := shortHome(t)
	ownHome(t, home, "")
	p, _ := seedRunDirHomeAt(t, home, time.Hour)
	step, _ := plantStraySession(t, p)

	report, err := opsview.Diagnose(context.Background(), p, p.Root, "v1.0.0", true)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if len(report.StrayStepSessions) != 1 || report.StrayStepSessions[0].Verdict != string(sessionledger.VerdictWouldReap) {
		t.Fatalf("stray step sessions = %+v", report.StrayStepSessions)
	}
	if err := syscall.Kill(step.Process.Pid, 0); err != nil {
		t.Fatal("dry run ended the session")
	}
	if left, _ := sessionledger.Open(p.SessionLedgerDir()).List(); len(left) != 1 {
		t.Fatal("dry run removed the record")
	}
}
