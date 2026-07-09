package boxslot_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/boxslot"
)

func TestAcquire_DisabledWhenMaxZero(t *testing.T) {
	release, err := boxslot.Acquire(context.Background(), boxslot.Options{
		MaxSlots: 0,
		LockDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if release == nil {
		t.Fatal("release nil")
	}
	release()
}

func TestAcquire_SingleHolderRoundtrip(t *testing.T) {
	dir := t.TempDir()
	release, err := boxslot.Acquire(context.Background(), boxslot.Options{
		MaxSlots: 1,
		LockDir:  dir,
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	release()

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".lock" && e.Name() != "coord.lock" {
			t.Fatalf("stale holder remains after release: %s", e.Name())
		}
	}
}

func TestAcquire_NoWaitFailsWhenFull(t *testing.T) {
	dir := t.TempDir()
	rel1, err := boxslot.Acquire(context.Background(), boxslot.Options{
		MaxSlots: 1,
		LockDir:  dir,
	})
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	defer rel1()

	_, err = boxslot.Acquire(context.Background(), boxslot.Options{
		MaxSlots: 1,
		LockDir:  dir,
		NoWait:   true,
	})
	if !errors.Is(err, boxslot.ErrSlotsFull) {
		t.Fatalf("second Acquire err = %v, want ErrSlotsFull", err)
	}
}

func TestAcquire_BlocksUntilSlotFrees(t *testing.T) {
	dir := t.TempDir()
	rel1, err := boxslot.Acquire(context.Background(), boxslot.Options{
		MaxSlots: 1,
		LockDir:  dir,
	})
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	waitCount := atomic.Int32{}
	acquired := make(chan struct{})
	go func() {
		release, err := boxslot.Acquire(context.Background(), boxslot.Options{
			MaxSlots:     1,
			LockDir:      dir,
			PollInterval: 20 * time.Millisecond,
			PollMax:      40 * time.Millisecond,
			OnWait:       func(active, max int) { waitCount.Add(1) },
		})
		if err != nil {
			t.Errorf("second Acquire: %v", err)
			close(acquired)
			return
		}
		release()
		close(acquired)
	}()

	time.Sleep(80 * time.Millisecond)
	if waitCount.Load() == 0 {
		t.Error("expected at least one OnWait callback while slot busy")
	}

	rel1()
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("second Acquire never returned after first released")
	}
}

func TestAcquire_ContextCancelAbortsWait(t *testing.T) {
	dir := t.TempDir()
	rel1, err := boxslot.Acquire(context.Background(), boxslot.Options{
		MaxSlots: 1,
		LockDir:  dir,
	})
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	defer rel1()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := boxslot.Acquire(ctx, boxslot.Options{
			MaxSlots:     1,
			LockDir:      dir,
			PollInterval: 20 * time.Millisecond,
		})
		done <- err
	}()

	time.Sleep(40 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Acquire err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Acquire did not return after context cancel")
	}
}

func TestAcquire_RecoversFromStaleHolder(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "holder-pid99999-0-0.lock")
	if err := os.WriteFile(stale, []byte("stale"), 0o600); err != nil {
		t.Fatalf("seed stale holder: %v", err)
	}

	release, err := boxslot.Acquire(context.Background(), boxslot.Options{
		MaxSlots: 1,
		LockDir:  dir,
		NoWait:   true,
	})
	if err != nil {
		t.Fatalf("Acquire over stale holder: %v", err)
	}
	defer release()

	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale holder still present: %v", err)
	}
}

func TestAcquire_MultipleSlotsAdmitInParallel(t *testing.T) {
	dir := t.TempDir()
	const N = 3
	releases := make([]func(), 0, N)
	for i := 0; i < N; i++ {
		rel, err := boxslot.Acquire(context.Background(), boxslot.Options{
			MaxSlots: N,
			LockDir:  dir,
			NoWait:   true,
		})
		if err != nil {
			t.Fatalf("Acquire #%d: %v", i, err)
		}
		releases = append(releases, rel)
	}
	if _, err := boxslot.Acquire(context.Background(), boxslot.Options{
		MaxSlots: N,
		LockDir:  dir,
		NoWait:   true,
	}); !errors.Is(err, boxslot.ErrSlotsFull) {
		t.Fatalf("over-cap Acquire err = %v, want ErrSlotsFull", err)
	}
	for _, r := range releases {
		r()
	}
}

func TestAcquire_ConcurrentRaceNeverExceedsMax(t *testing.T) {
	const M = 12
	const K = 3
	dir := t.TempDir()
	var inflight, peak atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < M; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				release, err := boxslot.Acquire(context.Background(), boxslot.Options{
					MaxSlots:     K,
					LockDir:      dir,
					PollInterval: 5 * time.Millisecond,
					PollMax:      15 * time.Millisecond,
				})
				if err != nil {
					t.Errorf("Acquire: %v", err)
					return
				}
				cur := inflight.Add(1)
				for {
					p := peak.Load()
					if cur <= p || peak.CompareAndSwap(p, cur) {
						break
					}
				}
				time.Sleep(10 * time.Millisecond)
				inflight.Add(-1)
				release()
			}
		}()
	}
	wg.Wait()
	if p := peak.Load(); p > K {
		t.Fatalf("peak concurrency = %d, want <= %d", p, K)
	}
}

func TestDefaultMaxSlots(t *testing.T) {
	ncpu := runtime.NumCPU()
	cases := []struct {
		workers int
		want    int
	}{
		{0, ncpu},
		{1, ncpu},
		{ncpu, 1},
		{1 << 20, 1},
	}
	for _, c := range cases {
		if got := boxslot.DefaultMaxSlots(c.workers); got != c.want {
			t.Errorf("DefaultMaxSlots(%d) = %d, want %d", c.workers, got, c.want)
		}
	}
}

func TestAcquire_ResolverRaisesCapUnblocksWaiter(t *testing.T) {
	dir := t.TempDir()
	rel1, err := boxslot.Acquire(context.Background(), boxslot.Options{
		MaxSlots: 1, LockDir: dir,
	})
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	defer rel1()

	var capVal atomic.Int64
	capVal.Store(1)
	acquired := make(chan error, 1)
	go func() {
		rel, err := boxslot.Acquire(context.Background(), boxslot.Options{
			ResolveMaxSlots: func() int { return int(capVal.Load()) },
			LockDir:         dir,
			PollInterval:    10 * time.Millisecond,
			PollMax:         20 * time.Millisecond,
		})
		if err == nil {
			rel()
		}
		acquired <- err
	}()

	select {
	case err := <-acquired:
		t.Fatalf("second Acquire returned early (err=%v) while cap=1 and slot held", err)
	case <-time.After(60 * time.Millisecond):
	}

	capVal.Store(2)
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("second Acquire after live cap raise: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second Acquire never unblocked after the cap was raised in flight")
	}
}

func TestAcquire_ResolverDisableMidWaitAdmits(t *testing.T) {
	dir := t.TempDir()
	rel1, err := boxslot.Acquire(context.Background(), boxslot.Options{MaxSlots: 1, LockDir: dir})
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	defer rel1()

	var capVal atomic.Int64
	capVal.Store(1)
	done := make(chan error, 1)
	go func() {
		rel, err := boxslot.Acquire(context.Background(), boxslot.Options{
			ResolveMaxSlots: func() int { return int(capVal.Load()) },
			LockDir:         dir,
			PollInterval:    10 * time.Millisecond,
			PollMax:         20 * time.Millisecond,
		})
		if err == nil {
			rel()
		}
		done <- err
	}()

	time.Sleep(40 * time.Millisecond)
	capVal.Store(0)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Acquire after live disable: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Acquire did not admit after the semaphore was disabled mid-wait")
	}
}

func TestStatus_ReportsHoldersAndWaiters(t *testing.T) {
	dir := t.TempDir()
	st, err := boxslot.Status(dir)
	if err != nil {
		t.Fatalf("Status empty: %v", err)
	}
	if st.ActiveHolders != 0 || st.Waiters != 0 {
		t.Fatalf("empty Status = %+v, want zero", st)
	}

	rel1, err := boxslot.Acquire(context.Background(), boxslot.Options{MaxSlots: 1, LockDir: dir})
	if err != nil {
		t.Fatalf("Acquire holder: %v", err)
	}
	defer rel1()

	ctx, cancel := context.WithCancel(context.Background())
	waiterDone := make(chan struct{})
	go func() {
		defer close(waiterDone)
		rel, err := boxslot.Acquire(ctx, boxslot.Options{
			MaxSlots:     1,
			LockDir:      dir,
			PollInterval: 10 * time.Millisecond,
			PollMax:      20 * time.Millisecond,
		})
		if err == nil {
			rel()
		}
	}()

	waitFor(t, 2*time.Second, func() bool {
		st, err = boxslot.Status(dir)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		return st.ActiveHolders == 1 && st.Waiters == 1
	}, "Status never reached 1 holder + 1 waiter")

	cancel()
	<-waiterDone

	waitFor(t, 2*time.Second, func() bool {
		st, _ = boxslot.Status(dir)
		return st.Waiters == 0
	}, "waiter marker lingered after the waiter was canceled")
}

func TestControl_WriteReadClearRoundtrip(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := boxslot.ReadControl(dir); err != nil || ok {
		t.Fatalf("ReadControl unset = ok:%v err:%v, want ok:false", ok, err)
	}
	if err := boxslot.WriteControl(dir, "3"); err != nil {
		t.Fatalf("WriteControl: %v", err)
	}
	v, ok, err := boxslot.ReadControl(dir)
	if err != nil || !ok || v != "3" {
		t.Fatalf("ReadControl = %q,%v,%v want 3,true,nil", v, ok, err)
	}
	if err := boxslot.ClearControl(dir); err != nil {
		t.Fatalf("ClearControl: %v", err)
	}
	if _, ok, _ := boxslot.ReadControl(dir); ok {
		t.Fatal("ReadControl after clear still reports set")
	}
	if err := boxslot.ClearControl(dir); err != nil {
		t.Fatalf("ClearControl when absent should be a no-op: %v", err)
	}
}

func TestWriteControl_CreatesLockDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "box-slots")
	if err := boxslot.WriteControl(dir, "off"); err != nil {
		t.Fatalf("WriteControl into a missing dir: %v", err)
	}
	v, ok, _ := boxslot.ReadControl(dir)
	if !ok || v != "off" {
		t.Fatalf("ReadControl = %q,%v want off,true", v, ok)
	}
}

// TestAcquire_GrantsSlotsInArrivalOrder pins the fairness contract: a freed
// slot goes to the longest-waiting run, not whichever waiter's poll fires
// first. Waiters are launched in an observed arrival order, then the held
// slot is released and the queue must drain in that same order.
func TestAcquire_GrantsSlotsInArrivalOrder(t *testing.T) {
	dir := t.TempDir()
	rel0, err := boxslot.Acquire(context.Background(), boxslot.Options{
		MaxSlots: 1,
		LockDir:  dir,
	})
	if err != nil {
		t.Fatalf("hold Acquire: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()

	const N = 8
	var mu sync.Mutex
	grantOrder := make([]int, 0, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rel, err := boxslot.Acquire(ctx, boxslot.Options{
				MaxSlots:     1,
				LockDir:      dir,
				PollInterval: 5 * time.Millisecond,
				PollMax:      10 * time.Millisecond,
			})
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					t.Errorf("waiter %d Acquire: %v", i, err)
				}
				return
			}
			mu.Lock()
			grantOrder = append(grantOrder, i)
			mu.Unlock()
			time.Sleep(5 * time.Millisecond)
			rel()
		}(i)
		want := i + 1
		waitFor(t, 2*time.Second, func() bool {
			st, err := boxslot.Status(dir)
			return err == nil && st.Waiters == want
		}, "waiter did not register before the next arrival")
	}

	rel0()
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(grantOrder) != N {
		t.Fatalf("granted %d slots, want %d", len(grantOrder), N)
	}
	for pos, id := range grantOrder {
		if id != pos {
			t.Fatalf("grant order = %v, want strict arrival order 0..%d", grantOrder, N-1)
		}
	}
}

func TestAcquire_HeadWaiterDrainsPromptlyWhenSlotFrees(t *testing.T) {
	dir := t.TempDir()
	rel0, err := boxslot.Acquire(context.Background(), boxslot.Options{
		MaxSlots: 1,
		LockDir:  dir,
	})
	if err != nil {
		t.Fatalf("hold Acquire: %v", err)
	}

	waiting := make(chan struct{}, 1)
	acquired := make(chan struct{})
	go func() {
		rel, err := boxslot.Acquire(context.Background(), boxslot.Options{
			MaxSlots:     1,
			LockDir:      dir,
			PollInterval: 5 * time.Second,
			PollMax:      5 * time.Second,
			OnWait: func(_, _ int) {
				select {
				case waiting <- struct{}{}:
				default:
				}
			},
		})
		if err != nil {
			t.Errorf("waiter Acquire: %v", err)
			close(acquired)
			return
		}
		rel()
		close(acquired)
	}()

	select {
	case <-waiting:
	case <-time.After(2 * time.Second):
		t.Fatal("waiter did not enter wait path")
	}
	rel0()
	select {
	case <-acquired:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("head waiter did not drain promptly after slot freed")
	}
}

// TestAcquire_MultiSlotAdmitsEarliestArrivalsFirst covers the max>1 path:
// when several slots free together, the earliest arrivals fill them and
// admission never exceeds the cap.
func TestAcquire_MultiSlotAdmitsEarliestArrivalsFirst(t *testing.T) {
	dir := t.TempDir()
	const Slots = 2
	held := make([]func(), 0, Slots)
	for i := 0; i < Slots; i++ {
		rel, err := boxslot.Acquire(context.Background(), boxslot.Options{
			MaxSlots: Slots, LockDir: dir, NoWait: true,
		})
		if err != nil {
			t.Fatalf("fill holder %d: %v", i, err)
		}
		held = append(held, rel)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()

	const N = 4
	var mu sync.Mutex
	grantOrder := make([]int, 0, N)
	var inflight, peak atomic.Int32
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rel, err := boxslot.Acquire(ctx, boxslot.Options{
				MaxSlots:     Slots,
				LockDir:      dir,
				PollInterval: 5 * time.Millisecond,
				PollMax:      10 * time.Millisecond,
			})
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					t.Errorf("waiter %d Acquire: %v", i, err)
				}
				return
			}
			mu.Lock()
			grantOrder = append(grantOrder, i)
			mu.Unlock()
			cur := inflight.Add(1)
			for {
				p := peak.Load()
				if cur <= p || peak.CompareAndSwap(p, cur) {
					break
				}
			}
			time.Sleep(40 * time.Millisecond)
			inflight.Add(-1)
			rel()
		}(i)
		want := i + 1
		waitFor(t, 2*time.Second, func() bool {
			st, err := boxslot.Status(dir)
			return err == nil && st.Waiters == want
		}, "waiter did not register before the next arrival")
	}

	for _, rel := range held {
		rel()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if p := peak.Load(); p != Slots {
		t.Fatalf("peak concurrent admissions = %d, want %d", p, Slots)
	}
	firstBatch := map[int]bool{grantOrder[0]: true, grantOrder[1]: true}
	if !firstBatch[0] || !firstBatch[1] {
		t.Fatalf("grant order = %v, want the two earliest arrivals admitted first", grantOrder)
	}
}

// TestAcquire_StaleWaiterDoesNotBlockQueue asserts an abandoned waiter marker
// with an earlier arrival key is reclaimed rather than holding the line: a
// live waiter queued behind it is still admitted when a slot frees.
func TestAcquire_StaleWaiterDoesNotBlockQueue(t *testing.T) {
	dir := t.TempDir()
	rel0, err := boxslot.Acquire(context.Background(), boxslot.Options{
		MaxSlots: 1, LockDir: dir,
	})
	if err != nil {
		t.Fatalf("hold Acquire: %v", err)
	}

	stale := filepath.Join(dir, "waiter-pid99999-1-1.lock")
	if err := os.WriteFile(stale, []byte("stale"), 0o600); err != nil {
		t.Fatalf("seed stale waiter: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	acquired := make(chan struct{})
	defer func() {
		cancel()
		<-acquired
	}()
	go func() {
		rel, err := boxslot.Acquire(ctx, boxslot.Options{
			MaxSlots:     1,
			LockDir:      dir,
			PollInterval: 5 * time.Millisecond,
			PollMax:      10 * time.Millisecond,
		})
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				t.Errorf("waiter Acquire: %v", err)
			}
			close(acquired)
			return
		}
		rel()
		close(acquired)
	}()

	rel0()
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("live waiter never admitted behind a stale earlier-key marker")
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale waiter marker not reclaimed: %v", err)
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestAcquire_RejectsMissingLockDir asserts the precondition guard
// fires before any filesystem work, so callers get a useful error
// instead of a confusing flock failure.
func TestAcquire_RejectsMissingLockDir(t *testing.T) {
	_, err := boxslot.Acquire(context.Background(), boxslot.Options{
		MaxSlots: 1,
		LockDir:  "",
	})
	if err == nil {
		t.Fatal("expected error for empty LockDir, got nil")
	}
}

// TestAcquire_ReadOnlyLockDirFails covers the "user's $SPARKWING_HOME
// became read-only" case (e.g. a mounted volume went read-only, or
// permissions got clobbered). Should surface a clean error from the
// preparation step rather than crash.
func TestAcquire_ReadOnlyLockDirFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses POSIX file permissions; skipping")
	}
	parent := t.TempDir()
	lockDir := filepath.Join(parent, "box-slots")
	if err := os.Mkdir(lockDir, 0o500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(lockDir, 0o700) })

	_, err := boxslot.Acquire(context.Background(), boxslot.Options{
		MaxSlots: 1, LockDir: lockDir,
	})
	if err == nil {
		t.Fatal("expected error on read-only lock dir, got nil")
	}
}

// TestAcquire_ReleaseIdempotent: calling release more than once is
// safe. The closure must guard against double-unlink / double-close.
func TestAcquire_ReleaseIdempotent(t *testing.T) {
	release, err := boxslot.Acquire(context.Background(), boxslot.Options{
		MaxSlots: 1, LockDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	release()
	release()
	release()
}
