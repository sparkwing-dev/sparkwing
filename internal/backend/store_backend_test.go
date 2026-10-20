package backend_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func newTestStore(t *testing.T) (*store.Store, paths.Paths) {
	t.Helper()
	dir := t.TempDir()
	st, err := teststore.OpenExisting(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st, paths.PathsAt(dir)
}

func TestStoreBackend_Capabilities(t *testing.T) {
	t.Parallel()
	st, _ := newTestStore(t)
	b := backend.NewStoreBackend(teststore.ForTeam(t, st, store.DefaultTeam), nil)
	got, err := b.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if got.Mode != "local" {
		t.Errorf("Mode = %q, want local", got.Mode)
	}
	if got.Storage.Logs != "fs" || got.Storage.Artifacts != "fs" {
		t.Errorf("Storage = %+v, want fs/fs", got.Storage)
	}
	if got.Storage.Runs != "sqlite" {
		t.Errorf("Storage.Runs = %q, want sqlite", got.Storage.Runs)
	}
	if got.ReadOnly {
		t.Errorf("ReadOnly = true, want false")
	}
}

func TestStoreBackend_RunsRoundTrip(t *testing.T) {
	t.Parallel()
	st, _ := newTestStore(t)
	b := backend.NewStoreBackend(teststore.ForTeam(t, st, store.DefaultTeam), nil)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	seedBackendRun(t, st, store.DefaultTeam, "r1", "demo", now)
	if err := teststore.SeedNode(st, ctx, store.Node{RunID: "r1", NodeID: "n1", Status: "pending"}); err != nil {
		t.Fatal(err)
	}

	got, err := b.GetRun(ctx, "r1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.ID != "r1" || got.Pipeline != "demo" {
		t.Errorf("GetRun = %+v", got)
	}
	nodes, err := b.ListNodes(ctx, "r1")
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].NodeID != "n1" {
		t.Errorf("ListNodes = %+v", nodes)
	}
	runs, err := b.ListRuns(ctx, store.RunFilter{})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Errorf("ListRuns len = %d, want 1", len(runs))
	}
}

func TestStoreBackendReadRequiresBoundLogs(t *testing.T) {
	st, _ := newTestStore(t)
	_, err := backend.NewStoreBackend(teststore.ForTeam(t, st, store.DefaultTeam), nil).ReadNodeLog(t.Context(), "r1", "n1", backend.ReadOpts{})
	if !errors.Is(err, storage.ErrNotSupported) {
		t.Fatalf("unbound log read = %v", err)
	}
}

func TestStoreBackendTenantReadsProperty(t *testing.T) {
	st, _ := newTestStore(t)
	random := rand.New(rand.NewPCG(919, 382))
	seedBackendRun(t, st, store.DefaultTeam, "private-default", "private", time.Unix(0, 1))
	if err := teststore.SeedNode(st, t.Context(), store.Node{RunID: "private-default", NodeID: "private-job", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.SQL(t, st).ExecContext(t.Context(), `INSERT INTO events(team,run_id,seq,ts,kind,payload) VALUES (?,'private-default',1,1,'private',?)`, store.DefaultTeam, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	for range 24 {
		team := store.Team(fmt.Sprintf("reader-%x", random.Uint64()))
		teststore.SeedTeam(t, st, team)
		tenant := teststore.ForTeam(t, st, team)
		run := fmt.Sprintf("run-%x", random.Uint64())
		seedBackendRun(t, st, team, run, "pipeline", time.Unix(0, 1))
		if err := teststore.SeedNode(st, t.Context(), store.Node{RunID: run, NodeID: "job", Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		if _, err := teststore.SQL(t, st).ExecContext(t.Context(), `INSERT INTO events(team,run_id,seq,ts,kind,payload) VALUES (?,?,1,1,'fixture',?)`, team, run, []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
		b := backend.NewStoreBackend(tenant, nil)
		if row, err := b.GetRun(t.Context(), run); err != nil || row == nil || row.ID != run {
			t.Fatalf("own run: %+v %v", row, err)
		}
		if row, err := b.GetRun(t.Context(), "private-default"); !errors.Is(err, store.ErrNotFound) || row != nil {
			t.Fatalf("default run: %+v %v", row, err)
		}
		if rows, err := b.ListNodes(t.Context(), "private-default"); err != nil || len(rows) != 0 {
			t.Fatalf("default jobs: %+v %v", rows, err)
		}
		if rows, err := b.ListEventsAfter(t.Context(), "private-default", 0, 100); err != nil || len(rows) != 0 {
			t.Fatalf("default events: %+v %v", rows, err)
		}
		rows, err := b.ListRuns(t.Context(), store.RunFilter{})
		if err != nil || len(rows) != 1 || rows[0].ID != run {
			t.Fatalf("team %s runs: %+v %v", team, rows, err)
		}
		nodes, err := b.ListNodes(t.Context(), run)
		if err != nil || len(nodes) != 1 || nodes[0].RunID != run {
			t.Fatalf("team %s jobs: %+v %v", team, nodes, err)
		}
		events, err := b.ListEventsAfter(t.Context(), run, 0, 100)
		if err != nil || len(events) != 1 || events[0].RunID != run {
			t.Fatalf("team %s events: %+v %v", team, events, err)
		}
		other := backend.NewStoreBackend(teststore.ForTeam(t, st, store.DefaultTeam), nil)
		if row, err := other.GetRun(t.Context(), run); !errors.Is(err, store.ErrNotFound) || row != nil {
			t.Fatalf("foreign run: %+v %v", row, err)
		}
		if rows, err := other.ListNodes(t.Context(), run); err != nil || len(rows) != 0 {
			t.Fatalf("foreign jobs: %+v %v", rows, err)
		}
		if rows, err := other.ListEventsAfter(t.Context(), run, 0, 100); err != nil || len(rows) != 0 {
			t.Fatalf("foreign events: %+v %v", rows, err)
		}
	}
}

func seedBackendRun(t *testing.T, st *store.Store, team store.Team, id, pipeline string, at time.Time) {
	t.Helper()
	if _, err := teststore.SQL(t, st).ExecContext(t.Context(), `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,started_at,log_retention_class,log_retention_days) VALUES (?,?,1,?,'running',?,'fixture',?,'gh:1','host','execute',?,'fixture',?,?,'r30',30)`, team, id, pipeline, []byte(`{"version":1}`), at.UnixNano(), []byte(`[]`), at.UnixNano(), at.UnixNano()); err != nil {
		t.Fatal(err)
	}
}
