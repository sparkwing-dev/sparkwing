package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestHostedNestedRunAttachesToItsParentsLease(t *testing.T) {
	home := t.TempDir()
	startWingd(t, home, 1)
	parentAdmission := testWingdAdmission(home, nil)
	h := &hostedAdmitter{la: parentAdmission, pipeline: "parent"}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	parent, err := h.Acquire(ctx, wingwire.AdmissionRequest{RunID: "c3-parent", Pipeline: "parent", Resources: wingwire.HostResources{Cores: 1}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Release()

	old := testWingdAdmission(home, nil)
	old.ParentLeaseToken = parent.Token()
	old.ParentRunID = "c3-parent"
	previous, _, err := old.attachChildRun(ctx, Backends{}, "c3-old-child", "child", sparkwing.NewPlan(), func(error) {})
	if err != nil {
		t.Fatalf("old dispatcher attach: %v", err)
	}
	previous.release()
	t.Log("old dispatcher attached child immediately while parent holds full CPU capacity")

	next := testWingdAdmission(home, nil)
	next.ParentRunID, next.ParentNodeID = "c3-parent", "call"
	hosted := &hostedAdmitter{la: next, pipeline: "child"}
	childCtx, stop := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer stop()
	lease, err := hosted.Acquire(childCtx, wingwire.AdmissionRequest{RunID: "c3-new-child", Pipeline: "child", Resources: wingwire.HostResources{Cores: 1}}, nil)
	if err != nil {
		t.Fatalf("new hosted child failed instead of attaching: %v", err)
	}
	lease.Release()
}

func TestHostedRunHandleWaitsForTheRunRow(t *testing.T) {
	dir := t.TempDir()
	paths := PathsAt(dir)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	st, err := teststore.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	handle := filepath.Join(dir, "handle.json")
	_, err = RunHosted(t.Context(), paths, Options{RunID: "c3-no-row", Pipeline: "fixture", State: st, Delegate: &storedLog{}, RunHandlePath: handle}, HostedEntry{Program: filepath.Join(dir, "missing-sdk"), Dir: dir, Env: []string{}})
	if err == nil {
		t.Fatal("expected SDK startup refusal")
	}
	_, rowErr := st.GetRun(t.Context(), "c3-no-row")
	if !errors.Is(rowErr, store.ErrNotFound) {
		t.Fatalf("expected no accepted run, got %v", rowErr)
	}
	data, readErr := os.ReadFile(handle)
	if readErr == nil {
		t.Fatalf("published handle for nonexistent run after startup refusal %v: %s", err, data)
	}
}

func TestHostedKeepsAnExplicitResourcePinStrict(t *testing.T) {
	home := t.TempDir()
	startWingd(t, home, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	old := testWingdAdmission(home, nil)
	plan := sparkwing.NewPlan().Resources(sparkwing.Cores(2))
	previous, _, err := old.admitRun(ctx, Backends{}, "pinned", "c3-old-pin", plan, 1, func(error) {})
	if err == nil {
		previous.release()
		t.Fatal("old dispatcher unexpectedly accepted infeasible 2-core pin on 1-core machine")
	}
	t.Logf("old dispatcher correctly refused: %v", err)
	h := &hostedAdmitter{la: testWingdAdmission(home, nil), pipeline: "pinned"}
	next, err := h.Acquire(ctx, wingwire.AdmissionRequest{RunID: "c3-new-pin", Pipeline: "pinned", Resources: wingwire.HostResources{Cores: 2}}, nil)
	if err != nil {
		return
	}
	defer next.Release()
	t.Fatalf("hosted adapter accepted 2-core resource pin; granted resources: %+v", next.(hostedLease).l.Resources)
}
