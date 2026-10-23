package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func newRecoveryCapacityFixture(t *testing.T) *storeRecoveryFixture {
	t.Helper()
	return newStoreRecoveryFixtureAt(t, false, storetest.NewSQLite(t), func(doc *nodeproto.PlanDoc) {
		scope, capacity, cost, policy := "global", 1, 1, "queue"
		doc.Concurrency = []nodeproto.ConcurrencySpec{{Group: "plan", Scope: &scope, Capacity: &capacity, Cost: &cost, OnLimit: &policy}}
		for _, name := range []string{"node", "deploy"} {
			doc.Jobs[0].Concurrency = append(doc.Jobs[0].Concurrency, nodeproto.ConcurrencySpec{Group: name, Scope: &scope, Capacity: &capacity, Cost: &cost, OnLimit: &policy})
		}
	})
}

func (f *storeRecoveryFixture) claimOwner(ctx context.Context, runID, name string) (runstate.Actor, error) {
	authority := f.plan.Seal.Request.AuthorityRef
	issued, err := f.s.AsOperator().ProvisionCapability(ctx, capability.ProvisionSpec{Kind: capability.ProvisionMachine, Authority: &authority, Team: string(f.tenant.Team()), RegistrationID: "runner:planner:" + name, Subject: capability.Subject{Kind: capability.SubjectMachine, ID: name}, Grants: []capability.Grant{capability.TriggersClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}})
	if err != nil {
		return runstate.Actor{}, err
	}
	claim, err := f.s.AsOperator().ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: issued.Capability.Prefix, Target: &store.UnitRef{Kind: capability.UnitTrigger, RunID: runspec.RunID(runID)}})
	if err != nil {
		return runstate.Actor{}, err
	}
	return runstate.Actor{Kind: runstate.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Owner: claim.Owner}, nil
}

func (f *storeRecoveryFixture) capacityCompetitor(ctx context.Context) (string, error) {
	admitted, err := f.tenant.AdmitRun(ctx, store.IntakeRequest{Authority: f.plan.Seal.Request.AuthorityRef, Request: f.plan.Seal.Request.Request})
	if err != nil {
		return "", err
	}
	owner, err := f.claimOwner(ctx, admitted.RunID, "competitor")
	if err != nil {
		return "", err
	}
	plan := f.plan
	plan.RunID, plan.Actor, plan.Seal.Request = admitted.RunID, owner, admitted.Request
	_, err = f.tenant.AcceptPlan(ctx, plan)
	return admitted.RunID, err
}

func TestClaimedCapacityWaiterFollowsNodeRenewal(t *testing.T) {
	f := newRecoveryCapacityFixture(t)
	ctx := t.Context()
	blocker, award := admissionPolicyBlocker(t, f, func(doc *nodeproto.PlanDoc) { doc.Concurrency = nil })
	admissionWant(t, f, blocker, award, runstate.AcquireGranted)
	queued, err := f.tenant.MayStart(ctx, runstate.CapacityBinding{RunID: string(f.ref.RunID), NodeID: string(f.ref.NodeID), Generation: int64(f.holder.Generation)}, f.holder)
	if err != nil || queued.Kind != runstate.AcquireQueued {
		t.Fatalf("queue=%+v %v", queued, err)
	}
	var arrival int64
	if err := f.s.DB().QueryRowContext(ctx, `SELECT arrived_at FROM concurrency_waiters WHERE team=? AND run_id=? AND node_id=?`, f.tenant.Team(), f.ref.RunID, f.ref.NodeID).Scan(&arrival); err != nil {
		t.Fatal(err)
	}
	for _, live := range []bool{true, false} {
		until := time.Now().Add(time.Second).UnixNano()
		if !live {
			until = time.Now().Add(-time.Second).UnixNano()
		}
		if _, err := f.s.DB().ExecContext(ctx, `UPDATE concurrency_waiters SET lease_expires_at=? WHERE team=? AND run_id=? AND node_id=?`, until, f.tenant.Team(), f.ref.RunID, f.ref.NodeID); err != nil {
			t.Fatal(err)
		}
		beat, err := f.tenant.RenewNodeClaim(ctx, f.ref, f.holder)
		if err != nil {
			t.Fatal(err)
		}
		var retained, renewed int64
		if err := f.s.DB().QueryRowContext(ctx, `SELECT arrived_at,lease_expires_at FROM concurrency_waiters WHERE team=? AND run_id=? AND node_id=?`, f.tenant.Team(), f.ref.RunID, f.ref.NodeID).Scan(&retained, &renewed); err != nil {
			t.Fatal(err)
		}
		if retained != arrival || live && renewed < beat.ExpiresAt.UnixNano() || !live && renewed != until {
			t.Fatalf("live=%t arrival=%d/%d expiry=%d/%d beat=%s", live, retained, arrival, renewed, until, beat.ExpiresAt)
		}
	}
}
