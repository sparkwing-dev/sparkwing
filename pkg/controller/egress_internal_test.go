package controller

import (
	"context"
	"path/filepath"
	"testing"

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
	s.flushEgressUsage(ctx)

	rows, err := st.ListEgressUsage(ctx, "")
	if err != nil {
		t.Fatalf("ListEgressUsage: %v", err)
	}
	if len(rows) != 1 || rows[0].Principal != "alice" || rows[0].Bytes != 400 {
		t.Fatalf("rows = %+v, want alice at 400", rows)
	}

	// safety: a sweep with nothing served must not write, which is what
	// keeps metering off the per-response write path.
	s.flushEgressUsage(ctx)
	s.egress.Record("alice", egress.ClassArtifact, 100)
	s.flushEgressUsage(ctx)
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
	s.flushEgressUsage(ctx)
	state, problems := s.egressHealth()
	if state["enabled"] != false || len(problems) != 0 {
		t.Fatalf("health without a meter = %+v, %v; want disabled and no problem", state, problems)
	}
	if s.EgressMeter() != nil {
		t.Error("a controller given no meter reports one")
	}
}
