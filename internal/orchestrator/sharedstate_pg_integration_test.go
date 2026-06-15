package orchestrator_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var pgIntegRegisterOnce sync.Once

var pgCacheReplayInvocations atomic.Int32

type pgCacheReplayOut struct {
	Tag string `json:"tag"`
}

type pgCacheReplayJob struct {
	sparkwing.Base
	sparkwing.Produces[pgCacheReplayOut]
}

func (j *pgCacheReplayJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", j.run), nil
}

func (pgCacheReplayJob) run(ctx context.Context) (pgCacheReplayOut, error) {
	pgCacheReplayInvocations.Add(1)
	return pgCacheReplayOut{Tag: "pg-cached-v1"}, nil
}

type pgCacheReplayPipe struct{ sparkwing.Base }

func (pgCacheReplayPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	node := sparkwing.Job(plan, "build", &pgCacheReplayJob{})
	node.Cache(func(ctx context.Context) sparkwing.CacheKey { return sparkwing.Key("pg-integ", "replay-v1") })
	return nil
}

func registerPgIntegPipelines(t *testing.T) {
	t.Helper()
	pgIntegRegisterOnce.Do(func() {
		sparkwing.Register[sparkwing.NoInputs]("pg-integ-replay",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return &pgCacheReplayPipe{} })
	})
}

// TestPgSharing_CoordinatedCacheReservation is the central Mode 3
// claim: two runs against the same Postgres schema sharing the same
// .Cache() key -- the second sees an AcquireCached outcome (not just
// a content-addressed blob HEAD). Verify via the concurrency_cache
// row written by Run A and the cached outcome on Run B's node.
func TestPgSharing_CoordinatedCacheReservation(t *testing.T) {
	registerPgIntegPipelines(t)
	stA := openIntegrationPostgres(t)
	stB := openIntegrationPostgresAt(t, stA)

	art, logs := openIntegrationS3(t)
	paths := newPaths(t)
	pgCacheReplayInvocations.Store(0)

	resA, err := orchestrator.RunLocal(context.Background(), paths, orchestrator.Options{
		Pipeline:      "pg-integ-replay",
		State:         stA,
		LogStore:      logs,
		ArtifactStore: art,
	})
	if err != nil {
		t.Fatalf("Run A: %v", err)
	}
	if resA.Status != "success" {
		t.Fatalf("Run A status = %q (err=%v)", resA.Status, resA.Error)
	}
	if got := pgCacheReplayInvocations.Load(); got != 1 {
		t.Fatalf("Run A invocations = %d, want 1", got)
	}

	cacheRows, err := stA.CountConcurrencyCache(context.Background())
	if err != nil {
		t.Fatalf("CountConcurrencyCache: %v", err)
	}
	if cacheRows == 0 {
		t.Fatal("concurrency_cache empty after Run A; expected a reservation row")
	}

	resB, err := orchestrator.RunLocal(context.Background(), paths, orchestrator.Options{
		Pipeline:      "pg-integ-replay",
		State:         stB,
		LogStore:      logs,
		ArtifactStore: art,
	})
	if err != nil {
		t.Fatalf("Run B: %v", err)
	}
	if resB.Status != "success" {
		t.Fatalf("Run B status = %q (err=%v)", resB.Status, resB.Error)
	}
	if got := pgCacheReplayInvocations.Load(); got != 1 {
		t.Errorf("invocations after Run B = %d, want 1 (Run B should have hit the pg-coordinated cache)", got)
	}

	nodes, err := stB.ListNodes(context.Background(), resB.RunID)
	if err != nil {
		t.Fatalf("ListNodes B: %v", err)
	}
	var build *store.Node
	for _, n := range nodes {
		if n.NodeID == "build" {
			build = n
			break
		}
	}
	if build == nil {
		t.Fatalf("Run B has no build node; nodes=%+v", nodes)
	}
	if build.Outcome != string(sparkwing.Cached) {
		t.Errorf("Run B build outcome = %q, want %q (cached)", build.Outcome, sparkwing.Cached)
	}
}

// TestPgSharing_StateVisibleToStoreBackend verifies the dashboard
// path: after Run A completes, a fresh StoreBackend over the same
// *store.Store handle reflects Run A's writes. This is the assertion
// that backs the cmd/sparkwing-web --state-spec=postgres://... flow.
func TestPgSharing_StateVisibleToStoreBackend(t *testing.T) {
	registerPgIntegPipelines(t)
	st := openIntegrationPostgres(t)
	art, logs := openIntegrationS3(t)
	paths := newPaths(t)
	pgCacheReplayInvocations.Store(0)

	res, err := orchestrator.RunLocal(context.Background(), paths, orchestrator.Options{
		Pipeline:      "pg-integ-replay",
		State:         st,
		LogStore:      logs,
		ArtifactStore: art,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q", res.Status)
	}

	b := backend.NewStoreBackend(st, paths, logs)
	got, err := b.GetRun(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got == nil || got.ID != res.RunID {
		t.Fatalf("GetRun returned %+v, want id %q", got, res.RunID)
	}
	if got.Pipeline != "pg-integ-replay" {
		t.Errorf("pipeline = %q, want pg-integ-replay", got.Pipeline)
	}

	nodes, err := b.ListNodes(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) == 0 {
		t.Error("ListNodes returned no nodes")
	}

	runs, err := b.ListRuns(context.Background(), store.RunFilter{})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	found := false
	for _, r := range runs {
		if r.ID == res.RunID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ListRuns missed run %s", res.RunID)
	}

	caps, err := b.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	_ = caps

	if strings.Contains(got.Pipeline, "?") {
		t.Errorf("unexpected '?' in pipeline name: %q", got.Pipeline)
	}
}
