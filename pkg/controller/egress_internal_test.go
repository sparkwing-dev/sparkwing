package controller

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func egressServer(t *testing.T, cfg egress.Config) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(st, nil).WithEgressMeter(egress.New(cfg)), st
}

func TestFlushEgressUsageWritesOnlyWhatMoved(t *testing.T) {
	ctx := context.Background()
	cfg := egress.Config{PerPrincipalMonthlyBytes: 1 << 20}
	s, st := egressServer(t, cfg)

	s.egress.Record("alice", egress.ClassArtifact, 400)
	s.sweepEgressUsage(ctx)

	rows, err := st.ListEgressUsage(ctx, "")
	if err != nil {
		t.Fatalf("ListEgressUsage: %v", err)
	}
	if len(rows) != 1 || rows[0].Principal != "alice" || rows[0].Bytes != 400 {
		t.Fatalf("rows = %+v, want alice at 400", rows)
	}

	// safety: a sweep with nothing served must not write, which is what
	// keeps metering off the per-response write path.
	s.sweepEgressUsage(ctx)
	s.egress.Record("alice", egress.ClassArtifact, 100)
	s.sweepEgressUsage(ctx)
	rows, err = st.ListEgressUsage(ctx, "")
	if err != nil {
		t.Fatalf("ListEgressUsage: %v", err)
	}
	if len(rows) != 1 || rows[0].Bytes != 500 {
		t.Fatalf("rows = %+v, want alice raised to 500", rows)
	}

	restarted := New(st, nil).WithEgressMeter(egress.New(cfg))
	restarted.loadEgressUsage(ctx)
	if got := restarted.egress.State().GlobalMonthBytes; got != 500 {
		t.Fatalf("a restarted controller resumed at %d bytes, want 500", got)
	}
	if err := restarted.egress.Check("alice"); err != nil {
		t.Fatalf("Check under the reloaded budget = %v, want nil", err)
	}
}

func TestEgressHooksAreInertWithoutAMeter(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := New(st, nil)

	s.loadEgressUsage(ctx)
	s.sweepEgressUsage(ctx)
	state, problems := s.egressHealth()
	if state["enabled"] != false || len(problems) != 0 {
		t.Fatalf("health without a meter = %+v, %v; want disabled and no problem", state, problems)
	}
	if s.EgressMeter() != nil {
		t.Error("a controller given no meter reports one")
	}
}

func TestTheSweepPrunesMonthsPastRetentionExactlyOnce(t *testing.T) {
	ctx := context.Background()
	s, st := egressServer(t, egress.Config{})

	stale := time.Now().UTC().AddDate(0, -(store.EgressUsageRetentionMonths + 1), 0).Format("2006-01")
	keep := time.Now().UTC().AddDate(0, -1, 0).Format("2006-01")
	if err := st.RecordEgressUsage(ctx, []store.EgressUsage{
		{Principal: "alice", Month: stale, Bytes: 9},
		{Principal: "alice", Month: keep, Bytes: 9},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	s.sweepEgressUsage(ctx)
	rows, err := st.ListEgressUsage(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Month != keep {
		t.Fatalf("rows after the sweep = %+v, want only %s", rows, keep)
	}

	// safety: the prune runs once a month, not on every ten-second sweep,
	// so a second sweep must not reach the database again.
	if err := st.RecordEgressUsage(ctx, []store.EgressUsage{
		{Principal: "bob", Month: stale, Bytes: 9},
	}); err != nil {
		t.Fatalf("seed a second stale row: %v", err)
	}
	s.sweepEgressUsage(ctx)
	rows, err = st.ListEgressUsage(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows after a second sweep = %+v, want the stale row left for next month", rows)
	}
}

// safety: the sweep drains this meter whenever the server has a store, so
// marking it must not depend on where in the builder chain the call lands.
func TestPersistenceDoesNotDependOnBuilderOrder(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	for _, tc := range []struct {
		name  string
		build func(*store.Store) *Server
	}{
		{"meter first", func(st *store.Store) *Server {
			return New(st, nil).WithEgressMeter(egress.New(egress.Config{})).WithCacheURL("http://cache")
		}},
		{"meter last", func(st *store.Store) *Server {
			return New(st, nil).WithCacheURL("http://cache").WithEgressMeter(egress.New(egress.Config{}))
		}},
		{"config carries it", func(st *store.Store) *Server {
			return New(st, nil).WithEgressMeter(egress.New(egress.Config{Persisted: true}))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			if !tc.build(st).egress.State().Persisted {
				t.Fatal("the meter does not park a closing month, so the sweep would drop it")
			}
		})
	}

	// safety: a server with no store drains nothing, so its meter must not
	// park either.
	if New(nil, nil).WithEgressMeter(egress.New(egress.Config{})).egress.State().Persisted {
		t.Error("a storeless controller marked its meter as drained")
	}
}
