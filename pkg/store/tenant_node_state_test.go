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

// A node's writes and its steps belong to its run's team: every write
// another team's handle aims at the node reads as not found and leaves it
// as it was, and that handle lists none of its steps.
func TestTenantNodeWritesStayInTheRunsTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	acme := tenantFor(t, st, "acme")
	globex := tenantFor(t, st, "globex")
	seedTenantRun(t, acme, "run-a", "deploy")
	if err := st.CreateNode(ctx, store.Node{RunID: "run-a", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}

	writes := map[string]func(*store.Tenant) error{
		"StartNode":               func(tn *store.Tenant) error { return tn.StartNode(ctx, "run-a", "build") },
		"SetNodeStatus":           func(tn *store.Tenant) error { return tn.SetNodeStatus(ctx, "run-a", "build", "running") },
		"UpdateNodeDeps":          func(tn *store.Tenant) error { return tn.UpdateNodeDeps(ctx, "run-a", "build", []string{"lint"}) },
		"UpdateNodeActivity":      func(tn *store.Tenant) error { return tn.UpdateNodeActivity(ctx, "run-a", "build", "compiling") },
		"TouchNodeHeartbeat":      func(tn *store.Tenant) error { return tn.TouchNodeHeartbeat(ctx, "run-a", "build") },
		"AppendNodeAnnotation":    func(tn *store.Tenant) error { return tn.AppendNodeAnnotation(ctx, "run-a", "build", "note") },
		"SetNodeSummary":          func(tn *store.Tenant) error { return tn.SetNodeSummary(ctx, "run-a", "build", "# done") },
		"SetNodeArtifactManifest": func(tn *store.Tenant) error { return tn.SetNodeArtifactManifest(ctx, "run-a", "build", "sha256:x") },
		"StartNodeStep":           func(tn *store.Tenant) error { return tn.StartNodeStep(ctx, "run-a", "build", "compile") },
		"FinishNodeStep":          func(tn *store.Tenant) error { return tn.FinishNodeStep(ctx, "run-a", "build", "compile", "passed") },
		"SkipNodeStep":            func(tn *store.Tenant) error { return tn.SkipNodeStep(ctx, "run-a", "build", "test") },
		"AppendStepAnnotation":    func(tn *store.Tenant) error { return tn.AppendStepAnnotation(ctx, "run-a", "build", "compile", "slow") },
		"SetStepSummary":          func(tn *store.Tenant) error { return tn.SetStepSummary(ctx, "run-a", "build", "compile", "ok") },
	}
	for name, write := range writes {
		if err := write(globex); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("globex.%s = %v, want ErrNotFound", name, err)
		}
	}
	usage := store.NodeUsage{CPUTime: time.Second, Wall: time.Second}
	if err := globex.AddNodeUsage(ctx, "run-a", "build", usage); err != nil {
		t.Errorf("globex.AddNodeUsage = %v, want the no-op a missing node gets", err)
	}
	n, err := acme.GetNode(ctx, "run-a", "build")
	if err != nil {
		t.Fatal(err)
	}
	if n.Status != "pending" || n.Summary != "" || len(n.Annotations) != 0 || n.ArtifactManifest != "" || n.CPUNanos != 0 {
		t.Fatalf("another team's writes changed the node: %+v", n)
	}
	for name, write := range writes {
		if err := write(acme); err != nil {
			t.Errorf("acme.%s: %v", name, err)
		}
	}
	if steps, err := globex.ListNodeSteps(ctx, "run-a"); err != nil || len(steps) != 0 {
		t.Fatalf("globex.ListNodeSteps = %v, %v, want none", steps, err)
	}
	if steps, err := acme.ListNodeSteps(ctx, "run-a"); err != nil || len(steps) != 2 {
		t.Fatalf("acme.ListNodeSteps = %v, %v, want compile and test", steps, err)
	}
}

// A run's events belong to its team: another team's handle neither lists
// them nor appends a once-only event onto the run.
func TestTenantEventsStayInTheRunsTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	acme := tenantFor(t, st, "acme")
	globex := tenantFor(t, st, "globex")
	seedTenantRun(t, acme, "run-a", "deploy")

	if _, err := globex.AppendEventOnce(ctx, "run-a", "", "credits_blocked", []byte(`{}`)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("globex.AppendEventOnce = %v, want ErrNotFound", err)
	}
	if wrote, err := acme.AppendEventOnce(ctx, "run-a", "", "credits_blocked", []byte(`{}`)); err != nil || !wrote {
		t.Fatalf("acme.AppendEventOnce = %v, %v, want written", wrote, err)
	}
	if wrote, err := acme.AppendEventOnce(ctx, "run-a", "", "credits_blocked", []byte(`{}`)); err != nil || wrote {
		t.Fatalf("second acme.AppendEventOnce = %v, %v, want skipped", wrote, err)
	}
	if events, err := globex.ListEventsAfter(ctx, "run-a", 0, 10); err != nil || len(events) != 0 {
		t.Fatalf("globex.ListEventsAfter = %v, %v, want none", events, err)
	}
	if events, err := acme.ListEventsAfter(ctx, "run-a", 0, 10); err != nil || len(events) != 1 {
		t.Fatalf("acme.ListEventsAfter = %v, %v, want the one event", events, err)
	}
}

// Run control belongs to the run's team: another team's handle can neither
// cancel, retry-link, delete nor read the run, and leaves it as it was.
func TestTenantRunControlStaysInTheRunsTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	acme := tenantFor(t, st, "acme")
	globex := tenantFor(t, st, "globex")
	seedTenantRun(t, acme, "run-a", "deploy")

	if _, err := globex.GetRun(ctx, "run-a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("globex.GetRun = %v, want ErrNotFound", err)
	}
	if err := globex.DeleteRun(ctx, "run-a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("globex.DeleteRun = %v, want ErrNotFound", err)
	}
	if err := globex.RequestCancel(ctx, "run-a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("globex.RequestCancel = %v, want ErrNotFound", err)
	}
	if cancelled, err := globex.CancelPendingTrigger(ctx, "run-a"); err != nil || cancelled {
		t.Fatalf("globex.CancelPendingTrigger = %v, %v, want nothing cancelled", cancelled, err)
	}
	if err := globex.SetRetriedAs(ctx, "run-a", "run-b"); err != nil {
		t.Fatalf("globex.SetRetriedAs: %v", err)
	}
	run, err := acme.GetRun(ctx, "run-a")
	if err != nil {
		t.Fatalf("another team's delete removed the run: %v", err)
	}
	if run.RetriedAs != "" || run.FinishedAt != nil {
		t.Fatalf("another team's writes changed the run: %+v", run)
	}
	if err := acme.DeleteRun(ctx, "run-a"); err != nil {
		t.Fatalf("acme.DeleteRun: %v", err)
	}
	if _, err := acme.GetRun(ctx, "run-a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("acme.GetRun after delete = %v, want ErrNotFound", err)
	}
}

// A trigger belongs to its team: another team's handle reads it as missing,
// cannot finish, beat, release or requeue its claim, and finds none of its
// children.
func TestTenantTriggersStayInTheirTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	acme := tenantFor(t, st, "acme")
	globex := tenantFor(t, st, "globex")
	now := time.Now()
	if err := acme.CreateTrigger(ctx, store.Trigger{ID: "trig-a", Pipeline: "deploy", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := acme.CreateTrigger(ctx, store.Trigger{
		ID: "child-a", Pipeline: "sub", ParentRunID: "trig-a", ParentNodeID: "build", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := globex.GetTrigger(ctx, "trig-a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("globex.GetTrigger = %v, want ErrNotFound", err)
	}
	if _, err := globex.TriggerClaimant(ctx, "trig-a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("globex.TriggerClaimant = %v, want ErrNotFound", err)
	}
	if _, err := globex.TriggerClaimGeneration(ctx, "trig-a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("globex.TriggerClaimGeneration = %v, want ErrNotFound", err)
	}
	if _, err := globex.HeartbeatTrigger(ctx, "trig-a", time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("globex.HeartbeatTrigger = %v, want ErrNotFound", err)
	}
	if err := globex.FinishTrigger(ctx, "trig-a"); err != nil {
		t.Fatalf("globex.FinishTrigger: %v", err)
	}
	for name, end := range map[string]func() (bool, error){
		"FinishTriggerAtGeneration": func() (bool, error) { return globex.FinishTriggerAtGeneration(ctx, "trig-a", 0) },
		"ReleaseClaimAtGeneration":  func() (bool, error) { return globex.ReleaseClaimAtGeneration(ctx, "trig-a", 0) },
		"RequeueUnstartedClaim":     func() (bool, error) { return globex.RequeueUnstartedClaim(ctx, "trig-a") },
	} {
		if ended, err := end(); err != nil || ended {
			t.Errorf("globex.%s = %v, %v, want nothing ended", name, ended, err)
		}
	}
	if id, err := globex.FindSpawnedChildTriggerID(ctx, "trig-a", "build", "sub"); err != nil || id != "" {
		t.Fatalf("globex.FindSpawnedChildTriggerID = %q, %v, want none", id, err)
	}
	if kids, err := globex.ListSpawnedChildrenByRun(ctx, "trig-a"); err != nil || len(kids) != 0 {
		t.Fatalf("globex.ListSpawnedChildrenByRun = %v, %v, want none", kids, err)
	}
	if ids, err := globex.ListPendingTriggersForParent(ctx, "trig-a"); err != nil || len(ids) != 0 {
		t.Fatalf("globex.ListPendingTriggersForParent = %v, %v, want none", ids, err)
	}
	trig, err := acme.GetTrigger(ctx, "trig-a")
	if err != nil || trig.Status != "pending" {
		t.Fatalf("acme.GetTrigger = %+v, %v, want the pending trigger", trig, err)
	}
	if id, err := acme.FindSpawnedChildTriggerID(ctx, "trig-a", "build", "sub"); err != nil || id != "child-a" {
		t.Fatalf("acme.FindSpawnedChildTriggerID = %q, %v, want child-a", id, err)
	}
}

// Bounce requests and metric samples belong to the run's team: another
// team's handle can neither write them onto the node nor read them back.
func TestTenantBouncesAndMetricsStayInTheRunsTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	acme := tenantFor(t, st, "acme")
	globex := tenantFor(t, st, "globex")
	seedTenantRun(t, acme, "run-a", "deploy")
	if err := st.CreateNode(ctx, store.Node{RunID: "run-a", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := acme.StartNode(ctx, "run-a", "build"); err != nil {
		t.Fatal(err)
	}

	if _, err := globex.RequestNodeBounce(ctx, "run-a", "build", "mallory"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("globex.RequestNodeBounce = %v, want ErrNotFound", err)
	}
	b, err := acme.RequestNodeBounce(ctx, "run-a", "build", "alice")
	if err != nil {
		t.Fatalf("acme.RequestNodeBounce: %v", err)
	}
	if got, err := globex.PendingNodeBounce(ctx, "run-a", "build"); err != nil || got != nil {
		t.Fatalf("globex.PendingNodeBounce = %+v, %v, want none", got, err)
	}
	if got, err := globex.ListNodeBounces(ctx, "run-a"); err != nil || len(got) != 0 {
		t.Fatalf("globex.ListNodeBounces = %v, %v, want none", got, err)
	}
	if err := globex.ConsumeNodeBounce(ctx, "run-a", "build", b.Seq, store.BounceMissed); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("globex.ConsumeNodeBounce = %v, want ErrNotFound", err)
	}
	if got, err := acme.PendingNodeBounce(ctx, "run-a", "build"); err != nil || got == nil || got.Seq != b.Seq {
		t.Fatalf("acme.PendingNodeBounce = %+v, %v, want the open request", got, err)
	}

	sample := store.MetricSample{TS: time.Now(), CPUMillicores: 5}
	if err := globex.AddNodeMetricSample(ctx, "run-a", "build", sample); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("globex.AddNodeMetricSample = %v, want ErrNotFound", err)
	}
	if err := acme.AddNodeMetricSample(ctx, "run-a", "build", sample); err != nil {
		t.Fatalf("acme.AddNodeMetricSample: %v", err)
	}
	if err := acme.AddNodeMetricSample(ctx, "run-a", "build", sample); err != nil {
		t.Fatalf("an identical retry: %v", err)
	}
	if got, err := globex.ListNodeMetricsPage(ctx, "run-a", "build", time.Time{}, 0); err != nil || len(got) != 0 {
		t.Fatalf("globex.ListNodeMetricsPage = %v, %v, want none", got, err)
	}
	if got, err := acme.ListNodeMetricsPage(ctx, "run-a", "build", time.Time{}, 0); err != nil || len(got) != 1 {
		t.Fatalf("acme.ListNodeMetricsPage = %v, %v, want the one sample", got, err)
	}
}
