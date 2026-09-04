package client_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

var coordinationRunnerScopes = []string{
	controller.ScopeNodesClaim,
	controller.ScopeTriggersClaim,
	controller.ScopeRunsState,
	controller.ScopeSecretsRead,
	controller.ScopeLogsWrite,
}

type coordinationFixture struct {
	runner *client.Client
	admin  *client.Client
	store  *store.Store
	ctx    context.Context
}

// safety: the runner client holds only the trigger claim on run r1 of pipeline demo,
// which is the whole standing an orchestrator process has while it drives a run.
func newCoordinationFixture(t *testing.T) coordinationFixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	adminRaw, _, err := st.CreateToken("root", store.TokenKindUser, []string{controller.ScopeAdmin}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken admin: %v", err)
	}
	runnerRaw, _, err := st.CreateToken("pool", store.TokenKindRunner, coordinationRunnerScopes, 0, now)
	if err != nil {
		t.Fatalf("CreateToken runner: %v", err)
	}

	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	t.Cleanup(srv.Close)

	ctx := context.Background()
	if err := st.CreateTrigger(ctx, store.Trigger{ID: "r1", Pipeline: "demo", CreatedAt: now}); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	f := coordinationFixture{
		runner: client.NewWithToken(srv.URL, srv.Client(), runnerRaw),
		admin:  client.NewWithToken(srv.URL, srv.Client(), adminRaw),
		store:  st,
	}
	claimed, err := f.runner.ClaimTrigger(ctx)
	if err != nil {
		t.Fatalf("ClaimTrigger: %v", err)
	}
	ctx = store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{ClaimGeneration: claimed.ClaimSeq})
	f.ctx = ctx
	if err := f.runner.CreateRun(ctx, store.Run{
		ID: "r1", Pipeline: "demo", Status: "running", StartedAt: now,
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := f.runner.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	return f
}

func TestClientTriggerLoopRoutesMatchTheStore(t *testing.T) {
	f := newCoordinationFixture(t)
	c, st := f.runner, f.store
	ctx := context.Background()

	if err := st.CreateTrigger(ctx, store.Trigger{
		ID: "child-1", Pipeline: "child", ParentRunID: "r1", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed the child trigger: %v", err)
	}

	pending, err := c.ListPendingTriggersForParent(ctx, "r1")
	if err != nil || len(pending) != 1 || pending[0] != "child-1" {
		t.Fatalf("ListPendingTriggersForParent = %v, %v; want [child-1], nil", pending, err)
	}
	want, err := st.ListPendingTriggersForParent(ctx, "r1")
	if err != nil {
		t.Fatalf("store list: %v", err)
	}
	if len(want) != 1 || want[0] != pending[0] {
		t.Fatalf("client ids %v, store ids %v", pending, want)
	}

	claimed, err := c.ClaimSpecificTrigger(ctx, "child-1", store.DefaultLeaseDuration)
	if err != nil || claimed.ID != "child-1" {
		t.Fatalf("ClaimSpecificTrigger = %v, %v", claimed, err)
	}
	claimedCtx := store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{ClaimGeneration: claimed.ClaimSeq})

	if err := c.CreateRun(claimedCtx, store.Run{
		ID: "child-1", Pipeline: "child", Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateRun for the trigger this client just claimed: %v", err)
	}
	if err := c.CreateNode(claimedCtx, store.Node{RunID: "child-1", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatalf("CreateNode on the claimed child run: %v", err)
	}

	if _, err := c.ClaimSpecificTrigger(ctx, "child-1", store.DefaultLeaseDuration); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second claim err = %v, want ErrNotFound", err)
	}
	if _, err := c.ClaimSpecificTrigger(ctx, "no-such-trigger", store.DefaultLeaseDuration); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("claim of a missing trigger err = %v, want ErrNotFound", err)
	}
	if err := c.FinishTrigger(claimedCtx, "child-1"); err != nil {
		t.Fatalf("FinishTrigger: %v", err)
	}
}

func TestClientCapacityRoutesLandInTheSameRows(t *testing.T) {
	f := newCoordinationFixture(t)
	c, st := f.runner, f.store
	ctx := context.Background()

	obs := store.ProfileObservation{
		Duration: 5 * time.Second, PeakCores: 2, SustainedCores: 1.25,
		PeakMemoryBytes: 1 << 30, CPUMeasured: true, PlanHash: "abc",
	}
	if err := c.RecordProfileObservation(ctx, "demo", "", obs); err != nil {
		t.Fatalf("RecordProfileObservation: %v", err)
	}
	if err := c.SetPipelinePin(ctx, "demo", "", 1.5, 1<<29); err != nil {
		t.Fatalf("SetPipelinePin: %v", err)
	}
	if err := c.RecordContention(ctx, "demo"); err != nil {
		t.Fatalf("RecordContention: %v", err)
	}
	if err := c.RecordWaitObservation(ctx, "demo", 750*time.Millisecond); err != nil {
		t.Fatalf("RecordWaitObservation: %v", err)
	}

	stored, err := st.GetPipelineProfile(ctx, "demo", "")
	if err != nil || stored == nil {
		t.Fatalf("store profile = %v, %v; want a row", stored, err)
	}
	if stored.SampleCount != 1 {
		t.Errorf("sample count = %d, want 1", stored.SampleCount)
	}
	if stored.PinnedCores != 1.5 {
		t.Errorf("pinned cores = %v, want 1.5", stored.PinnedCores)
	}
	fetched, err := c.GetPipelineProfile(ctx, "demo", "")
	if err != nil || fetched == nil {
		t.Fatalf("GetPipelineProfile = %v, %v", fetched, err)
	}
	if fetched.PinnedCores != stored.PinnedCores || fetched.SampleCount != stored.SampleCount {
		t.Errorf("client profile %+v disagrees with stored %+v", fetched, stored)
	}
}

func TestClientCapacityRoutesRefuseAnotherPipelineAndAbsurdInput(t *testing.T) {
	f := newCoordinationFixture(t)
	c := f.runner
	ctx := context.Background()

	if err := c.RecordProfileObservation(ctx, "release", "", store.ProfileObservation{
		Duration: time.Second, PeakCores: 1, CPUMeasured: true,
	}); err == nil {
		t.Error("recorded an observation against a pipeline this client holds no claim in")
	}
	if err := c.RecordWaitObservation(ctx, "release", time.Second); err == nil {
		t.Error("recorded a wait against a pipeline this client holds no claim in")
	}
	if err := c.RecordProfileObservation(ctx, "demo", "", store.ProfileObservation{
		PeakCores: -1, Duration: time.Second,
	}); err == nil {
		t.Error("a negative peak core count reached the pricing model")
	}
	if err := c.AddNodeUsage(ctx, "r1", "build", store.NodeUsage{CPUTime: -time.Second}); err == nil {
		t.Error("a negative CPU time was accepted")
	}
}

func TestClientNodeUsageAndMetricsRoundTrip(t *testing.T) {
	f := newCoordinationFixture(t)
	c, st := f.runner, f.store
	ctx := f.ctx

	sample := store.MetricSample{
		TS: time.Now().UTC().Truncate(time.Millisecond), CPUMillicores: 900,
		MemoryBytes: 1 << 20, CPUTime: 1500 * time.Millisecond,
	}
	if err := c.AddNodeMetricSample(ctx, "r1", "build", sample); err != nil {
		t.Fatalf("AddNodeMetricSample: %v", err)
	}
	samples, err := c.ListNodeMetrics(ctx, "r1", "build")
	if err != nil || len(samples) != 1 {
		t.Fatalf("ListNodeMetrics = %v, %v; want one sample", samples, err)
	}
	if samples[0].CPUMillicores != sample.CPUMillicores || samples[0].CPUTime != sample.CPUTime {
		t.Errorf("sample = %+v, want %+v", samples[0], sample)
	}
	if !samples[0].TS.Equal(sample.TS) {
		t.Errorf("sample ts = %v, want %v", samples[0].TS, sample.TS)
	}

	nodes, err := c.ListNodes(ctx, "r1")
	if err != nil || len(nodes) != 1 {
		t.Fatalf("ListNodes = %v, %v; want the run's one node", nodes, err)
	}

	if err := c.AddNodeUsage(ctx, "r1", "build", store.NodeUsage{
		CPUTime: 3 * time.Second, MaxRSSBytes: 8192, Wall: 4 * time.Second,
	}); err != nil {
		t.Fatalf("AddNodeUsage: %v", err)
	}
	n, err := st.GetNode(ctx, "r1", "build")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if n.CPUNanos != int64(3*time.Second) || n.MaxRSSBytes != 8192 || n.ProcessWallNanos != int64(4*time.Second) {
		t.Errorf("node usage = (%d, %d, %d), want (%d, 8192, %d)",
			n.CPUNanos, n.MaxRSSBytes, n.ProcessWallNanos, int64(3*time.Second), int64(4*time.Second))
	}
}

func TestClientReconcileOrphansClosesADeadRun(t *testing.T) {
	f := newCoordinationFixture(t)
	st := f.store
	ctx := context.Background()

	if err := st.CreateRun(ctx, store.Run{
		ID: "stale", Pipeline: "demo", Status: "running",
		StartedAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("create stale run: %v", err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE runs SET last_heartbeat_at = NULL WHERE id = ?`, "stale"); err != nil {
		t.Fatalf("clear heartbeat: %v", err)
	}
	n, err := f.admin.ReconcileOrphanedLocalRuns(ctx, time.Minute)
	if err != nil {
		t.Fatalf("ReconcileOrphanedLocalRuns: %v", err)
	}
	if n != 1 {
		t.Fatalf("reconciled = %d, want 1", n)
	}
	run, err := st.GetRun(ctx, "stale")
	if err != nil {
		t.Fatalf("get stale run: %v", err)
	}
	if run.Status == "running" {
		t.Error("stale run is still running after a reconcile")
	}
}

func TestClientReconcileOrphansRefusesANonPositiveThreshold(t *testing.T) {
	f := newCoordinationFixture(t)
	if _, err := f.admin.ReconcileOrphanedLocalRuns(context.Background(), 0); err == nil {
		t.Error("a zero threshold was accepted; it means every running run is orphaned")
	}
}

func TestClientReconcileOrphansIsAdminOnly(t *testing.T) {
	f := newCoordinationFixture(t)
	if _, err := f.runner.ReconcileOrphanedLocalRuns(context.Background(), time.Minute); err == nil {
		t.Error("a runner token swept the machine's orphaned runs, want 403")
	}
}
