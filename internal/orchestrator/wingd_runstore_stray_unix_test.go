//go:build unix

package orchestrator

import (
	"context"
	"encoding/json"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/sessionledger"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestFinalizeRunEndsTheRunsStraySessionsAndRecordsThem(t *testing.T) {
	home := t.TempDir()
	p := PathsAt(home)
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(context.Background(), store.Run{ID: "run-dead", Pipeline: "build", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

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
		Run: "run-dead", Node: "build", OwnerPID: gone.Process.Pid, OwnerBirth: "earlier",
		Handle:  sessionledger.Handle{Kind: "session", LeaderPID: leader.LeaderPID, SessionID: leader.SessionID, LeaderBirth: leader.BirthToken},
		Command: "go build ./...",
	}); err != nil {
		t.Fatal(err)
	}

	runs, err := NewHeldRunStore(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runs.Close() })
	runs.FinalizeRun("run-dead")

	waited := make(chan error, 1)
	go func() { waited <- step.Wait() }()
	select {
	case <-waited:
	case <-time.After(10 * time.Second):
		t.Fatal("the dead run's step session outlived FinalizeRun")
	}
	if empty, err := procgroup.SessionEmpty(leader); err != nil || !empty {
		t.Fatalf("session empty = %v, %v", empty, err)
	}
	left, _ := sessionledger.Open(p.SessionLedgerDir()).List()
	if len(left) != 0 {
		t.Fatalf("records after finalize = %+v", left)
	}

	st, err = store.Open(p.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	events, err := st.ListEventsAfter(context.Background(), "run-dead", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range events {
		if e.Kind != sessionledger.EventKind {
			continue
		}
		var payload sessionledger.EventPayload
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if e.NodeID == "build" && payload.Reaper == "daemon" && payload.Verdict == sessionledger.VerdictReaped && payload.LeaderPID == leader.LeaderPID {
			found = true
		}
	}
	if !found {
		t.Fatalf("no %s event for the reaped session; events = %+v", sessionledger.EventKind, events)
	}
}
