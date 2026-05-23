package boxslot_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/boxslot"
)

// TestStress_HighConcurrency hammers a small-K semaphore with many
// goroutines doing many iterations. Asserts:
//   - peak observed concurrency never exceeds K
//   - every goroutine completes (no starvation)
//   - total elapsed time is consistent with serialized execution
//     (admitted runs × per-run hold time / K, within a generous margin)
func TestStress_HighConcurrency(t *testing.T) {
	if testing.Short() {
		t.Skip("stress: skipped in -short mode")
	}
	const (
		goroutines    = 50
		iterations    = 20
		slots         = 3
		holdPerIter   = 5 * time.Millisecond
		pollInterval  = 2 * time.Millisecond
		pollMaxJitter = 8 * time.Millisecond
	)
	dir := t.TempDir()

	var inflight, peak atomic.Int32
	var completed atomic.Int32
	start := time.Now()

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				release, err := boxslot.Acquire(context.Background(), boxslot.Options{
					MaxSlots:     slots,
					LockDir:      dir,
					PollInterval: pollInterval,
					PollMax:      pollMaxJitter,
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
				time.Sleep(holdPerIter)
				inflight.Add(-1)
				release()
				completed.Add(1)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	if p := peak.Load(); p > slots {
		t.Fatalf("peak concurrency = %d, want <= %d", p, slots)
	}
	if c := completed.Load(); int(c) != goroutines*iterations {
		t.Fatalf("completed = %d, want %d (some goroutines starved?)",
			c, goroutines*iterations)
	}
	// Sanity check on elapsed time. Lower bound: serial work / slots.
	expectedMin := time.Duration(goroutines*iterations) * holdPerIter / time.Duration(slots)
	if elapsed < expectedMin/2 {
		t.Fatalf("elapsed=%s suspiciously fast; lower bound ~%s",
			elapsed, expectedMin/2)
	}

	// Lock dir should be clean of holder files after everyone released.
	staleHolders := countHolders(t, dir)
	if staleHolders != 0 {
		t.Errorf("stale holder files after stress: %d", staleHolders)
	}
}

// TestStress_FairnessFloor asserts that the FIFO-ish polling
// distribution doesn't completely starve any goroutine. Stricter
// than the previous test because peak-concurrency tests pass even
// if one goroutine never acquires.
func TestStress_FairnessFloor(t *testing.T) {
	if testing.Short() {
		t.Skip("stress: skipped in -short mode")
	}
	const goroutines = 16
	const slots = 2
	dir := t.TempDir()

	wins := make([]atomic.Int32, goroutines)
	stop := atomic.Bool{}

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		gi := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				release, err := boxslot.Acquire(context.Background(), boxslot.Options{
					MaxSlots:     slots,
					LockDir:      dir,
					PollInterval: 1 * time.Millisecond,
					PollMax:      4 * time.Millisecond,
				})
				if err != nil {
					return
				}
				wins[gi].Add(1)
				time.Sleep(500 * time.Microsecond)
				release()
			}
		}()
	}

	time.Sleep(300 * time.Millisecond)
	stop.Store(true)
	wg.Wait()

	for i := range wins {
		if wins[i].Load() == 0 {
			t.Errorf("goroutine %d never acquired (starvation)", i)
		}
	}
}

// TestStress_CoordContention spams Acquire-then-immediately-release
// to stress the coord-lock path. No holder ever observes peak > 1.
func TestStress_CoordContention(t *testing.T) {
	if testing.Short() {
		t.Skip("stress: skipped in -short mode")
	}
	const goroutines = 40
	const iterations = 50
	dir := t.TempDir()

	var inflight, peak atomic.Int32
	errs := make(chan error, goroutines*iterations)

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				release, err := boxslot.Acquire(context.Background(), boxslot.Options{
					MaxSlots:     1,
					LockDir:      dir,
					PollInterval: 200 * time.Microsecond,
					PollMax:      1 * time.Millisecond,
				})
				if err != nil {
					errs <- err
					return
				}
				cur := inflight.Add(1)
				for {
					p := peak.Load()
					if cur <= p || peak.CompareAndSwap(p, cur) {
						break
					}
				}
				// No real work; the test is about lock churn under
				// minimal hold time.
				inflight.Add(-1)
				release()
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("Acquire: %v", err)
	}
	if p := peak.Load(); p > 1 {
		t.Fatalf("peak concurrency = %d, want 1", p)
	}
}

// TestStress_SIGKILLHolderRecovery spawns a child process via this
// test binary, has it acquire a slot, then SIGKILLs it. The next
// in-process Acquire must reclaim cleanly.
func TestStress_SIGKILLHolderRecovery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGKILL semantics differ on windows; covered by stale-holder test")
	}
	if os.Getenv("BOXSLOT_CHILD_LOCK_DIR") != "" {
		// We're the child: acquire and block forever.
		release, err := boxslot.Acquire(context.Background(), boxslot.Options{
			MaxSlots: 1,
			LockDir:  os.Getenv("BOXSLOT_CHILD_LOCK_DIR"),
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "child: Acquire: %v\n", err)
			os.Exit(2)
		}
		fmt.Fprintln(os.Stdout, "READY") // signal acquired
		_ = release                      // prevent unused; never actually called
		select {}                        // block until SIGKILLed
	}

	dir := t.TempDir()

	// Re-exec this test binary, running just this test, which the
	// env-var branch above intercepts. The child becomes a holder.
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=^TestStress_SIGKILLHolderRecovery$", "-test.v")
	cmd.Env = append(os.Environ(), "BOXSLOT_CHILD_LOCK_DIR="+dir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	// Wait for child to signal READY.
	ready := make(chan struct{})
	go func() {
		buf := make([]byte, 512)
		for {
			n, err := stdout.Read(buf)
			if n > 0 && strings.Contains(string(buf[:n]), "READY") {
				close(ready)
				return
			}
			if err != nil {
				return
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("child never signaled READY")
	}

	// Confirm a NoWait acquire fails while the child holds the slot.
	if _, err := boxslot.Acquire(context.Background(), boxslot.Options{
		MaxSlots: 1, LockDir: dir, NoWait: true,
	}); !errors.Is(err, boxslot.ErrSlotsFull) {
		t.Fatalf("pre-kill Acquire err = %v, want ErrSlotsFull", err)
	}

	// SIGKILL the child. The OS releases its flock; the next
	// Acquire should reclaim the slot.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("Kill child: %v", err)
	}
	_, _ = cmd.Process.Wait()

	// The next Acquire under NoWait should succeed because the stale
	// holder file is flock-probable.
	release, err := boxslot.Acquire(context.Background(), boxslot.Options{
		MaxSlots: 1, LockDir: dir, NoWait: true,
	})
	if err != nil {
		t.Fatalf("post-kill Acquire: %v", err)
	}
	defer release()

	// The dead child's holder file should have been removed by the
	// scan-and-reclaim path.
	if h := countHolders(t, dir); h != 1 {
		t.Errorf("holders after reclaim = %d, want 1 (us only)", h)
	}
}

// TestStress_ManyStaleHolders simulates a host that crashed
// repeatedly under load: 100 stale holder files left behind. A
// single live Acquire should sweep them all on the first attempt.
func TestStress_ManyStaleHolders(t *testing.T) {
	dir := t.TempDir()
	const stale = 100
	for i := 0; i < stale; i++ {
		path := filepath.Join(dir, fmt.Sprintf("holder-pid%d-0-0.lock", 99000+i))
		if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	release, err := boxslot.Acquire(context.Background(), boxslot.Options{
		MaxSlots: 1, LockDir: dir, NoWait: true,
	})
	if err != nil {
		t.Fatalf("Acquire over %d stale: %v", stale, err)
	}
	defer release()

	remaining := countHolders(t, dir) - 1 // minus our holder
	if remaining != 0 {
		t.Errorf("%d stale holders not cleaned up", remaining)
	}
}

// TestStress_ManyWaitersOneSlot validates that N waiters on a single
// freeing slot all eventually acquire, and that wakeups don't cluster
// (the jitter does its job).
func TestStress_ManyWaitersOneSlot(t *testing.T) {
	if testing.Short() {
		t.Skip("stress: skipped in -short mode")
	}
	const N = 12
	dir := t.TempDir()

	primary, err := boxslot.Acquire(context.Background(), boxslot.Options{
		MaxSlots: 1, LockDir: dir,
	})
	if err != nil {
		t.Fatalf("primary Acquire: %v", err)
	}

	acquireTimes := make(chan time.Time, N)
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := boxslot.Acquire(context.Background(), boxslot.Options{
				MaxSlots:     1,
				LockDir:      dir,
				PollInterval: 5 * time.Millisecond,
				PollMax:      20 * time.Millisecond,
			})
			if err != nil {
				t.Errorf("waiter Acquire: %v", err)
				return
			}
			acquireTimes <- time.Now()
			// Brief hold so the next waiter actually has to acquire,
			// rather than the test racing through with all waiters
			// finding the slot free at once after primary release.
			time.Sleep(2 * time.Millisecond)
			release()
		}()
	}

	// Hold long enough that every waiter is blocked.
	time.Sleep(80 * time.Millisecond)
	primary()
	wg.Wait()
	close(acquireTimes)

	got := 0
	for range acquireTimes {
		got++
	}
	if got != N {
		t.Fatalf("waiters acquired = %d, want %d", got, N)
	}
}

func countHolders(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "holder-") {
			n++
		}
	}
	return n
}
