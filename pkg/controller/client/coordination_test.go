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

func coordinationFixture(t *testing.T) (*client.Client, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	t.Cleanup(srv.Close)

	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	return client.NewWithToken(srv.URL, srv.Client(), ""), st
}

func TestClientTriggerLoopRoutesMatchTheStore(t *testing.T) {
	c, st := coordinationFixture(t)
	ctx := context.Background()

	if _, err := c.ListPendingTriggersForParent(ctx, "r1"); err != nil {
		t.Fatalf("ListPendingTriggersForParent on an empty parent: %v", err)
	}
	if _, err := c.CreateTrigger(ctx, client.TriggerRequest{
		Pipeline: "child", ParentRunID: "r1", ParentNodeID: "build",
		Trigger: client.TriggerMeta{Source: "await-pipeline"},
	}); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	pending, err := c.ListPendingTriggersForParent(ctx, "r1")
	if err != nil || len(pending) != 1 {
		t.Fatalf("ListPendingTriggersForParent = %v, %v; want one id", pending, err)
	}
	want, err := st.ListPendingTriggersForParent(ctx, "r1")
	if err != nil {
		t.Fatalf("store list: %v", err)
	}
	if len(want) != 1 || want[0] != pending[0] {
		t.Fatalf("client ids %v, store ids %v", pending, want)
	}

	claimed, err := c.ClaimSpecificTrigger(ctx, pending[0], store.DefaultLeaseDuration)
	if err != nil || claimed.ID != pending[0] {
		t.Fatalf("ClaimSpecificTrigger = %v, %v", claimed, err)
	}
	if _, err := c.ClaimSpecificTrigger(ctx, pending[0], store.DefaultLeaseDuration); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second claim err = %v, want ErrNotFound", err)
	}
	if _, err := c.ClaimSpecificTrigger(ctx, "no-such-trigger", store.DefaultLeaseDuration); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("claim of a missing trigger err = %v, want ErrNotFound", err)
	}
	if err := c.FinishTrigger(ctx, pending[0]); err != nil {
		t.Fatalf("FinishTrigger: %v", err)
	}
}

func TestClientCapacityRoutesLandInTheSameRows(t *testing.T) {
	c, st := coordinationFixture(t)
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

func TestClientNodeUsageAndMetricsRoundTrip(t *testing.T) {
	c, st := coordinationFixture(t)
	ctx := context.Background()

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
	c, st := coordinationFixture(t)
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
	n, err := c.ReconcileOrphanedLocalRuns(ctx, time.Minute)
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
	c, _ := coordinationFixture(t)
	if _, err := c.ReconcileOrphanedLocalRuns(context.Background(), 0); err == nil {
		t.Error("a zero threshold was accepted; it means every running run is orphaned")
	}
}
