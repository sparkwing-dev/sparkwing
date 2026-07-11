package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// doctorHome prepares an isolated sparkwing home with an initialized
// state database and returns its paths. home doubles as the daemon home;
// no daemon runs under a fresh temp dir, so daemon queries report empty.
func doctorHome(t *testing.T) paths.Paths {
	t.Helper()
	dir := t.TempDir()
	p := paths.PathsAt(dir)
	if err := p.EnsureRoot(); err != nil {
		t.Fatalf("EnsureRoot: %v", err)
	}
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	_ = st.Close()
	return p
}

func withStore(t *testing.T, p paths.Paths, fn func(st *store.Store)) {
	t.Helper()
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()
	fn(st)
}

// backdateHeartbeat rewinds a run's heartbeat so it reads as a process
// that died, since CreateRun stamps a fresh heartbeat on running rows.
func backdateHeartbeat(t *testing.T, st *store.Store, runID string, age time.Duration) {
	t.Helper()
	if _, err := st.DB().Exec(
		`UPDATE runs SET last_heartbeat_at = ? WHERE id = ?`,
		time.Now().Add(-age).UnixNano(), runID); err != nil {
		t.Fatalf("backdate heartbeat: %v", err)
	}
}

func TestDiagnose_CleanHomeFindsNothing(t *testing.T) {
	p := doctorHome(t)
	rep, err := diagnose(context.Background(), p, p.Root, false)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if !rep.clean() {
		t.Fatalf("clean home not reported clean: %+v", rep)
	}
}

func TestDiagnose_FinalizesOrphanedRunKeepsRecent(t *testing.T) {
	p := doctorHome(t)
	ctx := context.Background()
	withStore(t, p, func(st *store.Store) {
		if err := st.CreateRun(ctx, store.Run{
			ID: "run-orphan", Pipeline: "demo", Status: "running",
			StartedAt: time.Now().Add(-10 * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
		backdateHeartbeat(t, st, "run-orphan", 10*time.Minute)
		if err := st.CreateRun(ctx, store.Run{
			ID: "run-fresh", Pipeline: "demo", Status: "running",
			StartedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	})

	rep, err := diagnose(ctx, p, p.Root, false)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if len(rep.OrphanedRuns) != 1 || rep.OrphanedRuns[0] != "run-orphan" {
		t.Fatalf("OrphanedRuns = %v, want [run-orphan]", rep.OrphanedRuns)
	}
	withStore(t, p, func(st *store.Store) {
		orphan, _ := st.GetRun(ctx, "run-orphan")
		if orphan == nil || orphan.Status != "cancelled" {
			t.Fatalf("orphan run status = %v, want cancelled", orphan)
		}
		fresh, _ := st.GetRun(ctx, "run-fresh")
		if fresh == nil || fresh.Status != "running" {
			t.Fatalf("fresh run status = %v, want running (protected by grace)", fresh)
		}
	})
}

func TestDiagnose_RemovesDeadLocalConcurrencyRows(t *testing.T) {
	p := doctorHome(t)
	ctx := context.Background()
	withStore(t, p, func(st *store.Store) {
		if err := st.CreateRun(ctx, store.Run{ID: "run-dead", Pipeline: "demo", Status: "failed", StartedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.DB().ExecContext(ctx,
			`INSERT INTO concurrency_holders (key, holder_id, run_id, claimed_at, lease_expires_at)
			 VALUES ('r:run-dead:build','run-dead:n','run-dead',?,?)`,
			time.Now().UnixNano(), time.Now().Add(time.Hour).UnixNano()); err != nil {
			t.Fatal(err)
		}
	})

	rep, err := diagnose(ctx, p, p.Root, false)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if rep.DeadConcurrencyHolders != 1 {
		t.Fatalf("DeadConcurrencyHolders = %d, want 1", rep.DeadConcurrencyHolders)
	}
}

func TestDiagnose_RemovesDanglingRunDirKeepsKnown(t *testing.T) {
	p := doctorHome(t)
	ctx := context.Background()
	withStore(t, p, func(st *store.Store) {
		if err := st.CreateRun(ctx, store.Run{ID: "run-known", Pipeline: "demo", Status: "success", StartedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	})
	if err := os.MkdirAll(filepath.Join(p.RunsDir(), "run-known"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(p.RunsDir(), "run-ghost"), 0o755); err != nil {
		t.Fatal(err)
	}

	rep, err := diagnose(ctx, p, p.Root, false)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if len(rep.DanglingRunDirs) != 1 || rep.DanglingRunDirs[0] != "run-ghost" {
		t.Fatalf("DanglingRunDirs = %v, want [run-ghost]", rep.DanglingRunDirs)
	}
	if _, err := os.Stat(filepath.Join(p.RunsDir(), "run-ghost")); !os.IsNotExist(err) {
		t.Fatalf("dangling dir survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.RunsDir(), "run-known")); err != nil {
		t.Fatalf("known run dir removed: %v", err)
	}
}

func TestDiagnose_SecondRunIsClean(t *testing.T) {
	p := doctorHome(t)
	ctx := context.Background()
	withStore(t, p, func(st *store.Store) {
		if err := st.CreateRun(ctx, store.Run{
			ID: "run-orphan", Pipeline: "demo", Status: "running",
			StartedAt: time.Now().Add(-10 * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := diagnose(ctx, p, p.Root, false); err != nil {
		t.Fatalf("first diagnose: %v", err)
	}
	rep, err := diagnose(ctx, p, p.Root, false)
	if err != nil {
		t.Fatalf("second diagnose: %v", err)
	}
	if !rep.clean() {
		t.Fatalf("second run not clean: %+v", rep)
	}
}

func TestDiagnose_DryRunChangesNothing(t *testing.T) {
	p := doctorHome(t)
	ctx := context.Background()
	withStore(t, p, func(st *store.Store) {
		if err := st.CreateRun(ctx, store.Run{
			ID: "run-orphan", Pipeline: "demo", Status: "running",
			StartedAt: time.Now().Add(-10 * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
		backdateHeartbeat(t, st, "run-orphan", 10*time.Minute)
	})
	rep, err := diagnose(ctx, p, p.Root, true)
	if err != nil {
		t.Fatalf("diagnose dry-run: %v", err)
	}
	if len(rep.OrphanedRuns) != 1 {
		t.Fatalf("dry-run OrphanedRuns = %v, want one candidate reported", rep.OrphanedRuns)
	}
	withStore(t, p, func(st *store.Store) {
		orphan, _ := st.GetRun(ctx, "run-orphan")
		if orphan == nil || orphan.Status != "running" {
			t.Fatalf("dry-run changed the run: status = %v, want running", orphan)
		}
	})
}
