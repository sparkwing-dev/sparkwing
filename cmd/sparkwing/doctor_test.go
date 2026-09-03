package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRenderPartialDoctorWritesRepairsBeforeError(t *testing.T) {
	report := doctorReport{PermissionRepairs: []fssecure.Change{{Path: "/private/home/state.db"}}}
	diagnoseErr := errors.New("later repair failed")
	var out bytes.Buffer
	err := renderPartialDoctor(&out, report, "plain", diagnoseErr)
	if !errors.Is(err, diagnoseErr) {
		t.Fatalf("render error = %v, want diagnose error", err)
	}
	if got := out.String(); !strings.Contains(got, "permission_repairs\t1") {
		t.Fatalf("partial report was not rendered before error:\n%s", got)
	}
}

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
	if !rep.Clean() {
		t.Fatalf("clean home not reported clean: %+v", rep)
	}
}

func TestDiagnose_ReportsQuarantinedLedgersWithoutRemoving(t *testing.T) {
	p := doctorHome(t)
	dir := filepath.Join(p.Root, "wingd")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir wingd dir: %v", err)
	}
	quarantined := filepath.Join(dir, "state.json.corrupt-1784666506")
	if err := os.WriteFile(quarantined, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write quarantined ledger: %v", err)
	}

	rep, err := diagnose(context.Background(), p, p.Root, false)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if len(rep.QuarantinedLedgers) != 1 || rep.QuarantinedLedgers[0] != quarantined {
		t.Fatalf("QuarantinedLedgers = %v, want [%s]", rep.QuarantinedLedgers, quarantined)
	}
	if rep.Clean() {
		t.Fatal("report with a quarantined ledger reported clean")
	}
	if _, err := os.Stat(quarantined); err != nil {
		t.Fatalf("doctor removed the quarantined ledger: %v", err)
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
	t.Setenv("SPARKWING_PROFILES", filepath.Join(t.TempDir(), "profiles.yaml"))
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
	ghost := filepath.Join(p.RunsDir(), "run-ghost")
	if err := os.MkdirAll(ghost, 0o755); err != nil {
		t.Fatal(err)
	}
	settled := time.Now().Add(-time.Hour)
	if err := os.Chtimes(ghost, settled, settled); err != nil {
		t.Fatal(err)
	}

	rep, err := diagnose(ctx, p, p.Root, false)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if len(rep.DanglingRunDirs) != 1 || rep.DanglingRunDirs[0] != "run-ghost" {
		t.Fatalf("DanglingRunDirs = %v, want [run-ghost]", rep.DanglingRunDirs)
	}
	if _, err := os.Stat(ghost); !os.IsNotExist(err) {
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
	if !rep.Clean() {
		t.Fatalf("second run not clean: %+v", rep)
	}
}

func TestDiagnose_FlagsPoisonedProfileWithoutRepair(t *testing.T) {
	p := doctorHome(t)
	ctx := context.Background()
	floor := float64(runtime.NumCPU())
	withStore(t, p, func(st *store.Store) {
		if err := st.RecordProfileObservation(ctx, "myrepo/ci", "", store.ProfileObservation{
			CPUMeasured: true, PlanHash: "A", Contended: true, FloorCores: floor,
		}); err != nil {
			t.Fatal(err)
		}
	})

	rep, err := diagnose(ctx, p, p.Root, false)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if len(rep.PoisonedProfiles) != 1 || rep.PoisonedProfiles[0].Pipeline != "myrepo/ci" {
		t.Fatalf("PoisonedProfiles = %+v, want the myrepo/ci rollup flagged", rep.PoisonedProfiles)
	}
	if rep.PoisonedProfiles[0].FloorCores != floor {
		t.Errorf("FloorCores = %v, want %v", rep.PoisonedProfiles[0].FloorCores, floor)
	}
	withStore(t, p, func(st *store.Store) {
		prof, err := st.GetPipelineProfile(ctx, "myrepo/ci", "")
		if err != nil || prof == nil || prof.FloorCores != floor {
			t.Fatalf("profile after diagnose = %+v (err %v), want the learned floor untouched", prof, err)
		}
	})
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
