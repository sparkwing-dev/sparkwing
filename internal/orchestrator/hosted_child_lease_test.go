package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestHostedChildOfAPinnedRunInheritsItsCores(t *testing.T) {
	home := t.TempDir()
	startWingd(t, home, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	h := &hostedAdmitter{la: testWingdAdmission(home, nil), pipeline: "parent"}
	parent, err := h.Acquire(ctx, wingwire.AdmissionRequest{RunID: "pinned-parent", Pipeline: "parent", Resources: wingwire.HostResources{Cores: 1}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Release()
	node, err := h.Acquire(ctx, wingwire.AdmissionRequest{RunID: "pinned-parent/node-semaphore/cGFyZW50", OwnerRunID: "pinned-parent", OwnerLeaseToken: parent.Token(), SemaphoresOnly: true, SubLease: true, Semaphores: []wingwire.SemaphoreClaim{{Name: "__box__:parent-node", Capacity: 1, Cost: 1, Policy: wingwire.PolicyQueue}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer node.Release()
	old := testWingdAdmission(home, nil)
	old.ParentLeaseToken = parent.Token()
	old.ParentRunID = "pinned-parent"
	previous, _, err := old.attachChildRun(ctx, Backends{}, "old-child", "child", sparkwing.NewPlan(), func(error) {})
	if err != nil {
		t.Fatal(err)
	}
	if !previous.hostAdmitted {
		t.Fatal("old child lacked parent host share")
	}
	previous.release()
	t.Log("old dispatcher child inherited pinned parent's occupied core")
	next := testWingdAdmission(home, nil)
	next.ParentRunID, next.ParentNodeID = "pinned-parent", "parent"
	hosted := &hostedAdmitter{la: next, pipeline: "child"}
	child, err := hosted.Acquire(ctx, wingwire.AdmissionRequest{RunID: "new-child", Pipeline: "child", SemaphoresOnly: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer child.Release()
	t.Logf("new child's inherited resources=%+v attachedHost=%v", child.(hostedLease).l.Resources, hosted.attachedHost)
	childctx, stop := context.WithTimeout(ctx, 300*time.Millisecond)
	defer stop()
	task, err := hosted.Acquire(childctx, wingwire.AdmissionRequest{RunID: "new-child/node-host/YnVpbGQ", OwnerRunID: "new-child", OwnerLeaseToken: child.Token(), Pipeline: "child", Resources: wingwire.HostResources{Cores: 1}, SubLease: true}, nil)
	if err != nil {
		t.Fatalf("child queues for the occupied core its waiting parent pins: %v", err)
	}
	task.Release()
}

func TestHostedChildOfAGroupedRunInheritsItsGroup(t *testing.T) {
	home := t.TempDir()
	startWingd(t, home, 2)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	h := &hostedAdmitter{la: testWingdAdmission(home, nil), pipeline: "parent"}
	group := sparkwing.NewConcurrencyGroup("shared-parent-child", sparkwing.ConcurrencyLimit{Capacity: 1, Scope: sparkwing.ScopeBox, OnLimit: sparkwing.Queue})
	plan := sparkwing.NewPlan().Concurrency(group)
	claims := planSemaphoreClaims(plan, "group-parent")
	parent, err := h.Acquire(ctx, wingwire.AdmissionRequest{RunID: "group-parent", Pipeline: "parent", SemaphoresOnly: true, Semaphores: claims}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Release()
	node, err := h.Acquire(ctx, wingwire.AdmissionRequest{RunID: "group-parent/node-host/cGFyZW50", OwnerRunID: "group-parent", OwnerLeaseToken: parent.Token(), Resources: wingwire.HostResources{Cores: 1}, SubLease: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer node.Release()
	old := testWingdAdmission(home, nil)
	old.ParentLeaseToken = parent.Token()
	old.ParentRunID = "group-parent"
	previous, _, err := old.attachChildRun(ctx, Backends{}, "old-group-child", "child", plan, func(error) {})
	if err != nil {
		t.Fatal(err)
	}
	previous.release()
	t.Log("old child attaches and inherits shared plan semaphore without reacquisition")
	next := testWingdAdmission(home, nil)
	next.ParentRunID, next.ParentNodeID = "group-parent", "parent"
	hosted := &hostedAdmitter{la: next, pipeline: "child"}
	childctx, stop := context.WithTimeout(ctx, 300*time.Millisecond)
	defer stop()
	child, err := hosted.Acquire(childctx, wingwire.AdmissionRequest{RunID: "new-group-child", Pipeline: "child", SemaphoresOnly: true, Semaphores: claims}, nil)
	if err != nil {
		t.Fatalf("child queues for shared plan semaphore its waiting parent holds: %v", err)
	}
	child.Release()
}
