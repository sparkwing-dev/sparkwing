package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// A debug pause belongs to its run's team: another team's handle can
// neither open one on that run nor see or release the one that is open.
func TestTenantDebugPausesStayInTheRunsTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	acme := tenantFor(t, st, "acme")
	globex := tenantFor(t, st, "globex")
	seedTenantRun(t, acme, "run-a", "deploy")
	now := time.Now()
	pause := store.DebugPause{RunID: "run-a", NodeID: "build", Reason: "manual", PausedAt: now, ExpiresAt: now.Add(time.Hour)}

	if err := globex.CreateDebugPause(ctx, pause); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("globex.CreateDebugPause = %v, want ErrNotFound", err)
	}
	if err := acme.CreateDebugPause(ctx, pause); err != nil {
		t.Fatalf("acme.CreateDebugPause: %v", err)
	}
	if _, err := globex.GetActiveDebugPause(ctx, "run-a", "build"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("globex.GetActiveDebugPause = %v, want ErrNotFound", err)
	}
	if pauses, err := globex.ListDebugPauses(ctx, "run-a"); err != nil || len(pauses) != 0 {
		t.Fatalf("globex.ListDebugPauses = %v, %v, want none", pauses, err)
	}
	if err := globex.ReleaseDebugPause(ctx, "run-a", "build", "mallory", store.PauseReleaseManual); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("globex.ReleaseDebugPause = %v, want ErrNotFound", err)
	}
	if p, err := acme.GetActiveDebugPause(ctx, "run-a", "build"); err != nil || p.Reason != "manual" {
		t.Fatalf("acme.GetActiveDebugPause = %+v, %v, want the open pause", p, err)
	}
	if err := acme.ReleaseDebugPause(ctx, "run-a", "build", "alice", store.PauseReleaseManual); err != nil {
		t.Fatalf("acme.ReleaseDebugPause: %v", err)
	}
	if pauses, err := acme.ListDebugPauses(ctx, "run-a"); err != nil || len(pauses) != 1 || pauses[0].ReleasedBy != "alice" {
		t.Fatalf("acme.ListDebugPauses = %+v, %v, want the released pause", pauses, err)
	}
}

// An approval gate belongs to its run's team: another team's handle can
// neither open one on that run nor read, list or resolve it.
func TestTenantApprovalsStayInTheRunsTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	acme := tenantFor(t, st, "acme")
	globex := tenantFor(t, st, "globex")
	seedTenantRun(t, acme, "run-a", "deploy")
	gate := store.Approval{RunID: "run-a", NodeID: "ship", RequestedAt: time.Now(), Message: "ship it?"}

	if err := globex.CreateApproval(ctx, gate); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("globex.CreateApproval = %v, want ErrNotFound", err)
	}
	if err := acme.CreateApproval(ctx, gate); err != nil {
		t.Fatalf("acme.CreateApproval: %v", err)
	}
	if _, err := globex.GetApproval(ctx, "run-a", "ship"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("globex.GetApproval = %v, want ErrNotFound", err)
	}
	if rows, err := globex.ListApprovalsForRun(ctx, "run-a"); err != nil || len(rows) != 0 {
		t.Fatalf("globex.ListApprovalsForRun = %v, %v, want none", rows, err)
	}
	if rows, err := globex.ListPendingApprovals(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("globex.ListPendingApprovals = %v, %v, want none", rows, err)
	}
	if _, err := globex.ResolveApproval(ctx, "run-a", "ship", store.ApprovalResolutionApproved, "mallory", ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("globex.ResolveApproval = %v, want ErrNotFound", err)
	}
	got, err := acme.ResolveApproval(ctx, "run-a", "ship", store.ApprovalResolutionApproved, "alice", "")
	if err != nil || got.Approver != "alice" {
		t.Fatalf("acme.ResolveApproval = %+v, %v, want alice's approval", got, err)
	}
	if rows, err := acme.ListApprovalsForRun(ctx, "run-a"); err != nil || len(rows) != 1 || rows[0].Resolution != store.ApprovalResolutionApproved {
		t.Fatalf("acme.ListApprovalsForRun = %+v, %v, want the approved gate", rows, err)
	}
}

// A dispatch snapshot belongs to its run's team: another team's handle can
// neither write one onto that run nor read or list it.
func TestTenantNodeDispatchesStayInTheRunsTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	acme := tenantFor(t, st, "acme")
	globex := tenantFor(t, st, "globex")
	seedTenantRun(t, acme, "run-a", "deploy")
	d := store.NodeDispatch{RunID: "run-a", NodeID: "build", Seq: -1, CodeVersion: "v1"}

	if err := globex.WriteNodeDispatch(ctx, d); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("globex.WriteNodeDispatch = %v, want ErrNotFound", err)
	}
	if err := acme.WriteNodeDispatch(ctx, d); err != nil {
		t.Fatalf("acme.WriteNodeDispatch: %v", err)
	}
	if _, err := globex.GetNodeDispatch(ctx, "run-a", "build", -1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("globex.GetNodeDispatch = %v, want ErrNotFound", err)
	}
	if out, err := globex.ListNodeDispatches(ctx, "run-a", "build"); err != nil || len(out) != 0 {
		t.Fatalf("globex.ListNodeDispatches = %v, %v, want none", out, err)
	}
	if got, err := acme.GetNodeDispatch(ctx, "run-a", "build", 0); err != nil || got.CodeVersion != "v1" {
		t.Fatalf("acme.GetNodeDispatch = %+v, %v, want the snapshot", got, err)
	}
}
