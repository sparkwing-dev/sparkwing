package orchestrator

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/s3state"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func coordinationStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func seedCoordinationRun(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
}

func TestLocalStateServesEveryRunCoordinationMethod(t *testing.T) {
	st := coordinationStore(t)
	seedCoordinationRun(t, st)
	ctx := context.Background()
	state := localState{st: st}

	nodes, err := state.ListNodes(ctx, "r1")
	if err != nil || len(nodes) != 1 {
		t.Fatalf("ListNodes = %d nodes, %v; want 1, nil", len(nodes), err)
	}

	sample := store.MetricSample{TS: time.Now().Truncate(time.Millisecond), CPUMillicores: 1500, MemoryBytes: 1 << 30, CPUTime: 3 * time.Second}
	if err := st.AddNodeMetricSample(ctx, "r1", "build", sample); err != nil {
		t.Fatalf("seed metric: %v", err)
	}
	samples, err := state.ListNodeMetrics(ctx, "r1", "build")
	if err != nil || len(samples) != 1 {
		t.Fatalf("ListNodeMetrics = %d samples, %v; want 1, nil", len(samples), err)
	}
	if samples[0].CPUTime != sample.CPUTime {
		t.Errorf("sample cpu time = %v, want %v", samples[0].CPUTime, sample.CPUTime)
	}

	if err := state.AddNodeUsage(ctx, "r1", "build", store.NodeUsage{CPUTime: 2 * time.Second, MaxRSSBytes: 4096, Wall: time.Second}); err != nil {
		t.Fatalf("AddNodeUsage: %v", err)
	}
	n, err := st.GetNode(ctx, "r1", "build")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if n.CPUNanos != int64(2*time.Second) || n.MaxRSSBytes != 4096 {
		t.Errorf("node usage = (%d, %d), want (%d, 4096)", n.CPUNanos, n.MaxRSSBytes, int64(2*time.Second))
	}

	childID, err := state.EnqueueTrigger(ctx, "child", nil, "r1", "build", "", "await-pipeline", "", "", "")
	if err != nil {
		t.Fatalf("EnqueueTrigger: %v", err)
	}
	pending, err := state.ListPendingTriggersForParent(ctx, "r1")
	if err != nil || len(pending) != 1 || pending[0] != childID {
		t.Fatalf("ListPendingTriggersForParent = %v, %v; want [%s], nil", pending, err, childID)
	}
	claimed, err := state.ClaimSpecificTrigger(ctx, childID, store.DefaultLeaseDuration)
	if err != nil || claimed.ID != childID {
		t.Fatalf("ClaimSpecificTrigger = %v, %v", claimed, err)
	}
	got, err := state.GetTrigger(ctx, childID)
	if err != nil || got.Pipeline != "child" {
		t.Fatalf("GetTrigger = %v, %v", got, err)
	}
	if err := state.FinishTrigger(ctx, childID); err != nil {
		t.Fatalf("FinishTrigger: %v", err)
	}

	obs := store.ProfileObservation{Duration: 5 * time.Second, PeakCores: 2, SustainedCores: 1, PeakMemoryBytes: 1 << 30, CPUMeasured: true}
	if err := state.RecordProfileObservation(ctx, "demo", "", obs); err != nil {
		t.Fatalf("RecordProfileObservation: %v", err)
	}
	if err := state.SetPipelinePin(ctx, "demo", "", 1.5, 1<<30); err != nil {
		t.Fatalf("SetPipelinePin: %v", err)
	}
	prof, err := state.GetPipelineProfile(ctx, "demo", "")
	if err != nil || prof == nil {
		t.Fatalf("GetPipelineProfile = %v, %v", prof, err)
	}
	if prof.PinnedCores != 1.5 {
		t.Errorf("pinned cores = %v, want 1.5", prof.PinnedCores)
	}
	if err := state.RecordContention(ctx, "demo"); err != nil {
		t.Fatalf("RecordContention: %v", err)
	}
	if err := state.RecordWaitObservation(ctx, "demo", 250*time.Millisecond); err != nil {
		t.Fatalf("RecordWaitObservation: %v", err)
	}

	if _, err := state.ReconcileOrphanedLocalRuns(ctx, time.Hour); err != nil {
		t.Fatalf("ReconcileOrphanedLocalRuns: %v", err)
	}
	if _, err := state.ReconcileOrphanedLocalRuns(ctx, 0); err == nil {
		t.Error("a zero threshold was accepted; it means every running run is orphaned")
	}
}

func TestLocalStateSetPipelinePinClearsWithoutCreatingARow(t *testing.T) {
	st := coordinationStore(t)
	ctx := context.Background()
	state := localState{st: st}

	if err := state.SetPipelinePin(ctx, "unmeasured", "", 0, 0); err != nil {
		t.Fatalf("SetPipelinePin: %v", err)
	}
	prof, err := state.GetPipelineProfile(ctx, "unmeasured", "")
	if err != nil {
		t.Fatalf("GetPipelineProfile: %v", err)
	}
	if prof != nil {
		t.Errorf("profile = %+v, want nil: clearing a pin must not create a row", prof)
	}
}

func TestS3StateAdapterReportsCoordinationUnsupported(t *testing.T) {
	adapter := s3StateAdapter{Backend: s3state.New(nil)}
	ctx := context.Background()

	checks := map[string]func() error{
		"ListNodes":                    func() error { _, err := adapter.ListNodes(ctx, "r1"); return err },
		"ListNodeMetrics":              func() error { _, err := adapter.ListNodeMetrics(ctx, "r1", "n"); return err },
		"AddNodeUsage":                 func() error { return adapter.AddNodeUsage(ctx, "r1", "n", store.NodeUsage{}) },
		"ListPendingTriggersForParent": func() error { _, err := adapter.ListPendingTriggersForParent(ctx, "r1"); return err },
		"ClaimSpecificTrigger":         func() error { _, err := adapter.ClaimSpecificTrigger(ctx, "t1", time.Minute); return err },
		"FinishTrigger":                func() error { return adapter.FinishTrigger(ctx, "t1") },
		"GetPipelineProfile":           func() error { _, err := adapter.GetPipelineProfile(ctx, "demo", ""); return err },
		"SetPipelinePin":               func() error { return adapter.SetPipelinePin(ctx, "demo", "", 1, 1) },
		"RecordProfileObservation": func() error {
			return adapter.RecordProfileObservation(ctx, "demo", "", store.ProfileObservation{})
		},
		"RecordContention":           func() error { return adapter.RecordContention(ctx, "demo") },
		"RecordWaitObservation":      func() error { return adapter.RecordWaitObservation(ctx, "demo", time.Second) },
		"ReconcileOrphanedLocalRuns": func() error { _, err := adapter.ReconcileOrphanedLocalRuns(ctx, time.Hour); return err },
	}
	for name, call := range checks {
		if err := call(); !errors.Is(err, storage.ErrNotSupported) {
			t.Errorf("%s err = %v, want ErrNotSupported", name, err)
		}
	}
}

func TestMirrorStateBackendKeepsCapacityWritesOnTheCanonical(t *testing.T) {
	canonical := coordinationStore(t)
	mirror := coordinationStore(t)
	seedCoordinationRun(t, canonical)
	seedCoordinationRun(t, mirror)
	ctx := context.Background()

	m := newMirrorStateBackend(localState{st: canonical}, mirror, quietTestLogger())
	if err := m.RecordProfileObservation(ctx, "demo", "", store.ProfileObservation{
		Duration: time.Second, PeakCores: 1, SustainedCores: 1, PeakMemoryBytes: 1 << 20, CPUMeasured: true,
	}); err != nil {
		t.Fatalf("RecordProfileObservation: %v", err)
	}
	if prof, err := canonical.GetPipelineProfile(ctx, "demo", ""); err != nil || prof == nil {
		t.Fatalf("canonical profile = %v, %v; want a row", prof, err)
	}
	prof, err := mirror.GetPipelineProfile(ctx, "demo", "")
	if err != nil {
		t.Fatalf("mirror profile: %v", err)
	}
	if prof != nil {
		t.Errorf("mirror profile = %+v, want nil: a per-run mirror must not collect machine capacity", prof)
	}

	if err := m.AddNodeUsage(ctx, "r1", "build", store.NodeUsage{CPUTime: time.Second, MaxRSSBytes: 2048}); err != nil {
		t.Fatalf("AddNodeUsage: %v", err)
	}
	for name, st := range map[string]*store.Store{"canonical": canonical, "mirror": mirror} {
		n, err := st.GetNode(ctx, "r1", "build")
		if err != nil {
			t.Fatalf("%s get node: %v", name, err)
		}
		if n.MaxRSSBytes != 2048 {
			t.Errorf("%s node max rss = %d, want 2048", name, n.MaxRSSBytes)
		}
	}
}

func TestLoopbackServesCoordinationOverAStateBackend(t *testing.T) {
	st := coordinationStore(t)
	seedCoordinationRun(t, st)
	ctx := context.Background()

	lb := controller.NewLoopback(localState{st: st}, "r1", "", quietTestLogger())
	srv := httptest.NewServer(lb.Handler())
	t.Cleanup(srv.Close)
	c := client.NewWithToken(srv.URL, srv.Client(), "")

	childID, err := localState{st: st}.EnqueueTrigger(ctx, "child", nil, "r1", "build", "", "await-pipeline", "", "", "")
	if err != nil {
		t.Fatalf("enqueue trigger: %v", err)
	}
	pending, err := c.ListPendingTriggersForParent(ctx, "r1")
	if err != nil || len(pending) != 1 || pending[0] != childID {
		t.Fatalf("ListPendingTriggersForParent = %v, %v; want [%s], nil", pending, err, childID)
	}
	if err := c.RecordWaitObservation(ctx, "demo", time.Second); err != nil {
		t.Fatalf("RecordWaitObservation: %v", err)
	}
	if err := c.AddNodeUsage(ctx, "r1", "build", store.NodeUsage{CPUTime: time.Second, MaxRSSBytes: 512}); err != nil {
		t.Fatalf("AddNodeUsage: %v", err)
	}
	n, err := st.GetNode(ctx, "r1", "build")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if n.MaxRSSBytes != 512 {
		t.Errorf("node max rss = %d, want 512", n.MaxRSSBytes)
	}
}
