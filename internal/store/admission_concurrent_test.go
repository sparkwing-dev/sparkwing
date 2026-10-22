package store_test

import (
	"fmt"
	"maps"
	"math/rand"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestConcurrency_PropertyConcurrentOpsHoldInvariants(t *testing.T) {
	if testing.Short() {
		t.Skip("concurrent admission simulation")
	}
	runConcurrentAdmissionSuite(t, storetest.NewSQLite)
}

func TestConcurrency_PropertyConcurrentOpsHoldInvariants_Postgres(t *testing.T) {
	runConcurrentAdmissionSuite(t, storetest.NewPostgres)
}

func runConcurrentAdmissionSuite(t *testing.T, target func(*testing.T) *storetest.Target) {
	for _, seed := range fuzzSeeds(t) {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			capacity := 1 + int(uint64(seed)%3)
			f := newStoreRecoveryFixtureAt(t, false, target(t), func(doc *nodeproto.PlanDoc) {
				for _, key := range []string{"first", "second"} {
					doc.Jobs[0].Concurrency = append(doc.Jobs[0].Concurrency, nodeproto.ConcurrencySpec{Group: key, Scope: new("global"), Capacity: new(capacity), Cost: new(1), OnLimit: new("queue")})
				}
			})
			type contender struct {
				binding       store.CapacityBinding
				actor         store.Actor
				started, done bool
			}
			work := make([]contender, 8)
			work[0].binding = store.CapacityBinding{RunID: string(f.ref.RunID), NodeID: string(f.ref.NodeID), Generation: int64(f.holder.Generation)}
			work[0].actor = f.holder
			for i := 1; i < len(work); i++ {
				work[i].binding, work[i].actor = admissionPolicyBlocker(t, f)
			}
			advance := func(w *contender) error {
				if w.done {
					return nil
				}
				ref := controllerwire.NodeRef{RunID: runspec.RunID(w.binding.RunID), NodeID: runspec.NodeID(w.binding.NodeID)}
				if !w.started {
					result, err := f.tenant.MayStart(t.Context(), w.binding, w.actor)
					if err != nil {
						return fmt.Errorf("admit %s: %w", ref.RunID, err)
					}
					if result.Kind == store.AcquireQueued {
						return nil
					}
					if result.Kind != store.AcquireGranted {
						return fmt.Errorf("admit %s: unexpected %s", ref.RunID, result.Kind)
					}
					_, err = f.tenant.TransitionNode(t.Context(), store.StartNode{Ref: ref, Actor: w.actor, SourceAttempt: acceptanceAttempt(0)})
					if err != nil {
						return fmt.Errorf("start %s: %w", ref.RunID, err)
					}
					w.started = true
					return nil
				}
				_, err := f.tenant.TransitionNode(t.Context(), store.ReportNode{Ref: ref, Actor: w.actor, Outcome: store.NodeSuccess, SourceAttempt: acceptanceAttempt(1)})
				if err != nil {
					return fmt.Errorf("settle %s: %w", ref.RunID, err)
				}
				w.done = true
				return nil
			}
			rng := rand.New(rand.NewSource(seed))
			for _, i := range rng.Perm(len(work)) {
				if result, err := f.tenant.MayStart(t.Context(), work[i].binding, work[i].actor); err != nil || result.Kind != store.AcquireGranted && result.Kind != store.AcquireQueued {
					t.Fatalf("initial admission: %+v, %v", result, err)
				}
			}
			now, err := f.s.TestOnlyDatabaseTime(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			// Raw per-key fixture acquisition bypasses the complete-set lock owner.
			// Race real award admission and settlement with whole-set reaping instead.
			for round := 0; round < len(work)*8; round++ {
				if round == 0 {
					// One lapsed member must discard its sibling under the same owner.
					query := `UPDATE concurrency_waiters SET lease_expires_at=? WHERE team=? AND key='job:global:first'`
					if _, err := f.s.DB().ExecContext(t.Context(), storetest.Rebind(f.s, query), now.UnixNano(), f.tenant.Team()); err != nil {
						t.Fatal(err)
					}
				}
				var wg sync.WaitGroup
				errors := make(chan error, len(work)+1)
				start := make(chan struct{})
				for _, i := range rng.Perm(len(work)) {
					wg.Go(func() {
						<-start
						if err := advance(&work[i]); err != nil {
							errors <- err
						}
					})
				}
				wg.Go(func() {
					<-start
					if _, err := f.s.AsOperator().MaintainConcurrency(t.Context(), f.lease); err != nil {
						errors <- fmt.Errorf("maintenance: %w", err)
					}
				})
				close(start)
				wg.Wait()
				close(errors)
				for err := range errors {
					t.Error(err)
				}
				if t.Failed() {
					t.FailNow()
				}
				// Advisory try-locks promise no fairness, so give every worker an
				// uncontended turn periodically before judging eventual settlement.
				if round%4 == 3 {
					for _, i := range rng.Perm(len(work)) {
						if err := advance(&work[i]); err != nil {
							t.Fatal(err)
						}
					}
				}
				first, err := f.tenant.GetConcurrencyState(t.Context(), "job:global:first")
				if err != nil {
					t.Fatal(err)
				}
				second, err := f.tenant.GetConcurrencyState(t.Context(), "job:global:second")
				if err != nil {
					t.Fatal(err)
				}
				if first.UsedCost > first.EffectiveCapacity || second.UsedCost > second.EffectiveCapacity || !maps.Equal(admissionGroupMembers(first), admissionGroupMembers(second)) {
					t.Fatalf("round %d: partial or over-capacity group set: %+v / %+v", round, first, second)
				}
				for _, holder := range first.Holders {
					for _, waiter := range first.Waiters {
						if holder.QueueArrivedAt.After(waiter.ArrivedAt) {
							t.Fatalf("round %d: %s bypassed live waiter %s", round, holder.RunID, waiter.RunID)
						}
					}
				}
				done := 0
				for _, w := range work {
					if w.done {
						done++
					}
				}
				if done == len(work) {
					if len(first.Holders)+len(first.Waiters) != 0 {
						t.Fatal("settled work retained reservations")
					}
					return
				}
			}
			t.Fatal("admitted work failed to settle")
		})
	}
}

func admissionGroupMembers(state *store.ConcurrencyState) map[string]int64 {
	members := map[string]int64{}
	for _, h := range state.Holders {
		members["holder/"+h.HolderID] = h.QueueArrivedAt.UnixNano()
	}
	for _, w := range state.Waiters {
		members["waiter/"+w.HolderID] = w.ArrivedAt.UnixNano()
	}
	return members
}
