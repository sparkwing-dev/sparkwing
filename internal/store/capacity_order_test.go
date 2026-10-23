package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestReleasedJobAdmissionDoesNotReacquirePlan(t *testing.T) {
	f := newRecoveryCapacityFixture(t)
	ctx := t.Context()
	plan, owner := admissionAcceptedPlan(t, f)
	competitor := plan.RunID
	if _, err := f.s.DB().ExecContext(ctx, storetest.Rebind(f.s, `UPDATE concurrency_holders SET lease_expires_at=1 WHERE team=? AND run_id=? AND node_id=''`), f.tenant.Team(), f.ref.RunID); err != nil {
		t.Fatal(err)
	}
	occupied, err := f.tenant.MayStart(ctx, runstate.CapacityBinding{RunID: competitor, Generation: int64(owner.Owner.Generation)}, owner)
	if err != nil || occupied.Kind != runstate.AcquireGranted {
		t.Fatalf("occupy elapsed plan: %+v, %v", occupied, err)
	}
	result, err := f.tenant.MayStart(ctx, runstate.CapacityBinding{RunID: string(f.ref.RunID), NodeID: string(f.ref.NodeID), Generation: int64(f.holder.Generation)}, f.holder)
	if err != nil || result.Kind != runstate.AcquireGranted {
		t.Fatalf("released job blocked on parent: %+v, %v", result, err)
	}
	for _, key := range []string{"job:global:node", "job:global:deploy"} {
		state, err := f.tenant.GetConcurrencyState(ctx, key)
		if err != nil || len(state.Holders) != 1 || state.Holders[0].RunID != string(f.ref.RunID) {
			t.Fatalf("released job capacity %s: %+v, %v", key, state, err)
		}
	}
	state, err := f.tenant.GetConcurrencyState(ctx, "plan:global:plan")
	if err != nil || state.UsedCost != 1 {
		t.Fatalf("job disturbed parent capacity: %+v, %v", state, err)
	}
	found := false
	for _, holder := range state.Holders {
		found = found || holder.RunID == competitor && holder.LeaseExpiresAt.After(time.Now())
	}
	if !found {
		t.Fatalf("competitor lost parent capacity: %+v", state)
	}
}

func TestAdmissionGroupSetIsAtomicAndUsesOneArrival(t *testing.T) {
	f := newRecoveryCapacityFixture(t)
	ctx := t.Context()
	blocker, award := admissionPolicyBlocker(t, f, func(doc *nodeproto.PlanDoc) {
		doc.Concurrency = nil
		doc.Jobs[0].Concurrency = []nodeproto.ConcurrencySpec{{Group: "node", Scope: new("global"), Capacity: new(1), OnLimit: new("queue")}}
	})
	admissionWant(t, f, blocker, award, runstate.AcquireGranted)
	binding := runstate.CapacityBinding{RunID: string(f.ref.RunID), NodeID: string(f.ref.NodeID), Generation: int64(f.holder.Generation)}
	for range 3 {
		result, err := f.tenant.MayStart(ctx, binding, f.holder)
		if err != nil || result.Kind != runstate.AcquireQueued {
			t.Fatalf("group set admission: %+v %v", result, err)
		}
		var holders, waiters, arrivals int
		if err := f.s.DB().QueryRowContext(ctx, storetest.Rebind(f.s, `SELECT COUNT(*) FROM concurrency_holders WHERE team=? AND run_id=? AND node_id='job'`), f.tenant.Team(), f.ref.RunID).Scan(&holders); err != nil {
			t.Fatal(err)
		}
		if err := f.s.DB().QueryRowContext(ctx, storetest.Rebind(f.s, `SELECT COUNT(*),COUNT(DISTINCT arrived_at) FROM concurrency_waiters WHERE team=? AND run_id=? AND node_id='job'`), f.tenant.Team(), f.ref.RunID).Scan(&waiters, &arrivals); err != nil {
			t.Fatal(err)
		}
		if holders != 0 || waiters != 2 || arrivals != 1 {
			t.Fatalf("partial set: holders=%d waiters=%d arrivals=%d", holders, waiters, arrivals)
		}
		if _, err := f.tenant.TransitionNode(ctx, store.StartNode{Ref: f.ref, Actor: f.holder, SourceAttempt: acceptanceAttempt(0)}); err == nil {
			t.Fatal("queued award started without committed admission")
		}
	}
	admissionFinishJob(t, f, blocker, award)
	for _, key := range []string{"job:global:deploy", "job:global:node"} {
		state, err := f.tenant.GetConcurrencyState(ctx, key)
		if err != nil || len(state.Holders) != 0 {
			t.Fatalf("single-key release promoted a partial set: %s %+v %v", key, state, err)
		}
	}
	if result, err := f.tenant.MayStart(ctx, binding, f.holder); err != nil || result.Kind != runstate.AcquireGranted {
		t.Fatalf("complete set did not progress: %+v %v", result, err)
	}
}

func TestAdmissionQueueTimeoutOnlyAppliesWhileWaiting(t *testing.T) {
	for _, dialect := range []struct {
		name   string
		target func(*testing.T) *storetest.Target
	}{{"sqlite", storetest.NewSQLite}, {"postgres", storetest.NewPostgres}} {
		t.Run(dialect.name, func(t *testing.T) {
			for _, queued := range []bool{false, true} {
				t.Run(map[bool]string{false: "granted", true: "waiting"}[queued], func(t *testing.T) {
					f := newStoreRecoveryFixtureAt(t, false, dialect.target(t), func(doc *nodeproto.PlanDoc) {
						doc.Jobs[0].Concurrency = []nodeproto.ConcurrencySpec{{Group: "deploy", Scope: new("global"), Capacity: new(1), OnLimit: new("queue"), QueueTimeoutMS: new(int64(1000))}}
					})
					now, err := f.s.TestOnlyDatabaseTime(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					f.s.SetTestClock(t, func() time.Time { return now })
					want := runstate.AcquireGranted
					if queued {
						blocker, actor := admissionPolicyBlocker(t, f)
						if result, err := f.tenant.MayStart(t.Context(), blocker, actor); err != nil || result.Kind != runstate.AcquireGranted {
							t.Fatalf("blocker: %+v %v", result, err)
						}
						want = runstate.AcquireQueued
					}
					binding := runstate.CapacityBinding{RunID: string(f.ref.RunID), NodeID: string(f.ref.NodeID), Generation: int64(f.holder.Generation)}
					if result, err := f.tenant.MayStart(t.Context(), binding, f.holder); err != nil || result.Kind != want {
						t.Fatalf("initial admission: %+v %v", result, err)
					}
					now = now.Add(2 * time.Second)
					if queued {
						want = runstate.AcquireFailed
					}
					result, err := f.tenant.MayStart(t.Context(), binding, f.holder)
					if err != nil || result.Kind != want || queued && result.FailureReason != store.FailureQueueTimeout {
						t.Fatalf("admission after queue deadline: %+v %v", result, err)
					}
					if !queued {
						if _, err := f.tenant.TransitionNode(t.Context(), store.StartNode{Ref: f.ref, Actor: f.holder, SourceAttempt: acceptanceAttempt(0)}); err != nil {
							t.Fatalf("retained permit cannot start: %v", err)
						}
					}
				})
			}
		})
	}
}

func TestAdmissionRequiresActualJobAward(t *testing.T) {
	f := newRecoveryCapacityFixture(t)
	binding := runstate.CapacityBinding{RunID: string(f.ref.RunID), NodeID: string(f.ref.NodeID), Generation: int64(f.holder.Generation)}
	if _, err := f.tenant.MayStart(t.Context(), binding, f.owner); err == nil {
		t.Fatal("coordinator acquired executable job groups")
	}
	for _, alter := range []func(*runstate.Actor){
		func(a *runstate.Actor) { a.Holder = "another-host" },
		func(a *runstate.Actor) { a.Generation++ },
		func(a *runstate.Actor) { a.NodeID = "another-job" },
	} {
		actor := f.holder
		alter(&actor)
		if _, err := f.tenant.MayStart(t.Context(), binding, actor); err == nil {
			t.Fatal("forged execution award admitted")
		}
	}
}

func TestClaimedAdmissionRetainsPlanThroughOwnerRecovery(t *testing.T) {
	f := newRecoveryCapacityFixture(t)
	ctx := t.Context()
	before, err := f.tenant.GetConcurrencyState(ctx, "plan:global:plan")
	if err != nil || len(before.Holders) != 1 {
		t.Fatalf("initial plan: %+v, %v", before, err)
	}
	now, err := f.s.TestOnlyDatabaseTime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.DB().ExecContext(ctx, storetest.Rebind(f.s, `UPDATE triggers SET lease_expires_at=? WHERE team=? AND id=?`), now.UnixNano(), f.tenant.Team(), f.ref.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tenant.RenewNodeClaim(ctx, f.ref, f.holder); err != nil {
		t.Fatal(err)
	}
	if units, err := f.s.AsOperator().ReapUnits(ctx, f.lease, 10); err != nil || len(units) != 1 || !units[0].Requeued {
		t.Fatalf("owner recovery: %+v, %v", units, err)
	}
	owner, err := f.claimOwner(ctx, string(f.ref.RunID), "successor")
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.tenant.MayStart(ctx, runstate.CapacityBinding{RunID: string(f.ref.RunID), Generation: int64(owner.Owner.Generation)}, owner)
	if err != nil || result.Kind != runstate.AcquireGranted {
		t.Fatalf("successor admission: %+v, %v", result, err)
	}
	after, err := f.tenant.GetConcurrencyState(ctx, "plan:global:plan")
	if err != nil || len(after.Holders) != 1 || !after.Holders[0].ClaimedAt.Equal(before.Holders[0].ClaimedAt) || after.Holders[0].HolderID != before.Holders[0].HolderID {
		t.Fatalf("handoff changed reservation identity: %+v -> %+v, %v", before, after, err)
	}
}

func TestJobBoxScopeUsesExecutingAward(t *testing.T) {
	f := newStoreRecoveryFixtureAt(t, false, storetest.New(t), func(doc *nodeproto.PlanDoc) {
		doc.Jobs[0].Concurrency = []nodeproto.ConcurrencySpec{{Group: "deploy", Scope: new("box"), Capacity: new(1), OnLimit: new("queue")}}
	})
	owner, err := f.s.AsOperator().ResolveAuthority(t.Context(), f.owner.Authority)
	if err != nil {
		t.Fatal(err)
	}
	holder, err := f.s.AsOperator().ResolveAuthority(t.Context(), f.holder.Authority)
	if err != nil {
		t.Fatal(err)
	}
	if owner.Capability.ExecutionHost == holder.Capability.ExecutionHost {
		t.Fatal("fixture did not split owner from execution host")
	}
	result, err := f.tenant.MayStart(t.Context(), runstate.CapacityBinding{RunID: string(f.ref.RunID), NodeID: string(f.ref.NodeID), Generation: int64(f.holder.Generation)}, f.holder)
	if err != nil || result.Kind != runstate.AcquireGranted {
		t.Fatalf("execution admission: %+v %v", result, err)
	}
	key := runspec.ConcurrencyKey("job", "box", holder.Capability.ExecutionHost, "deploy")
	state, err := f.tenant.GetConcurrencyState(t.Context(), key)
	if err != nil || len(state.Holders) != 1 || state.Holders[0].RunID != string(f.ref.RunID) {
		t.Fatalf("wrong execution host scope: %+v %v", state, err)
	}
	key = runspec.ConcurrencyKey("job", "box", owner.Capability.ExecutionHost, "deploy")
	state, err = f.tenant.GetConcurrencyState(t.Context(), key)
	if !errors.Is(err, store.ErrNotFound) && (err != nil || state.UsedCost != 0 || len(state.Waiters) != 0) {
		t.Fatalf("coordinator reserved execution capacity: %+v %v", state, err)
	}
}

func TestWaitingGroupSetDoesNotPreemptEarlierGroup(t *testing.T) {
	f := newStoreRecoveryFixtureAt(t, false, storetest.New(t), func(doc *nodeproto.PlanDoc) {
		doc.Jobs[0].Concurrency = []nodeproto.ConcurrencySpec{
			{Group: "a-cancel", Scope: new("global"), Capacity: new(1), OnLimit: new("cancel_others")},
			{Group: "z-block", Scope: new("global"), Capacity: new(1), OnLimit: new("queue")},
		}
	})
	var competitor string
	for _, group := range []string{"a-cancel", "z-block"} {
		blocker, award := admissionPolicyBlocker(t, f, func(doc *nodeproto.PlanDoc) {
			doc.Concurrency = nil
			doc.Jobs[0].Concurrency = []nodeproto.ConcurrencySpec{{Group: group, Scope: new("global"), Capacity: new(1), OnLimit: new("queue")}}
		})
		if group == "a-cancel" {
			competitor = blocker.RunID
		}
		admissionWant(t, f, blocker, award, runstate.AcquireGranted)
	}
	result, err := f.tenant.MayStart(t.Context(), runstate.CapacityBinding{RunID: string(f.ref.RunID), NodeID: string(f.ref.NodeID), Generation: int64(f.holder.Generation)}, f.holder)
	if err != nil || result.Kind != runstate.AcquireQueued {
		t.Fatalf("group set did not wait: %+v %v", result, err)
	}
	row, err := f.tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: runspec.RunID(competitor), NodeID: "job"})
	if err != nil || row.CancelRequestedAt != nil {
		t.Fatalf("waiting set cancelled earlier group's victim: %+v %v", row, err)
	}
	state, err := f.tenant.GetConcurrencyState(t.Context(), "job:global:a-cancel")
	if err != nil || len(state.Holders) != 1 || state.Holders[0].Superseded {
		t.Fatalf("waiting set preempted earlier group: %+v %v", state, err)
	}
}

func TestAdmissionReapsCapacityWhenClaimAndOwnerBothEnd(t *testing.T) {
	for _, dialect := range []struct {
		name   string
		target func(*testing.T) *storetest.Target
	}{{"sqlite", storetest.NewSQLite}, {"postgres", storetest.NewPostgres}} {
		t.Run(dialect.name, func(t *testing.T) {
			f := newStoreRecoveryFixtureAt(t, false, dialect.target(t), func(doc *nodeproto.PlanDoc) {
				doc.Concurrency = []nodeproto.ConcurrencySpec{{Group: "plan", Scope: new("global"), Capacity: new(1)}}
				doc.Jobs[0].Concurrency = []nodeproto.ConcurrencySpec{{Group: "job", Scope: new("global"), Capacity: new(1)}}
			})
			binding := runstate.CapacityBinding{RunID: string(f.ref.RunID), NodeID: string(f.ref.NodeID), Generation: int64(f.holder.Generation)}
			if result, err := f.tenant.MayStart(t.Context(), binding, f.holder); err != nil || result.Kind != runstate.AcquireGranted {
				t.Fatalf("initial admission: %+v %v", result, err)
			}
			for _, table := range []string{"triggers", "nodes"} {
				column := "run_id"
				if table == "triggers" {
					column = "id"
				}
				if _, err := f.s.DB().ExecContext(t.Context(), storetest.Rebind(f.s, `UPDATE `+table+` SET lease_expires_at=1 WHERE team=? AND `+column+`=?`), f.tenant.Team(), f.ref.RunID); err != nil {
					t.Fatal(err)
				}
			}
			competitor, award := admissionPolicyBlocker(t, f)
			if result, err := f.tenant.MayStart(t.Context(), competitor, award); err != nil || result.Kind != runstate.AcquireGranted {
				t.Fatalf("dead work still consumes capacity: %+v %v", result, err)
			}
			var count int
			if err := f.s.DB().QueryRowContext(t.Context(), storetest.Rebind(f.s, `SELECT COUNT(*) FROM concurrency_holders WHERE team=? AND run_id=?`), f.tenant.Team(), f.ref.RunID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("dead reservations retained: %d %v", count, err)
			}
		})
	}
}
