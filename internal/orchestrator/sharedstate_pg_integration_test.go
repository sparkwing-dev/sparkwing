package orchestrator_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Mode 3 (direct-DB Postgres) integration tests. Two RunLocal
// invocations against the same Postgres schema and the same S3
// bucket; the second hits a coordinated cache reservation written by
// the first. The .Cache() DSL routes through pg's concurrency_cache
// + concurrency_holders tables, so cross-runner coalescing falls out
// for free.

var pgIntegRegisterOnce sync.Once

// Two separate invocation counters per pipeline so the two
// scenarios (cache-replay, coalesce) don't interfere. atomic.Int32
// is package-level state; the tests reset to 0 at start.

var (
	pgCacheReplayInvocations atomic.Int32
	pgCoalesceInvocations    atomic.Int32
)

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
	node.Cache(sparkwing.CacheOptions{
		Namespace:   "pg-integ-replay",
		OnLimit:     sparkwing.Coalesce,
		ContentHash: func(ctx context.Context) sparkwing.CacheKey { return sparkwing.Key("pg-integ", "replay-v1") },
	})
	return nil
}

type pgCoalesceJob struct {
	sparkwing.Base
	sparkwing.Produces[pgCacheReplayOut]
}

func (j *pgCoalesceJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", j.run), nil
}

func (pgCoalesceJob) run(ctx context.Context) (pgCacheReplayOut, error) {
	// A short delay widens the window for concurrent acquirers to
	// pile up behind the leader; without it, the leader might finish
	// before the followers even open their tx and the test reduces
	// to a sequential cache hit.
	time.Sleep(150 * time.Millisecond)
	pgCoalesceInvocations.Add(1)
	return pgCacheReplayOut{Tag: "pg-coalesced-v1"}, nil
}

type pgCoalescePipe struct{ sparkwing.Base }

func (pgCoalescePipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	node := sparkwing.Job(plan, "build", &pgCoalesceJob{})
	node.Cache(sparkwing.CacheOptions{
		Namespace:   "pg-integ-coalesce",
		OnLimit:     sparkwing.Coalesce,
		ContentHash: func(ctx context.Context) sparkwing.CacheKey { return sparkwing.Key("pg-integ", "coalesce-v1") },
	})
	return nil
}

func registerPgIntegPipelines(t *testing.T) {
	t.Helper()
	pgIntegRegisterOnce.Do(func() {
		sparkwing.Register[sparkwing.NoInputs]("pg-integ-replay",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return &pgCacheReplayPipe{} })
		sparkwing.Register[sparkwing.NoInputs]("pg-integ-coalesce",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return &pgCoalescePipe{} })
	})
}

// TestPgSharing_CoordinatedCacheReservation is the central Mode 3
// claim: two runs against the same Postgres schema sharing the same
// .Cache() key — the second sees an AcquireCached outcome (not just
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

	// Inspect Run B's build node — outcome must be Cached, evidence
	// the orchestrator routed it through applyCacheHit.
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

// TestPgSharing_ConcurrentRunsCoalesce is the contention test for the
// .Cache().OnLimit=Coalesce path: two RunLocal calls in parallel
// against the same uncached key. Exactly one runs the step; the
// other coalesces and inherits the output. Both runs reach success.
func TestPgSharing_ConcurrentRunsCoalesce(t *testing.T) {
	registerPgIntegPipelines(t)
	stA := openIntegrationPostgres(t)
	stB := openIntegrationPostgresAt(t, stA)

	art, logs := openIntegrationS3(t)
	paths := newPaths(t)
	pgCoalesceInvocations.Store(0)

	type result struct {
		res *orchestrator.Result
		err error
	}
	results := make([]result, 2)
	stores := []*store.Store{stA, stB}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, st := range stores {
		wg.Add(1)
		go func(i int, state *store.Store) {
			defer wg.Done()
			<-start
			res, err := orchestrator.RunLocal(context.Background(), paths, orchestrator.Options{
				Pipeline:      "pg-integ-coalesce",
				State:         state,
				LogStore:      logs,
				ArtifactStore: art,
			})
			results[i] = result{res: res, err: err}
		}(i, st)
	}
	close(start)
	wg.Wait()

	for i, r := range results {
		if r.err != nil {
			t.Errorf("Run %d err: %v", i, r.err)
			continue
		}
		if r.res.Status != "success" {
			t.Errorf("Run %d status = %q (err=%v)", i, r.res.Status, r.res.Error)
		}
	}

	got := pgCoalesceInvocations.Load()
	if got != 1 {
		t.Errorf("step invocations across two concurrent runs = %d, want 1 (the other should have coalesced)", got)
	}

	// Cross-check: exactly one cache row, populated by the leader.
	cacheRows, err := stA.CountConcurrencyCache(context.Background())
	if err != nil {
		t.Fatalf("CountConcurrencyCache: %v", err)
	}
	if cacheRows != 1 {
		t.Errorf("concurrency_cache rows = %d, want 1", cacheRows)
	}

	// One of the two runs' build node must be Cached (the coalesced
	// follower); the other must be Success (the leader).
	var leaderSeen, cachedSeen bool
	for i, r := range results {
		if r.res == nil {
			continue
		}
		var st *store.Store
		if i == 0 {
			st = stA
		} else {
			st = stB
		}
		nodes, _ := st.ListNodes(context.Background(), r.res.RunID)
		for _, n := range nodes {
			if n.NodeID != "build" {
				continue
			}
			switch n.Outcome {
			case "success":
				leaderSeen = true
			case string(sparkwing.Cached):
				cachedSeen = true
			}
		}
	}
	if !leaderSeen {
		t.Error("no leader run found; expected one Run's build node to have outcome=success")
	}
	if !cachedSeen {
		t.Error("no coalesced run found; expected the other Run's build node to have outcome=cached")
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
	_ = caps // shape varies; we just want to confirm the call doesn't fail.

	// Sanity: the pg path keeps using `?` placeholders only at the
	// store layer; ensure that didn't accidentally leak into the
	// rendered run.
	if strings.Contains(got.Pipeline, "?") {
		t.Errorf("unexpected '?' in pipeline name: %q", got.Pipeline)
	}
}
