package boxslot_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

	// After release, the holder file should be gone so the next
	// acquirer sees a clean directory.
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
			OnWait:       func(active int) { waitCount.Add(1) },
		})
		if err != nil {
			t.Errorf("second Acquire: %v", err)
			close(acquired)
			return
		}
		release()
		close(acquired)
	}()

	// Let the waiter accumulate at least one OnWait callback so we
	// know it actually polled rather than racing past the busy slot.
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
	// Simulate a crashed holder by creating a holder-*.lock file
	// nobody is flocking. The next acquirer should reclaim the slot
	// (count it as inactive, unlink it, take its own slot).
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
	// (N+1)th should fail under NoWait.
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
	// Stress: M goroutines try to acquire and release rapidly under
	// MaxSlots=K. Peak observed concurrency must never exceed K.
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
	cases := []struct {
		name    string
		workers int
		want    int // expected = max(1, NumCPU / max(1, workers))
	}{
		{"zero workers treated as one", 0, max1(boxslot.DefaultMaxSlots(1))},
		{"workers above NumCPU clamps to 1", 1 << 20, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := boxslot.DefaultMaxSlots(c.workers)
			if got != c.want {
				t.Errorf("DefaultMaxSlots(%d) = %d, want %d",
					c.workers, got, c.want)
			}
		})
	}
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}
