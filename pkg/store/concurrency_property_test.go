package store_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A granted arrival must not leave its own stale waiter row behind: a
// queued participant whose slot freed without a promotion (release
// without notify) re-acquires, is granted, and the parked row from the
// first attempt has to vanish -- otherwise a later promote pass tries
// to promote it on top of its own live holder and aborts an unrelated
// release.
func TestConcurrency_GrantClearsOwnStaleWaiterRow(t *testing.T) {
	s := newStoreT(t)
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "r1/n", RunID: "r1", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "r2/n", RunID: "r2", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("r2: want Queued, got %s", r.Kind)
	}
	if _, err := s.ReleaseConcurrencySlot(ctxT(t), "k", "r1/n", "success", "", "", 0); err != nil {
		t.Fatalf("release r1: %v", err)
	}
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "r2/n", RunID: "r2", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireGranted {
		t.Fatalf("r2 re-acquire: want Granted, got %s", r.Kind)
	}
	st, err := s.GetConcurrencyState(ctxT(t), "k")
	if err != nil {
		t.Fatalf("GetConcurrencyState: %v", err)
	}
	if len(st.Waiters) != 0 {
		t.Fatalf("stale waiter row survived the grant: %+v", st.Waiters)
	}
	if _, _, _, err := s.ReleaseAndNotify(ctxT(t), "k", "r2/n", "success", "", "", 0, 0); err != nil {
		t.Fatalf("release r2 with notify: %v", err)
	}
}

func fuzzSeeds(t *testing.T) []int64 {
	t.Helper()
	if env := os.Getenv("SPARKWING_CONC_FUZZ_SEED"); env != "" {
		seed, err := strconv.ParseInt(env, 10, 64)
		if err != nil {
			t.Fatalf("SPARKWING_CONC_FUZZ_SEED=%q: %v", env, err)
		}
		return []int64{seed}
	}
	if testing.Short() {
		return []int64{1}
	}
	return []int64{1, 2, 3, 4, 5}
}

func fuzzOps(full int) int {
	if testing.Short() {
		return full / 4
	}
	return full
}

// concurrencyFuzzOp drives one random store operation. The store's own
// transaction-boundary invariant checks run in fail-fast mode under go
// test, so any sequence that violates an invariant surfaces as an error
// from the op that broke it. The mix covers node-level and plan-level
// (empty NodeID) participants, capacity drift between arrivals, instant
// lease expiry, every release outcome, both reapers, the cache sweeps,
// and the startup reconcile pass.
func concurrencyFuzzOp(ctx context.Context, s *store.Store, rng *rand.Rand) error {
	keys := []string{"ka", "kb", "kc"}
	runs := []string{"r0", "r1", "r2", "r3", "r4"}
	nodes := []string{"n0", "n1", ""}
	policies := []string{
		store.OnLimitQueue, store.OnLimitQueue, store.OnLimitQueue,
		store.OnLimitCoalesce, store.OnLimitSkip, store.OnLimitFail,
		store.OnLimitCancelOthers,
	}

	key := keys[rng.Intn(len(keys))]
	run := runs[rng.Intn(len(runs))]
	node := nodes[rng.Intn(len(nodes))]
	holderID := run + "/" + node
	if node == "" {
		holderID = run + "/-"
	}

	switch rng.Intn(12) {
	case 0, 1, 2, 3:
		req := store.AcquireSlotRequest{
			Key:      key,
			HolderID: holderID,
			RunID:    run,
			NodeID:   node,
			Capacity: 1 + rng.Intn(5),
			Cost:     1 + rng.Intn(4),
			Policy:   policies[rng.Intn(len(policies))],
		}
		if req.Policy == store.OnLimitCoalesce {
			req.CacheKeyHash = "h-" + key
			req.CacheTTL = time.Hour
			req.BypassRead = rng.Intn(4) == 0
		}
		if req.Policy == store.OnLimitCancelOthers && rng.Intn(2) == 0 {
			req.CancelTimeout = time.Minute
		}
		if rng.Intn(5) == 0 {
			req.Lease = time.Nanosecond
		}
		if _, err := s.AcquireConcurrencySlot(ctx, req); err != nil {
			return fmt.Errorf("acquire %+v: %w", req, err)
		}
	case 4:
		if _, _, err := s.HeartbeatConcurrencySlot(ctx, key, holderID, 0); err != nil && !errors.Is(err, store.ErrLockHeld) {
			return fmt.Errorf("heartbeat %s/%s: %w", key, holderID, err)
		}
	case 5:
		hash := ""
		ttl := time.Duration(0)
		if rng.Intn(2) == 0 {
			hash = "h-" + key
			ttl = time.Hour
		}
		outcomes := []string{"success", "failed", "skipped", "superseded"}
		if _, _, _, err := s.ReleaseAndNotify(ctx, key, holderID, outcomes[rng.Intn(len(outcomes))], holderID, hash, ttl, 0); err != nil {
			return fmt.Errorf("release %s/%s: %w", key, holderID, err)
		}
	case 6:
		hash := ""
		if rng.Intn(2) == 0 {
			hash = "h-" + key
		}
		if _, err := s.ResolveWaiter(ctx, key, run, node, hash, "", "", rng.Intn(4) == 0); err != nil {
			return fmt.Errorf("resolve %s %s/%s: %w", key, run, node, err)
		}
	case 7:
		if _, err := s.CancelWaiter(ctx, key, run, node); err != nil {
			return fmt.Errorf("cancel %s %s/%s: %w", key, run, node, err)
		}
	case 8:
		if rng.Intn(2) == 0 {
			if _, err := s.PromoteNextWaiters(ctx, key, 0); err != nil {
				return fmt.Errorf("promote %s: %w", key, err)
			}
		} else {
			if _, err := s.ForceReleaseSupersededHolders(ctx, key); err != nil {
				return fmt.Errorf("force-release %s: %w", key, err)
			}
		}
	case 9:
		if _, err := store.Maintenance.ReapStaleConcurrencyHolders(s, ctx); err != nil {
			return fmt.Errorf("reap holders: %w", err)
		}
	case 10:
		maxAge := time.Hour
		if rng.Intn(3) == 0 {
			maxAge = time.Nanosecond
		}
		if _, err := store.Maintenance.ReapStaleConcurrencyWaiters(s, ctx, maxAge); err != nil {
			return fmt.Errorf("reap waiters: %w", err)
		}
	case 11:
		switch rng.Intn(3) {
		case 0:
			if _, err := store.Maintenance.SweepExpiredConcurrencyCache(s, ctx); err != nil {
				return fmt.Errorf("sweep expired cache: %w", err)
			}
		case 1:
			if _, err := store.Maintenance.SweepLRUConcurrencyCache(s, ctx, 1); err != nil {
				return fmt.Errorf("sweep lru cache: %w", err)
			}
		default:
			if _, err := store.Maintenance.ReconcileConcurrencyKeys(s, ctx, 0); err != nil {
				return fmt.Errorf("reconcile: %w", err)
			}
		}
	}

	st, err := s.GetConcurrencyState(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("state %s: %w", key, err)
	}
	if st.UsedCost > st.EffectiveCapacity {
		return fmt.Errorf("state %s: used cost %d exceeds effective capacity %d", key, st.UsedCost, st.EffectiveCapacity)
	}
	return nil
}

func runSequentialPropertySuite(t *testing.T, newStore func(*testing.T) *store.Store) {
	for _, seed := range fuzzSeeds(t) {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			s := newStore(t)
			rng := rand.New(rand.NewSource(seed))
			for i := 0; i < fuzzOps(600); i++ {
				if err := concurrencyFuzzOp(ctxT(t), s, rng); err != nil {
					t.Fatalf("seed %d op %d: %v", seed, i, err)
				}
			}
		})
	}
}

func runConcurrentPropertySuite(t *testing.T, newStore func(*testing.T) *store.Store) {
	s := newStore(t)
	ctx := ctxT(t)
	const goroutines = 8
	errCh := make(chan error, goroutines)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		seed := int64(100 + g)
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed))
			for i := 0; i < fuzzOps(150); i++ {
				if err := concurrencyFuzzOp(ctx, s, rng); err != nil {
					errCh <- fmt.Errorf("goroutine seed %d op %d: %w", seed, i, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

func TestConcurrency_PropertyRandomOpsHoldInvariants(t *testing.T) {
	runSequentialPropertySuite(t, newStoreT)
}

func TestConcurrency_PropertyConcurrentOpsHoldInvariants(t *testing.T) {
	runConcurrentPropertySuite(t, newStoreT)
}

func TestConcurrency_PropertyRandomOpsHoldInvariants_Postgres(t *testing.T) {
	runSequentialPropertySuite(t, openPGTestStore)
}

func TestConcurrency_PropertyConcurrentOpsHoldInvariants_Postgres(t *testing.T) {
	runConcurrentPropertySuite(t, openPGTestStore)
}

// Promotion among queue-policy waiters must follow arrival order
// exactly, across a random mix of grants, queues, and releases.
func TestConcurrency_PropertyFIFOPromotionOrder(t *testing.T) {
	runFIFOPropertySuite(t, newStoreT)
}

func TestConcurrency_PropertyFIFOPromotionOrder_Postgres(t *testing.T) {
	runFIFOPropertySuite(t, openPGTestStore)
}

func runFIFOPropertySuite(t *testing.T, newStore func(*testing.T) *store.Store) {
	for _, seed := range fuzzSeeds(t) {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			s := newStore(t)
			rng := rand.New(rand.NewSource(seed))
			const key = "fifo"
			var queue []string
			holders := map[string]bool{}
			next := 0
			for i := 0; i < 200; i++ {
				if rng.Intn(2) == 0 || len(holders) == 0 {
					run := fmt.Sprintf("r%03d", next)
					next++
					resp := acquireT(t, s, store.AcquireSlotRequest{
						Key: key, HolderID: run + "/n", RunID: run, NodeID: "n",
						Capacity: 2, Cost: 1, Policy: store.OnLimitQueue,
					})
					switch resp.Kind {
					case store.AcquireGranted:
						holders[run] = true
					case store.AcquireQueued:
						queue = append(queue, run)
					default:
						t.Fatalf("seed %d op %d: unexpected kind %s", seed, i, resp.Kind)
					}
				} else {
					var victim string
					pick := rng.Intn(len(holders))
					for run := range holders {
						if pick == 0 {
							victim = run
							break
						}
						pick--
					}
					delete(holders, victim)
					_, _, promoted, err := s.ReleaseAndNotify(ctxT(t), key, victim+"/n", "success", "", "", 0, 0)
					if err != nil {
						t.Fatalf("seed %d op %d: release %s: %v", seed, i, victim, err)
					}
					for _, w := range promoted {
						if len(queue) == 0 || queue[0] != w.RunID {
							t.Fatalf("seed %d op %d: promoted %s out of FIFO order (queue head %v)", seed, i, w.RunID, queue)
						}
						queue = queue[1:]
						holders[w.RunID] = true
					}
				}
			}
		})
	}
}
