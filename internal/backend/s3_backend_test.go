package backend_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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
	putState(
		t, st, mkRun("alpha", "build", "succeeded", now.Add(-2*time.Hour)),
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

func TestS3Backend_ListRunsFiltersBeforeLimit(t *testing.T) {
	t.Parallel()
	st := mustFS(t)
	b := backend.NewS3Backend(st, nil)
	base := time.Now().UTC().Add(-time.Hour)
	older := mkRun("older-match", "build", "succeeded", base)
	older.GitBranch = "rare"
	older.GitSHA = "deadbeef1234"
	putState(t, st, older)
	for i := range 201 {
		run := mkRun(fmt.Sprintf("newer-%03d", i), "build", "succeeded", base.Add(time.Duration(i+1)*time.Second))
		run.GitBranch = "main"
		run.GitSHA = "cafebabe1234"
		putState(t, st, run)
	}
	for _, filter := range []store.RunFilter{
		{GitBranches: []string{"rare"}, Limit: 200},
		{GitSHAPrefixes: []string{"deadbee"}, Limit: 200},
		{GitBranches: []string{"rare"}, GitSHAPrefixes: []string{"deadbee"}, Limit: 200},
	} {
		runs, err := b.ListRuns(context.Background(), filter)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) != 1 || runs[0].ID != older.ID {
			first := ""
			if len(runs) > 0 {
				first = runs[0].ID
			}
			t.Fatalf("ListRuns(%+v) = %d runs, first %q; want older-match", filter, len(runs), first)
		}
	}
}

func TestS3Backend_GetRunAndListNodes(t *testing.T) {
	t.Parallel()
	st := mustFS(t)
	b := backend.NewS3Backend(st, nil)
	ctx := context.Background()

	putState(
		t, st,
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

func TestS3Backend_ListEventsAfter_ReadsEventEnvelopes(t *testing.T) {
	t.Parallel()
	st := mustFS(t)
	b := backend.NewS3Backend(st, nil)
	b.SetLiveTTL(0)
	ctx := context.Background()

	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	_ = enc.Encode(map[string]any{"kind": "run", "data": mkRun("r", "p", "running", time.Now().UTC())})
	_ = enc.Encode(map[string]any{"kind": "event", "data": store.Event{RunID: "r", Seq: 1, Kind: "run_start", TS: time.Now().UTC()}})
	_ = enc.Encode(map[string]any{"kind": "event", "data": store.Event{RunID: "r", Seq: 2, Kind: "node_start", TS: time.Now().UTC()}})
	_ = enc.Encode(map[string]any{"kind": "event", "data": store.Event{RunID: "r", Seq: 3, Kind: "node_end", TS: time.Now().UTC()}})
	if err := st.Put(ctx, "runs/r/state.ndjson", strings.NewReader(buf.String())); err != nil {
		t.Fatalf("Put: %v", err)
	}

	all, err := b.ListEventsAfter(ctx, "r", 0, 100)
	if err != nil {
		t.Fatalf("ListEventsAfter: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("len=%d, want 3", len(all))
	}
	after2, err := b.ListEventsAfter(ctx, "r", 2, 100)
	if err != nil {
		t.Fatalf("ListEventsAfter(2): %v", err)
	}
	if len(after2) != 1 || after2[0].Kind != "node_end" {
		t.Fatalf("after seq=2 = %+v", after2)
	}
}
