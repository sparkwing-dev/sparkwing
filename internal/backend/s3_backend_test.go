package backend_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
)

// putState writes a state.ndjson dump for one run, matching the
// format orchestrator.dumpRunState produces.
func putState(t *testing.T, s storage.ArtifactStore, run store.Run, nodes ...store.Node) {
	t.Helper()
	var b strings.Builder
	enc := json.NewEncoder(&b)
	if err := enc.Encode(map[string]any{"kind": "run", "data": run}); err != nil {
		t.Fatalf("encode run: %v", err)
	}
	for _, n := range nodes {
		if err := enc.Encode(map[string]any{"kind": "node", "data": n}); err != nil {
			t.Fatalf("encode node: %v", err)
		}
	}
	if err := s.Put(context.Background(), "runs/"+run.ID+"/state.ndjson", strings.NewReader(b.String())); err != nil {
		t.Fatalf("Put state: %v", err)
	}
}

func TestS3Backend_Capabilities(t *testing.T) {
	t.Parallel()
	b := backend.NewS3Backend(mustFS(t), nil)
	got, err := b.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if got.Mode != "s3-only" {
		t.Errorf("Mode = %q, want s3-only", got.Mode)
	}
	if !got.ReadOnly {
		t.Errorf("ReadOnly = false, want true")
	}
	if got.Storage.Runs != "s3" {
		t.Errorf("Storage.Runs = %q, want s3", got.Storage.Runs)
	}
}

func TestS3Backend_ListRuns(t *testing.T) {
	t.Parallel()
	st := mustFS(t)
	b := backend.NewS3Backend(st, nil)

	now := time.Now().UTC().Truncate(time.Second)
	putState(t, st, mkRun("alpha", "build", "succeeded", now.Add(-2*time.Hour)),
		mkNode("alpha", "compile", "completed"),
	)
	putState(t, st, mkRun("beta", "deploy", "failed", now.Add(-1*time.Hour)))
	putState(t, st, mkRun("gamma", "build", "succeeded", now))

	runs, err := b.ListRuns(context.Background(), store.RunFilter{})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("ListRuns = %d runs, want 3", len(runs))
	}
	// Newest-first
	if runs[0].ID != "gamma" || runs[1].ID != "beta" || runs[2].ID != "alpha" {
		t.Fatalf("ListRuns order = %s,%s,%s, want gamma,beta,alpha",
			runs[0].ID, runs[1].ID, runs[2].ID)
	}

	runs, _ = b.ListRuns(context.Background(), store.RunFilter{Pipelines: []string{"build"}})
	if len(runs) != 2 {
		t.Fatalf("pipeline=build = %d, want 2", len(runs))
	}

	runs, _ = b.ListRuns(context.Background(), store.RunFilter{Statuses: []string{"failed"}})
	if len(runs) != 1 || runs[0].ID != "beta" {
		t.Fatalf("status=failed = %v, want [beta]", runs)
	}
}

func TestS3Backend_GetRunAndListNodes(t *testing.T) {
	t.Parallel()
	st := mustFS(t)
	b := backend.NewS3Backend(st, nil)
	ctx := context.Background()

	putState(t, st,
		mkRun("r1", "p", "succeeded", time.Now().UTC()),
		mkNode("r1", "n1", "completed"),
		mkNode("r1", "n2", "completed"),
	)

	got, err := b.GetRun(ctx, "r1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.ID != "r1" || got.Pipeline != "p" {
		t.Fatalf("GetRun = %+v", got)
	}
	nodes, err := b.ListNodes(ctx, "r1")
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("ListNodes = %d, want 2", len(nodes))
	}
	if _, err := b.GetRun(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetRun(missing) = %v, want ErrNotFound", err)
	}
}

func mustFS(t *testing.T) storage.ArtifactStore {
	t.Helper()
	a, err := fs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatalf("fs.NewArtifactStore: %v", err)
	}
	return a
}

func mkRun(id, pipeline, status string, started time.Time) store.Run {
	return store.Run{ID: id, Pipeline: pipeline, Status: status, StartedAt: started}
}

func mkNode(runID, nodeID, status string) store.Node {
	return store.Node{RunID: runID, NodeID: nodeID, Status: status}
}
