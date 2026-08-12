package orchestrator_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	fsstore "github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// artifactAggregatePipe is two cached producers feeding an aggregator
// that consumes both. The aggregator reads each shard from its workspace
// and fails if either is absent, so a run only succeeds when staging
// delivered the complete input set -- the exact condition the old
// incomplete-set-on-partial-hit bug violated.
type artifactAggregatePipe struct{ sparkwing.Base }

func (artifactAggregatePipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	producer := func(id, rel, content string) *sparkwing.JobNode {
		return sparkwing.Job(plan, id, func(_ context.Context) error {
			p := filepath.Join(sparkwing.WorkDir(), filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			return os.WriteFile(p, []byte(content), 0o644)
		}).
			Outputs(rel).
			Cache(func(_ context.Context) sparkwing.CacheKey { return sparkwing.Key(id, "v1") })
	}
	s1 := producer("shard-1", "shards/1.txt", "one")
	s2 := producer("shard-2", "shards/2.txt", "two")

	sparkwing.Job(plan, "aggregate", func(_ context.Context) error {
		ws := sparkwing.WorkDir()
		a, err := os.ReadFile(filepath.Join(ws, "shards", "1.txt"))
		if err != nil {
			return fmt.Errorf("shard 1 missing: %w", err)
		}
		b, err := os.ReadFile(filepath.Join(ws, "shards", "2.txt"))
		if err != nil {
			return fmt.Errorf("shard 2 missing: %w", err)
		}
		return os.WriteFile(filepath.Join(ws, "combined.txt"), []byte(string(a)+"\n"+string(b)), 0o644)
	}).Consumes(s1).Consumes(s2)
	return nil
}

func init() {
	register("artifact-aggregate", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &artifactAggregatePipe{} })
}

func setWorkDir(t *testing.T, dir string) {
	t.Helper()
	orig := sparkwing.CurrentRuntime().WorkDir
	sparkwing.SetWorkDir(dir)
	t.Cleanup(func() { sparkwing.SetWorkDir(orig) })
}

// TestArtifacts_AggregatorStagesFullSetOnCacheHit runs the aggregate
// pipeline twice against a shared store and artifact store, the second
// run in a fresh empty workspace. On run 2 both producers cache-hit and
// write nothing to disk, so the aggregator sees its inputs only if
// staging materialized them from the recorded manifests. The fresh
// workspace also rules out the local shared-tree accident: anything the
// aggregator reads got there by staging.
func TestArtifacts_AggregatorStagesFullSetOnCacheHit(t *testing.T) {
	art, err := fsstore.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewArtifactStore: %v", err)
	}
	p := newPaths(t)
	if err := p.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	ws1 := t.TempDir()
	setWorkDir(t, ws1)
	res1, err := orchestrator.Run(ctx, orchestrator.LocalBackends(p, st, art),
		orchestrator.Options{Pipeline: "artifact-aggregate"})
	if err != nil || res1.Status != "success" {
		t.Fatalf("run 1: status=%v err=%v", res1.Status, err)
	}
	if got := mustRead(t, filepath.Join(ws1, "combined.txt")); got != "one\ntwo" {
		t.Fatalf("run 1 combined = %q", got)
	}

	ws2 := t.TempDir()
	setWorkDir(t, ws2)
	res2, err := orchestrator.Run(ctx, orchestrator.LocalBackends(p, st, art),
		orchestrator.Options{Pipeline: "artifact-aggregate"})
	if err != nil || res2.Status != "success" {
		t.Fatalf("run 2 (cache-hit producers): status=%v err=%v", res2.Status, err)
	}
	if prod := findNode(t, st, res2.RunID, "shard-1"); prod.Outcome != string(sparkwing.Cached) {
		t.Fatalf("run 2 shard-1 outcome = %q, want cached", prod.Outcome)
	}
	if got := mustRead(t, filepath.Join(ws2, "combined.txt")); got != "one\ntwo" {
		t.Fatalf("run 2 combined = %q, want one\\ntwo (staging delivered the full set)", got)
	}
	if got := mustRead(t, filepath.Join(ws2, "shards", "1.txt")); got != "one" {
		t.Fatalf("run 2 shard file not staged: %q", got)
	}
}

// TestArtifacts_StagedInDistributedMode exercises the same publish-then-
// stage round trip with state served over HTTP by an in-process
// controller (RemoteBackends), proving staging is mode-agnostic: the
// producer's manifest is recorded and the consumer resolves it across the
// remote state surface. The second run uses a fresh workspace so the
// aggregator's inputs can only come from the shared artifact store.
func TestArtifacts_StagedInDistributedMode(t *testing.T) {
	art, err := fsstore.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewArtifactStore: %v", err)
	}
	ctrlStore, err := store.Open(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatalf("controller store: %v", err)
	}
	t.Cleanup(func() { _ = ctrlStore.Close() })
	srv := httptest.NewServer(controller.New(ctrlStore, nil).Handler())
	t.Cleanup(srv.Close)
	c := client.NewWithToken(srv.URL, nil, "")
	ctx := context.Background()

	run := func(ws string) *orchestrator.Result {
		setWorkDir(t, ws)
		res, err := orchestrator.Run(ctx,
			orchestrator.RemoteBackends(c, nil, art, nil, store.DefaultConcurrencyLease),
			orchestrator.Options{Pipeline: "artifact-aggregate"})
		if err != nil || res.Status != "success" {
			t.Fatalf("distributed run: status=%v err=%v", res.Status, err)
		}
		return res
	}

	run(t.TempDir())
	ws2 := t.TempDir()
	run(ws2)
	if got := mustRead(t, filepath.Join(ws2, "combined.txt")); got != "one\ntwo" {
		t.Fatalf("distributed run 2 combined = %q, want one\\ntwo", got)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
