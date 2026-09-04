package orchestrator

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func seedLapsedHolder(t *testing.T, st *store.Store, key, holderID, runID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key: key, HolderID: holderID, RunID: runID, NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); err != nil {
		t.Fatalf("acquire slot: %v", err)
	}
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE concurrency_holders SET lease_expires_at = ? WHERE key = ? AND holder_id = ?`,
		time.Now().Add(-time.Minute).UnixNano(), key, holderID); err != nil {
		t.Fatalf("expire holder lease: %v", err)
	}
}

func holdsSlot(t *testing.T, st *store.Store, key, holderID string) bool {
	t.Helper()
	var n int
	if err := st.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM concurrency_holders WHERE key = ? AND holder_id = ?`,
		key, holderID).Scan(&n); err != nil {
		t.Fatalf("count holders: %v", err)
	}
	return n > 0
}

func createStore(t *testing.T, home string) {
	t.Helper()
	st, err := store.Open(PathsAt(home).StateDB())
	if err != nil {
		t.Fatalf("create the store: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close the store: %v", err)
	}
}

func TestHeldRunStoreLeavesAnAbsentStoreAlone(t *testing.T) {
	home := t.TempDir()
	runs, err := NewHeldRunStore(home)
	if err != nil {
		t.Fatalf("new held run store: %v", err)
	}
	t.Cleanup(func() { _ = runs.Close() })

	terminal, err := runs.IsRunTerminal("r1")
	if terminal || err != nil {
		t.Fatalf("terminal check with no store = (%v, %v), want (false, nil)", terminal, err)
	}
	runs.FinalizeRun("r1")
	if err := runs.FinalizeCancelledRuns([]string{"r1"}, "cancelled in a test"); err != nil {
		t.Fatalf("finalize cancelled with no store: %v", err)
	}
	if _, err := os.Stat(PathsAt(home).StateDB()); !os.IsNotExist(err) {
		t.Fatal("the daemon created a runs store; a run against an object store keeps no local state")
	}
	if runs.Ready() == nil {
		t.Fatal("Ready reported an absent store as ready")
	}
}

func TestHeldRunStoreServesEveryCallFromOneHandle(t *testing.T) {
	home := t.TempDir()
	createStore(t, home)
	runs, err := NewHeldRunStore(home)
	if err != nil {
		t.Fatalf("new held run store: %v", err)
	}
	t.Cleanup(func() { _ = runs.Close() })

	st, err := runs.Store(context.Background(), true)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	terminal, err := runs.IsRunTerminal("r1")
	if err != nil || terminal {
		t.Fatalf("terminal check on a running run = (%v, %v), want (false, nil)", terminal, err)
	}
	if err := runs.FinalizeCancelledRuns([]string{"r1"}, "cancelled in a test"); err != nil {
		t.Fatalf("finalize cancelled: %v", err)
	}
	terminal, err = runs.IsRunTerminal("r1")
	if err != nil || !terminal {
		t.Fatalf("terminal check on a cancelled run = (%v, %v), want (true, nil)", terminal, err)
	}
	again, err := runs.Store(context.Background(), false)
	if err != nil {
		t.Fatalf("second store call: %v", err)
	}
	if again != st {
		t.Fatal("the daemon reopened the store instead of holding one handle")
	}
	if err := runs.Ready(); err != nil {
		t.Fatalf("Ready = %v, want nil", err)
	}
}

func TestHeldRunStoreReportsAnUnreadableStoreAndRecovers(t *testing.T) {
	home := t.TempDir()
	db := PathsAt(home).StateDB()
	if err := os.Mkdir(db, 0o700); err != nil {
		t.Fatalf("occupy the store path: %v", err)
	}
	runs, err := NewHeldRunStore(home)
	if err != nil {
		t.Fatalf("new held run store: %v", err)
	}
	t.Cleanup(func() { _ = runs.Close() })

	if _, err := runs.Store(context.Background(), true); err == nil {
		t.Fatal("opening a store path that is a directory succeeded")
	}
	if runs.Ready() == nil {
		t.Fatal("Ready reported an unusable store as ready")
	}
	if _, err := runs.IsRunTerminal("r1"); err == nil {
		t.Fatal("the terminal check hid the store failure instead of reporting it")
	}

	if err := os.Remove(db); err != nil {
		t.Fatalf("free the store path: %v", err)
	}
	createStore(t, home)
	if _, err := runs.IsRunTerminal("r1"); err != nil {
		t.Fatalf("the next call after a failure did not retry the open: %v", err)
	}
	if err := runs.Ready(); err != nil {
		t.Fatalf("Ready after recovery = %v, want nil", err)
	}
}

func TestHeldRunStoreRetriesAFailedOpenOnlyOnItsTimer(t *testing.T) {
	home := t.TempDir()
	db := PathsAt(home).StateDB()
	if err := os.Mkdir(db, 0o700); err != nil {
		t.Fatalf("occupy the store path: %v", err)
	}
	runs, err := NewHeldRunStore(home)
	if err != nil {
		t.Fatalf("new held run store: %v", err)
	}
	t.Cleanup(func() { _ = runs.Close() })
	if _, err := runs.Store(context.Background(), true); err == nil {
		t.Fatal("opening a store path that is a directory succeeded")
	}
	if err := os.Remove(db); err != nil {
		t.Fatalf("free the store path: %v", err)
	}
	createStore(t, home)
	if _, err := runs.Store(context.Background(), false); err == nil {
		t.Fatal("a background pass reopened the store before the retry interval")
	}
	runs.now = func() time.Time { return time.Now().Add(HeldRunStoreRetry) }
	if _, err := runs.Store(context.Background(), false); err != nil {
		t.Fatalf("a background pass past the retry interval did not reopen: %v", err)
	}
}

func TestRunStoreMaintenanceReapsALapsedHolder(t *testing.T) {
	home := t.TempDir()
	createStore(t, home)
	runs, err := NewHeldRunStore(home)
	if err != nil {
		t.Fatalf("new held run store: %v", err)
	}
	t.Cleanup(func() { _ = runs.Close() })
	st, err := runs.Store(context.Background(), true)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	seedLapsedHolder(t, st, "box:deploy", "killed/n", "killed")

	ready := make(chan struct{})
	close(ready)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		maintainRunStore(ctx, runs, ready, func(string, ...any) {})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for holdsSlot(t, st, "box:deploy", "killed/n") {
		if time.Now().After(deadline) {
			t.Fatal("the maintenance loop left a lapsed holder in the store")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-stopped
}

func TestWingdDaemonReapsWhileServingAndClosesTheStoreOnIdleExit(t *testing.T) {
	home := wingdTestHome(t)
	createStore(t, home)
	seed, err := NewHeldRunStore(home)
	if err != nil {
		t.Fatalf("new held run store: %v", err)
	}
	st, err := seed.Store(context.Background(), true)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	seedLapsedHolder(t, st, "box:deploy", "killed/n", "killed")
	if err := seed.Close(); err != nil {
		t.Fatalf("close the seed handle: %v", err)
	}

	var held *HeldRunStore
	err = runWingdDaemon(context.Background(), WingdOptions{Home: home, Version: "test"},
		func(cfg *wingd.Config, runs *HeldRunStore) {
			held = runs
			cfg.IdleTimeout = 400 * time.Millisecond
			cfg.GraceWindow = -1
			cfg.HeadroomFraction = -1
			cfg.Sampler = stubSampler{wingd.HostStat{
				TotalCores: 8, TotalMemoryBytes: 8 << 30, FreeMemoryBytes: 8 << 30,
				LoadMeasured: true, MemoryMeasured: true,
			}}
		})
	if err != nil {
		t.Fatalf("run daemon: %v", err)
	}
	if held.Ready() == nil {
		t.Fatal("idle exit left the runs-store handle open")
	}

	after, err := store.Open(PathsAt(home).StateDB())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = after.Close() }()
	if holdsSlot(t, after, "box:deploy", "killed/n") {
		t.Fatal("the daemon served a whole lifetime without reaping a lapsed holder")
	}
}

func TestHeldRunStoreReadsWhileTheReaperWaitsOnAForeignWriter(t *testing.T) {
	home := t.TempDir()
	createStore(t, home)
	runs, err := NewHeldRunStore(home)
	if err != nil {
		t.Fatalf("new held run store: %v", err)
	}
	t.Cleanup(func() { _ = runs.Close() })
	rw, err := runs.Store(context.Background(), true)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	if err := rw.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	foreign, err := store.Open(PathsAt(home).StateDB())
	if err != nil {
		t.Fatalf("foreign open: %v", err)
	}
	defer func() { _ = foreign.Close() }()
	tx, err := foreign.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("foreign begin: %v", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO sparkwing_meta (key, value, updated_at) VALUES ('held-store-probe', '1', ?)`,
		time.Now().UnixNano()); err != nil {
		_ = tx.Rollback()
		t.Fatalf("foreign write: %v", err)
	}
	rolled := false
	defer func() {
		if !rolled {
			_ = tx.Rollback()
		}
	}()

	reaped := make(chan struct{})
	go func() {
		defer close(reaped)
		_, _ = rw.MaintainConcurrency(ctx, store.ConcurrencyMaintenanceOptions{})
	}()
	time.Sleep(250 * time.Millisecond)

	answered := make(chan error, 1)
	go func() {
		_, err := runs.IsRunTerminal("r1")
		answered <- err
	}()
	select {
	case err := <-answered:
		if err != nil {
			t.Fatalf("terminal check while the reaper waits on a foreign writer: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the terminal check queued behind the reaper; one contended store stalls admission for the box")
	}
	select {
	case <-reaped:
		t.Fatal("the reaper pass finished, so the terminal check never had to pass a busy writing handle")
	default:
	}

	rolled = true
	if err := tx.Rollback(); err != nil {
		t.Fatalf("foreign rollback: %v", err)
	}
	select {
	case <-reaped:
	case <-time.After(30 * time.Second):
		t.Fatal("the reaper pass never finished after the foreign writer released")
	}
}

func TestHeldRunStoreFollowsAReplacedStoreFile(t *testing.T) {
	home := t.TempDir()
	createStore(t, home)
	runs, err := NewHeldRunStore(home)
	if err != nil {
		t.Fatalf("new held run store: %v", err)
	}
	t.Cleanup(func() { _ = runs.Close() })
	if _, err := runs.Store(context.Background(), true); err != nil {
		t.Fatalf("open: %v", err)
	}

	db := PathsAt(home).StateDB()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(db + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove %s: %v", db+suffix, err)
		}
	}
	replacement, err := store.Open(db)
	if err != nil {
		t.Fatalf("replacement open: %v", err)
	}
	ctx := context.Background()
	if err := replacement.CreateRun(ctx, store.Run{ID: "r2", Pipeline: "p", Status: "success", StartedAt: time.Now()}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatalf("close the replacement: %v", err)
	}

	terminal, err := runs.IsRunTerminal("r2")
	if err != nil {
		t.Fatalf("terminal check after the store was replaced: %v", err)
	}
	if !terminal {
		t.Fatal("the daemon answered from the replaced store file instead of the one on disk")
	}
	if err := runs.Ready(); err != nil {
		t.Fatalf("Ready after following the replacement = %v, want nil", err)
	}
}

func TestHeldRunStoreOpensAStoreThatAppearsWithoutWaitingOutTheRetry(t *testing.T) {
	home := t.TempDir()
	runs, err := NewHeldRunStore(home)
	if err != nil {
		t.Fatalf("new held run store: %v", err)
	}
	t.Cleanup(func() { _ = runs.Close() })
	if runs.Ready() == nil {
		t.Fatal("Ready reported an absent store as ready")
	}
	createStore(t, home)
	if err := runs.Ready(); err != nil {
		t.Fatalf("Ready one call after the store appeared = %v, want nil", err)
	}
}

func blockTheStoreOpen(t *testing.T, home string) *sql.Tx {
	t.Helper()
	t.Setenv(store.BusyTimeoutEnvVar, "2000")
	ctx := context.Background()
	foreign, err := store.Open(PathsAt(home).StateDB())
	if err != nil {
		t.Fatalf("foreign open: %v", err)
	}
	t.Cleanup(func() { _ = foreign.Close() })
	if err := foreign.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "p", Status: "success", StartedAt: time.Now()}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	// safety: the next open only migrates, and so only takes the write lock
	// the foreign transaction holds, when the applied versions are gone.
	if _, err := foreign.DB().ExecContext(ctx, `DELETE FROM sparkwing_schema_version`); err != nil {
		t.Fatalf("clear the applied schema versions: %v", err)
	}
	if _, err := foreign.DB().ExecContext(ctx, `DELETE FROM sparkwing_requirements WHERE name IN (
        'executor-enrollment-v1', 'executor-offer-arbitration-v1', 'agent-loss-attempt-fencing-v1'
    )`); err != nil {
		t.Fatalf("clear future Fleet requirements: %v", err)
	}
	tx, err := foreign.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("foreign begin: %v", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO sparkwing_meta (key, value, updated_at) VALUES ('open-block-probe', '1', ?)`,
		time.Now().UnixNano()); err != nil {
		_ = tx.Rollback()
		t.Fatalf("foreign write: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

func TestHeldRunStoreBoundsAnOpenThatCannotMigrate(t *testing.T) {
	home := t.TempDir()
	createStore(t, home)
	tx := blockTheStoreOpen(t, home)

	runs, err := NewHeldRunStore(home)
	if err != nil {
		t.Fatalf("new held run store: %v", err)
	}
	t.Cleanup(func() { _ = runs.Close() })

	start := time.Now()
	if err := runs.Ready(); err == nil {
		t.Fatal("Ready reported a store whose open is blocked as ready")
	}
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("Ready waited %s on a blocked open, want at most its %s budget", waited, readyOpenBudget)
	}

	answers := make(chan time.Duration, 2)
	for range 2 {
		go func() {
			began := time.Now()
			_, err := runs.IsRunTerminal("r1")
			if err == nil {
				t.Error("the terminal check answered from a store that never opened")
			} else if !strings.Contains(err.Error(), "still opening") {
				t.Errorf("terminal check error = %v, want the pending open named", err)
			}
			answers <- time.Since(began)
		}()
	}
	for range 2 {
		select {
		case waited := <-answers:
			if waited > terminalCheckTimeout+2*time.Second {
				t.Fatalf("the terminal check waited %s on a blocked open, want its %s deadline",
					waited, terminalCheckTimeout)
			}
		case <-time.After(3 * terminalCheckTimeout):
			t.Fatal("a terminal check stacked behind the blocked open instead of honoring its deadline")
		}
	}

	if err := tx.Rollback(); err != nil {
		t.Fatalf("foreign rollback: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if err := runs.Ready(); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the store never opened after the writer released: %v", runs.Ready())
		}
		time.Sleep(50 * time.Millisecond)
	}
	terminal, err := runs.IsRunTerminal("r1")
	if err != nil || !terminal {
		t.Fatalf("terminal check after the open completed = (%v, %v), want (true, nil)", terminal, err)
	}
}

func TestFinalizeGivesUpInsideTheDrainWindow(t *testing.T) {
	home := t.TempDir()
	createStore(t, home)
	runs, err := NewHeldRunStore(home)
	if err != nil {
		t.Fatalf("new held run store: %v", err)
	}
	t.Cleanup(func() { _ = runs.Close() })
	ctx := context.Background()
	rw, err := runs.Store(ctx, true)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := rw.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	busy, err := rw.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("occupy the writing connection: %v", err)
	}
	defer func() { _ = busy.Rollback() }()

	start := time.Now()
	err = runs.finalizeRun("r1", "cancelled in a test")
	waited := time.Since(start)
	if err == nil {
		t.Fatal("finalize claimed to write through an occupied connection")
	}
	if waited >= wingd.FinalizeDrainWindow {
		t.Fatalf("finalize took %s, which outlasts the %s shutdown drain: the run is lost to a closed handle",
			waited, wingd.FinalizeDrainWindow)
	}
	if !strings.Contains(err.Error(), FinalizeTimeout.String()) {
		t.Fatalf("finalize error = %v, want the %s deadline named", err, FinalizeTimeout)
	}
}
