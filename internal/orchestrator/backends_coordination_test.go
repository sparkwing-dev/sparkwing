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
	if _, err := state.ReconcileOrphanedLocalRuns(ctx, 0); !errors.Is(err, ErrOrphanThresholdRequired) {
		t.Errorf("zero-threshold err = %v, want ErrOrphanThresholdRequired", err)
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
	backend := s3state.New(nil)
	t.Cleanup(func() { _ = backend.Close() })
	adapter := s3StateAdapter{Backend: backend}
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
	canonicalNode, err := canonical.GetNode(ctx, "r1", "build")
	if err != nil {
		t.Fatalf("canonical get node: %v", err)
	}
	if canonicalNode.MaxRSSBytes != 2048 {
		t.Errorf("canonical node max rss = %d, want 2048", canonicalNode.MaxRSSBytes)
	}
	mirrorNode, err := mirror.GetNode(ctx, "r1", "build")
	if err != nil {
		t.Fatalf("mirror get node: %v", err)
	}
	if mirrorNode.MaxRSSBytes != 0 {
		t.Errorf("mirror node max rss = %d, want 0: usage is machine capacity accounting", mirrorNode.MaxRSSBytes)
	}
}

func TestCanonicalStateUnwrapsTheMirrorSoAChildStaysOutOfIt(t *testing.T) {
	canonical := coordinationStore(t)
	mirror := coordinationStore(t)
	ctx := context.Background()

	m := newMirrorStateBackend(localState{st: canonical}, mirror, quietTestLogger())
	child := store.Run{ID: "child-1", Pipeline: "child", Status: "failed", StartedAt: time.Now()}

	if err := m.CreateRun(ctx, child); err != nil {
		t.Fatalf("CreateRun through the mirror: %v", err)
	}
	if _, err := mirror.GetRun(ctx, "child-1"); err != nil {
		t.Fatalf("the mirror tees run rows, so this one should be there: %v", err)
	}

	other := coordinationStore(t)
	m2 := newMirrorStateBackend(localState{st: canonical}, other, quietTestLogger())
	if err := canonicalState(m2).CreateRun(ctx, store.Run{
		ID: "child-2", Pipeline: "child", Status: "failed", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun through the canonical backend: %v", err)
	}
	if _, err := canonical.GetRun(ctx, "child-2"); err != nil {
		t.Fatalf("canonical missing the child run: %v", err)
	}
	if _, err := other.GetRun(ctx, "child-2"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("mirror GetRun err = %v, want ErrNotFound: a child run belongs to no run's mirror", err)
	}
}

const loopbackTestToken = "swl_coordination"

func newCoordinationLoopback(t *testing.T, st *store.Store) *client.Client {
	t.Helper()
	lb := controller.NewLoopback(localState{st: st}, "r1", loopbackTestToken, quietTestLogger())
	srv := httptest.NewServer(lb.Handler())
	t.Cleanup(srv.Close)
	return client.NewWithToken(srv.URL, srv.Client(), loopbackTestToken)
}

func TestLoopbackServesCoordinationOverAStateBackend(t *testing.T) {
	st := coordinationStore(t)
	seedCoordinationRun(t, st)
	ctx := context.Background()
	c := newCoordinationLoopback(t, st)

	childID, err := localState{st: st}.EnqueueTrigger(ctx, "child", nil, "r1", "build", "", "await-pipeline", "", "", "")
	if err != nil {
		t.Fatalf("enqueue trigger: %v", err)
	}
	pending, err := c.ListPendingTriggersForParent(ctx, "r1")
	if err != nil || len(pending) != 1 || pending[0] != childID {
		t.Fatalf("ListPendingTriggersForParent = %v, %v; want [%s], nil", pending, err, childID)
	}
	if _, err := c.ClaimSpecificTrigger(ctx, childID, store.DefaultLeaseDuration); err != nil {
		t.Fatalf("ClaimSpecificTrigger on a child this run spawned: %v", err)
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

func TestLoopbackRefusesWorkBelongingToAnotherRun(t *testing.T) {
	st := coordinationStore(t)
	seedCoordinationRun(t, st)
	ctx := context.Background()
	c := newCoordinationLoopback(t, st)

	if err := st.CreateRun(ctx, store.Run{
		ID: "r2", Pipeline: "other", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create the stranger run: %v", err)
	}
	strangerID, err := localState{st: st}.EnqueueTrigger(ctx, "elsewhere", nil, "r2", "build", "", "await-pipeline", "", "", "")
	if err != nil {
		t.Fatalf("enqueue the stranger trigger: %v", err)
	}

	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"claim another run's child trigger", func() error {
			_, err := c.ClaimSpecificTrigger(ctx, strangerID, store.DefaultLeaseDuration)
			return err
		}},
		{"finish another run's child trigger", func() error {
			return c.FinishTrigger(ctx, strangerID)
		}},
		{"list another run's pending triggers", func() error {
			_, err := c.ListPendingTriggersForParent(ctx, "r2")
			return err
		}},
		{"record an observation against another pipeline", func() error {
			return c.RecordProfileObservation(ctx, "other", "", store.ProfileObservation{
				Duration: time.Second, PeakCores: 1, CPUMeasured: true,
			})
		}},
		{"record contention against another pipeline", func() error {
			return c.RecordContention(ctx, "other")
		}},
		{"record a wait against another pipeline", func() error {
			return c.RecordWaitObservation(ctx, "other", time.Second)
		}},
		{"pin another pipeline", func() error {
			return c.SetPipelinePin(ctx, "other", "", 4, 1<<30)
		}},
	} {
		if err := tc.call(); err == nil {
			t.Errorf("%s: succeeded, want a refusal", tc.name)
		}
	}

	if tg, err := st.GetTrigger(ctx, strangerID); err != nil || tg.Status != "pending" {
		t.Errorf("stranger trigger status = %v (err %v), want it untouched and pending", tg, err)
	}
	if prof, err := st.GetPipelineProfile(ctx, "other", ""); err != nil || prof != nil {
		t.Errorf("stranger profile = %v (err %v), want none written", prof, err)
	}
}

func TestLoopbackRefusesAnAbsurdProfileObservation(t *testing.T) {
	st := coordinationStore(t)
	seedCoordinationRun(t, st)
	c := newCoordinationLoopback(t, st)

	if err := c.RecordProfileObservation(context.Background(), "demo", "", store.ProfileObservation{
		PeakCores: -5, Duration: time.Second,
	}); err == nil {
		t.Error("a negative peak core count was accepted into the pricing model")
	}
}

func TestLoopbackScopesProfileWritesToItsRunsRepository(t *testing.T) {
	st := coordinationStore(t)
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID: "r1", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
		Repo: "acme/web", RepoURL: "https://github.com/acme/web.git",
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	c := newCoordinationLoopback(t, st)

	own := store.JoinProfileKey("github.com/acme/web", "demo")
	other := store.JoinProfileKey("github.com/evil/other", "demo")
	measurement := store.ProfileObservation{
		Duration: time.Minute, PeakCores: 2, SustainedCores: 1, PeakMemoryBytes: 1 << 30, CPUMeasured: true,
	}

	if err := c.RecordProfileObservation(ctx, own, "", measurement); err != nil {
		t.Fatalf("observation on this run's own repository: %v", err)
	}
	if err := c.SetPipelinePin(ctx, own, "", 4, 1<<30); err != nil {
		t.Fatalf("pin on this run's own repository: %v", err)
	}
	if err := c.RecordProfileObservation(ctx, other, "", measurement); err == nil {
		t.Error("wrote another repository's profile through a loopback bound to this run")
	}
	if err := c.SetPipelinePin(ctx, other, "", 4, 1<<30); err == nil {
		t.Error("pinned another repository's pipeline through a loopback bound to this run")
	}
	if prof, err := st.GetPipelineProfile(ctx, other, ""); err != nil || prof != nil {
		t.Errorf("other-repository profile = %v (err %v), want none", prof, err)
	}
	if prof, err := st.GetPipelineProfile(ctx, own, ""); err != nil || prof == nil || prof.PinnedCores != 4 {
		t.Fatalf("own profile = %v (err %v), want a row pinned at 4 cores", prof, err)
	}
}
