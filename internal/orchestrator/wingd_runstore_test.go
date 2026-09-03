package orchestrator

import (
	"context"
	"os"
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

	st, err := runs.Store(true)
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
	again, err := runs.Store(false)
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

	if _, err := runs.Store(true); err == nil {
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
	if _, err := runs.Store(true); err == nil {
		t.Fatal("opening a store path that is a directory succeeded")
	}
	if err := os.Remove(db); err != nil {
		t.Fatalf("free the store path: %v", err)
	}
	createStore(t, home)
	if _, err := runs.Store(false); err == nil {
		t.Fatal("a background pass reopened the store before the retry interval")
	}
	runs.now = func() time.Time { return time.Now().Add(HeldRunStoreRetry) }
	if _, err := runs.Store(false); err != nil {
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
	st, err := runs.Store(true)
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
	st, err := seed.Store(true)
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
