package store_test

import (
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func admissionGroup(capacity, cost int) nodeproto.ConcurrencySpec {
	return nodeproto.ConcurrencySpec{Group: "key", Scope: new("global"), Capacity: new(capacity), Cost: new(cost), OnLimit: new("queue")}
}

func admissionFixture(t *testing.T, target *storetest.Target, capacity, cost int) *storeRecoveryFixture {
	t.Helper()
	return newStoreRecoveryFixtureAt(t, false, target, func(doc *nodeproto.PlanDoc) {
		doc.Jobs[0].Concurrency = []nodeproto.ConcurrencySpec{admissionGroup(capacity, cost)}
	})
}

func admissionBinding(f *storeRecoveryFixture) store.CapacityBinding {
	return store.CapacityBinding{RunID: string(f.ref.RunID), NodeID: string(f.ref.NodeID), Generation: int64(f.holder.Generation)}
}

func admissionWant(t *testing.T, f *storeRecoveryFixture, binding store.CapacityBinding, actor store.Actor, want store.AcquireKind) store.AcquireSlotResponse {
	t.Helper()
	got, err := f.tenant.MayStart(t.Context(), binding, actor)
	if err != nil || got.Kind != want {
		t.Fatalf("MayStart(%s/%s): %+v %v, want %s", binding.RunID, binding.NodeID, got, err, want)
	}
	return got
}

func admissionFinishJob(t *testing.T, f *storeRecoveryFixture, binding store.CapacityBinding, actor store.Actor) {
	t.Helper()
	ref := controllerwire.NodeRef{RunID: runspec.RunID(binding.RunID), NodeID: runspec.NodeID(binding.NodeID)}
	if _, err := f.tenant.TransitionNode(t.Context(), store.StartNode{Ref: ref, Actor: actor, SourceAttempt: acceptanceAttempt(0)}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tenant.TransitionNode(t.Context(), store.ReportNode{Ref: ref, Actor: actor, Outcome: store.NodeSuccess, SourceAttempt: acceptanceAttempt(1)}); err != nil {
		t.Fatal(err)
	}
}

func admissionState(t *testing.T, f *storeRecoveryFixture) *store.ConcurrencyState {
	t.Helper()
	state, err := f.tenant.GetConcurrencyState(t.Context(), "job:global:key")
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func admissionExpiry(t *testing.T, f *storeRecoveryFixture, table, run string, until time.Time) {
	t.Helper()
	col := "run_id"
	if table == "triggers" {
		col = "id"
	}
	if _, err := f.s.DB().ExecContext(t.Context(), storetest.Rebind(f.s, "UPDATE "+table+" SET lease_expires_at=? WHERE team=? AND "+col+"=?"), until.UnixNano(), f.tenant.Team(), run); err != nil {
		t.Fatal(err)
	}
}

func TestAdmissionClaimOrOwnerLossRetainsLiveFIFO(t *testing.T) {
	for name, target := range map[string]func(*testing.T) *storetest.Target{"sqlite": storetest.NewSQLite, "postgres": storetest.NewPostgres} {
		t.Run(name, func(t *testing.T) {
			for _, loss := range []string{"claim", "owner", "both"} {
				t.Run(loss, func(t *testing.T) {
					f := admissionFixture(t, target(t), 1, 1)
					leader, leaderAward := admissionPolicyBlocker(t, f)
					admissionWant(t, f, leader, leaderAward, store.AcquireGranted)
					oldest, oldAward := admissionBinding(f), f.holder
					admissionWant(t, f, oldest, oldAward, store.AcquireQueued)
					before := admissionState(t, f).Waiters[0]
					later, laterAward := admissionPolicyBlocker(t, f)
					admissionWant(t, f, later, laterAward, store.AcquireQueued)
					now, err := f.s.TestOnlyDatabaseTime(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					if loss != "owner" {
						admissionExpiry(t, f, "nodes", oldest.RunID, now)
					}
					if loss != "claim" {
						admissionExpiry(t, f, "triggers", oldest.RunID, now)
					}
					admissionFinishJob(t, f, leader, leaderAward)
					want := store.AcquireQueued
					if loss == "both" {
						want = store.AcquireGranted
					}
					admissionWant(t, f, later, laterAward, want)
					state := admissionState(t, f)
					if loss == "both" {
						if len(state.Waiters) != 0 || len(state.Holders) != 1 || state.Holders[0].RunID != later.RunID {
							t.Fatalf("dead head retained capacity: %+v", state)
						}
						return
					}
					if len(state.Waiters) != 2 || state.Waiters[0].RunID != oldest.RunID || !state.Waiters[0].ArrivedAt.Equal(before.ArrivedAt) {
						t.Fatalf("live head lost FIFO: %+v", state)
					}
					if loss == "claim" {
						if reaped, err := f.s.AsOperator().ReapUnits(t.Context(), f.lease, 10); err != nil || len(reaped) != 1 || !reaped[0].Requeued {
							t.Fatalf("reap: %+v %v", reaped, err)
						}
						next, err := f.tenant.ReconcileNode(t.Context(), store.ReconcileRequest{Ref: f.ref, Actor: f.owner, Policy: store.SourceResume})
						if err != nil || next.Claim == nil {
							t.Fatalf("reaward: %+v %v", next, err)
						}
						c := next.Claim
						oldAward = store.Actor{Kind: store.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.CapabilityPrefix}, CapabilityPrefix: c.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: f.ref.RunID, NodeID: f.ref.NodeID, Holder: c.Holder, Generation: c.Generation}}
						oldest.Generation = int64(c.Generation)
					}
					admissionWant(t, f, oldest, oldAward, store.AcquireGranted)
					state = admissionState(t, f)
					if len(state.Holders) != 1 || !state.Holders[0].QueueArrivedAt.Equal(before.ArrivedAt) || len(state.Waiters) != 1 {
						t.Fatalf("promotion changed arrival or left waiter: %+v", state)
					}
					admissionFinishJob(t, f, oldest, oldAward)
					admissionWant(t, f, later, laterAward, store.AcquireGranted)

				})
			}
		})
	}
}

func TestAdmissionWeightedCapacityAndBackfill(t *testing.T) {
	type action struct {
		index  int
		finish bool
		want   store.AcquireKind
	}
	for name, target := range map[string]func(*testing.T) *storetest.Target{"sqlite": storetest.NewSQLite, "postgres": storetest.NewPostgres} {
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name           string
				capacity, cost int
				declarations   [][2]int
				actions        []action
			}{
				{"weighted", 8, 4, [][2]int{{8, 4}, {8, 4}}, []action{{0, false, store.AcquireGranted}, {1, false, store.AcquireGranted}, {2, false, store.AcquireQueued}, {0, true, ""}, {2, false, store.AcquireGranted}}},
				{"backfill", 8, 6, [][2]int{{8, 6}, {8, 2}}, []action{{0, false, store.AcquireGranted}, {1, false, store.AcquireQueued}, {2, false, store.AcquireGranted}, {0, true, ""}, {1, false, store.AcquireGranted}}},
				{"promotion_backfill", 8, 6, [][2]int{{8, 2}, {8, 6}, {8, 2}}, []action{{0, false, store.AcquireGranted}, {1, false, store.AcquireGranted}, {2, false, store.AcquireQueued}, {3, false, store.AcquireQueued}, {1, true, ""}, {3, false, store.AcquireGranted}, {2, false, store.AcquireQueued}, {0, true, ""}, {2, false, store.AcquireGranted}}},
				{"bounded_backfill", 10, 5, [][2]int{{10, 8}, {10, 5}, {10, 5}}, []action{{0, false, store.AcquireGranted}, {1, false, store.AcquireQueued}, {2, false, store.AcquireGranted}, {3, false, store.AcquireQueued}, {0, true, ""}, {3, false, store.AcquireQueued}, {2, true, ""}, {1, false, store.AcquireGranted}}},
				{"minimum_participant_capacity", 5, 1, [][2]int{{2, 1}, {5, 1}}, []action{{0, false, store.AcquireGranted}, {1, false, store.AcquireGranted}, {2, false, store.AcquireQueued}, {1, true, ""}, {2, false, store.AcquireGranted}}},
				{"parked_shrink", 4, 4, [][2]int{{4, 2}, {1, 1}}, []action{{0, false, store.AcquireGranted}, {1, false, store.AcquireQueued}, {2, false, store.AcquireQueued}, {0, true, ""}, {1, false, store.AcquireGranted}, {2, false, store.AcquireQueued}, {1, true, ""}, {2, false, store.AcquireGranted}}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					f := admissionFixture(t, target(t), tc.capacity, tc.cost)
					bindings := []store.CapacityBinding{admissionBinding(f)}
					actors := []store.Actor{f.holder}
					for _, d := range tc.declarations {
						b, a := admissionPolicyBlocker(t, f, func(doc *nodeproto.PlanDoc) {
							doc.Jobs[0].Concurrency = []nodeproto.ConcurrencySpec{admissionGroup(d[0], d[1])}
						})
						bindings = append(bindings, b)
						actors = append(actors, a)
					}
					for _, action := range tc.actions {
						if action.finish {
							admissionFinishJob(t, f, bindings[action.index], actors[action.index])
						} else {
							admissionWant(t, f, bindings[action.index], actors[action.index], action.want)
						}
						state := admissionState(t, f)
						if state.UsedCost > state.EffectiveCapacity {
							t.Fatalf("capacity inverted: %+v", state)
						}
						for _, h := range state.Holders {
							if h.Cost < 1 || h.DeclaredCapacity < 1 {
								t.Fatalf("lost weighted declaration: %+v", h)
							}
						}
					}
				})
			}
		})
	}
}

func TestAdmissionExpiredWaiterReturnsBehindLiveArrival(t *testing.T) {
	f := admissionFixture(t, storetest.NewSQLite(t), 1, 1)
	leader, leaderAward := admissionPolicyBlocker(t, f)
	admissionWant(t, f, leader, leaderAward, store.AcquireGranted)
	returning := admissionBinding(f)
	admissionWant(t, f, returning, f.holder, store.AcquireQueued)
	old := admissionState(t, f).Waiters[0].ArrivedAt
	earlier, earlierAward := admissionPolicyBlocker(t, f)
	admissionWant(t, f, earlier, earlierAward, store.AcquireQueued)
	now, err := f.s.TestOnlyDatabaseTime(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"concurrency_waiters", "admission_work"} {
		admissionExpiry(t, f, table, returning.RunID, now)
	}
	admissionWant(t, f, returning, f.holder, store.AcquireQueued)
	state := admissionState(t, f)
	if len(state.Waiters) != 2 || state.Waiters[0].RunID != earlier.RunID || !state.Waiters[1].ArrivedAt.After(old) {
		t.Fatalf("expired waiter retained place: %+v", state)
	}
	admissionFinishJob(t, f, leader, leaderAward)
	admissionWant(t, f, returning, f.holder, store.AcquireQueued)
	admissionWant(t, f, earlier, earlierAward, store.AcquireGranted)
	admissionFinishJob(t, f, earlier, earlierAward)
	admissionWant(t, f, returning, f.holder, store.AcquireGranted)
}

func TestAdmissionFIFOIsIndependentOfClockRegression(t *testing.T) {
	f := admissionFixture(t, storetest.NewSQLite(t), 1, 1)
	leader, leaderAward := admissionPolicyBlocker(t, f)
	later, award := admissionPolicyBlocker(t, f)
	admissionWant(t, f, leader, leaderAward, store.AcquireGranted)
	now, err := f.s.TestOnlyDatabaseTime(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	original := now
	now = now.Add(time.Second)
	f.s.SetTestClock(t, func() time.Time { return now })
	oldest := admissionBinding(f)
	admissionWant(t, f, oldest, f.holder, store.AcquireQueued)
	before := admissionState(t, f).Waiters[0]
	now = original
	for range 3 {
		admissionWant(t, f, oldest, f.holder, store.AcquireQueued)
	}
	admissionWant(t, f, later, award, store.AcquireQueued)
	state := admissionState(t, f)
	if len(state.Waiters) != 2 || state.Waiters[0].RunID != oldest.RunID || !state.Waiters[0].ArrivedAt.Equal(before.ArrivedAt) || !state.Waiters[1].ArrivedAt.After(before.ArrivedAt) {
		t.Fatalf("clock regression reordered queue: %+v", state)
	}
}

func TestAdmissionExpiredCapacityCannotReviveOnClaimRenewal(t *testing.T) {
	f := admissionFixture(t, storetest.NewSQLite(t), 1, 1)
	binding := admissionBinding(f)
	admitted := admissionWant(t, f, binding, f.holder, store.AcquireGranted)
	now, err := f.s.TestOnlyDatabaseTime(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"concurrency_holders", "admission_work"} {
		admissionExpiry(t, f, table, binding.RunID, now)
	}
	if _, err := f.tenant.RenewNodeClaim(t.Context(), f.ref, f.holder); err != nil {
		t.Fatal(err)
	}
	var expiry int64
	if err := f.s.DB().QueryRowContext(t.Context(), storetest.Rebind(f.s, "SELECT lease_expires_at FROM concurrency_holders WHERE team=? AND run_id=? AND node_id=?"), f.tenant.Team(), binding.RunID, binding.NodeID).Scan(&expiry); err != nil || expiry != now.UnixNano() {
		t.Fatalf("renewal revived expired capacity: %d %v", expiry, err)
	}
	competitor, award := admissionPolicyBlocker(t, f)
	admissionWant(t, f, competitor, award, store.AcquireGranted)
	admissionWant(t, f, binding, f.holder, store.AcquireQueued)
	state := admissionState(t, f)
	if len(state.Holders) != 1 || state.Holders[0].HolderID == admitted.HolderID || len(state.Waiters) != 1 {
		t.Fatalf("expired owner duplicated capacity: %+v", state)
	}
	admissionFinishJob(t, f, competitor, award)
	admissionWant(t, f, binding, f.holder, store.AcquireGranted)
}

func TestAdmissionPlanWaitingHandoffPreservesFIFO(t *testing.T) {
	for name, target := range map[string]func(*testing.T) *storetest.Target{"sqlite": storetest.NewSQLite, "postgres": storetest.NewPostgres} {
		t.Run(name, func(t *testing.T) {
			f := newStoreRecoveryFixtureAt(t, false, target(t), func(doc *nodeproto.PlanDoc) { doc.Concurrency = []nodeproto.ConcurrencySpec{admissionGroup(1, 1)} })
			now, err := f.s.TestOnlyDatabaseTime(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			// The released job keeps its award while plan continuity lapses.
			if _, err := f.s.DB().ExecContext(t.Context(), storetest.Rebind(f.s, "UPDATE concurrency_holders SET lease_expires_at=? WHERE team=? AND run_id=? AND node_id=''"), now.UnixNano(), f.tenant.Team(), f.ref.RunID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.DB().ExecContext(t.Context(), storetest.Rebind(f.s, "UPDATE admission_work SET lease_expires_at=? WHERE team=? AND run_id=? AND node_id=''"), now.UnixNano(), f.tenant.Team(), f.ref.RunID); err != nil {
				t.Fatal(err)
			}
			blocker, blockOwner := admissionAcceptedPlan(t, f)
			blockBinding := store.CapacityBinding{RunID: blocker.RunID, Generation: int64(blockOwner.Owner.Generation)}
			admissionWant(t, f, blockBinding, blockOwner, store.AcquireGranted)
			own := store.CapacityBinding{RunID: string(f.ref.RunID), Generation: int64(f.owner.Owner.Generation)}
			admissionWant(t, f, own, f.owner, store.AcquireQueued)
			before, err := f.tenant.GetConcurrencyState(t.Context(), "plan:global:key")
			if err != nil || len(before.Waiters) != 1 {
				t.Fatalf("plan waiters: %+v %v", before, err)
			}
			later, laterOwner := admissionAcceptedPlan(t, f)
			laterBinding := store.CapacityBinding{RunID: later.RunID, Generation: int64(laterOwner.Owner.Generation)}
			admissionWant(t, f, laterBinding, laterOwner, store.AcquireQueued)
			admissionExpiry(t, f, "triggers", own.RunID, now)
			if reaped, err := f.s.AsOperator().ReapUnits(t.Context(), f.lease, 10); err != nil || len(reaped) != 1 || !reaped[0].Requeued {
				t.Fatalf("owner reaping: %+v %v", reaped, err)
			}
			successor, err := f.claimOwner(t.Context(), own.RunID, "successor")
			if err != nil {
				t.Fatal(err)
			}
			own.Generation = int64(successor.Owner.Generation)
			admissionWant(t, f, own, successor, store.AcquireQueued)
			if _, err := f.tenant.TransitionRun(t.Context(), store.RunTransition{RunID: runspec.RunID(blocker.RunID), To: store.RunCancelled, Cause: store.RunCancel, Actor: store.Actor{Kind: store.ActorOperator, Authority: f.plan.Seal.Request.AuthorityRef}, Reason: "user_cancelled"}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.tenant.FinishRunAndUnit(t.Context(), store.FinishRunRequest{Unit: blockOwner.Owner, Transition: store.RunCompletion{RunID: runspec.RunID(blocker.RunID), Actor: blockOwner, Cause: store.RunFinish}}); err != nil {
				t.Fatal(err)
			}
			admissionWant(t, f, laterBinding, laterOwner, store.AcquireQueued)
			admissionWant(t, f, own, successor, store.AcquireGranted)
			after, err := f.tenant.GetConcurrencyState(t.Context(), "plan:global:key")
			if err != nil || len(after.Holders) != 1 || after.Holders[0].HolderID != own.RunID+"/-" || !after.Holders[0].QueueArrivedAt.Equal(before.Waiters[0].ArrivedAt) || len(after.Waiters) != 1 {
				t.Fatalf("handoff replaced plan reservation: %+v %v", after, err)
			}
		})
	}
}

func TestAdmissionTenantScopesRemainIndependent(t *testing.T) {
	target := storetest.NewSQLite(t)
	edit := func(doc *nodeproto.PlanDoc) {
		doc.Jobs[0].Concurrency = []nodeproto.ConcurrencySpec{admissionGroup(1, 1)}
	}
	a := newStoreRecoveryFixtureForAt(t, false, target, "alpha", edit)
	b := newStoreRecoveryFixtureForAt(t, false, target, "beta", edit)
	for _, f := range []*storeRecoveryFixture{a, b} {
		admissionWant(t, f, admissionBinding(f), f.holder, store.AcquireGranted)
	}
	for _, pair := range [][2]*storeRecoveryFixture{{a, b}, {b, a}} {
		if _, err := pair[0].tenant.MayStart(t.Context(), admissionBinding(pair[1]), pair[1].holder); err == nil {
			t.Fatal("foreign tenant award admitted")
		}
	}
	before := admissionState(t, b)
	admissionFinishJob(t, a, admissionBinding(a), a.holder)
	if len(admissionState(t, a).Holders) != 0 {
		t.Fatal("terminal job retained capacity")
	}
	after := admissionState(t, b)
	if len(after.Holders) != 1 || after.Holders[0].HolderID != before.Holders[0].HolderID || after.UsedCost != 1 {
		t.Fatalf("other team's settlement changed reservation: %+v -> %+v", before, after)
	}
}

func TestAdmissionAcrossSQLiteConnectionsSerializes(t *testing.T) {
	for _, policy := range []string{"queue", "fail"} {
		t.Run(policy, func(t *testing.T) {
			f := newStoreRecoveryFixtureAt(t, false, storetest.NewSQLite(t), func(doc *nodeproto.PlanDoc) {
				group := admissionGroup(1, 1)
				group.OnLimit = new(policy)
				doc.Jobs[0].Concurrency = []nodeproto.ConcurrencySpec{group}
			})
			bindings := []store.CapacityBinding{admissionBinding(f)}
			actors := []store.Actor{f.holder}
			for range 7 {
				b, a := admissionPolicyBlocker(t, f)
				bindings = append(bindings, b)
				actors = append(actors, a)
			}
			tenants := []*store.Tenant{f.tenant}
			for range 3 {
				st, err := f.target.TryOpen(store.WithPolicies(sealStorePolicies))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := st.Close(); err != nil {
						t.Error(err)
					}
				})
				tenant, err := st.ForTeam(t.Context(), f.tenant.Team())
				if err != nil {
					t.Fatal(err)
				}
				tenants = append(tenants, tenant)
			}
			type result struct {
				admission store.AcquireSlotResponse
				err       error
			}
			results := make([]result, len(bindings))
			start := make(chan struct{})
			var ready, syncCalls sync.WaitGroup
			ready.Add(len(bindings))
			syncCalls.Add(len(bindings))
			for i := range bindings {
				go func() {
					defer syncCalls.Done()
					ready.Done()
					<-start
					results[i].admission, results[i].err = tenants[i%len(tenants)].MayStart(t.Context(), bindings[i], actors[i])
				}()
			}
			ready.Wait()
			close(start)
			syncCalls.Wait()
			granted, limited := 0, 0
			want := store.AcquireQueued
			if policy == "fail" {
				want = store.AcquireFailed
			}
			for _, result := range results {
				if result.err != nil {
					t.Fatal(result.err)
				}
				switch result.admission.Kind {
				case store.AcquireGranted:
					granted++
				case want:
					limited++
				default:
					t.Fatalf("unexpected admission: %+v", result)
				}
			}
			state := admissionState(t, f)
			if granted != 1 || limited != len(bindings)-1 || state.UsedCost != 1 || len(state.Holders) != 1 {
				t.Fatalf("independent connections over-admitted: grants=%d limited=%d state=%+v", granted, limited, state)
			}
		})
	}
}

func TestAdmissionCancelledWaiterCannotBlockLiveWork(t *testing.T) {
	f := admissionFixture(t, storetest.NewSQLite(t), 1, 1)
	leader, leaderAward := admissionPolicyBlocker(t, f)
	admissionWant(t, f, leader, leaderAward, store.AcquireGranted)
	admissionWant(t, f, admissionBinding(f), f.holder, store.AcquireQueued)
	later, award := admissionPolicyBlocker(t, f)
	admissionWant(t, f, later, award, store.AcquireQueued)
	if _, err := f.tenant.TransitionRun(t.Context(), store.RunTransition{RunID: f.ref.RunID, To: store.RunCancelled, Cause: store.RunCancel, Actor: store.Actor{Kind: store.ActorOperator, Authority: f.plan.Seal.Request.AuthorityRef}, Reason: "user_cancelled"}); err != nil {
		t.Fatal(err)
	}
	if err := f.expireHolder(t.Context()); err != nil {
		t.Fatal(err)
	}
	if reaped, err := f.s.AsOperator().ReapUnits(t.Context(), f.lease, 10); err != nil || len(reaped) != 1 {
		t.Fatalf("cancel recovery: %+v %v", reaped, err)
	}

	if _, err := f.tenant.FinishRunAndUnit(t.Context(), store.FinishRunRequest{Unit: f.owner.Owner, Transition: store.RunCompletion{RunID: f.ref.RunID, Actor: f.owner, Cause: store.RunFinish}}); err != nil {
		t.Fatal(err)
	}
	admissionFinishJob(t, f, leader, leaderAward)
	admissionWant(t, f, later, award, store.AcquireGranted)
	state := admissionState(t, f)
	if len(state.Waiters) != 0 || len(state.Holders) != 1 || state.Holders[0].RunID != later.RunID {
		t.Fatalf("terminal waiter retained reservation: %+v", state)
	}
	if _, err := f.tenant.MayStart(t.Context(), admissionBinding(f), f.holder); err == nil {
		t.Fatal("terminal waiter reacquired capacity")
	}
}
