package store_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func newRecoveryCapacityFixture(t *testing.T) *storeRecoveryFixture {
	t.Helper()
	return newStoreRecoveryFixtureAt(t, true, storetest.NewSQLite(t), func(doc *nodeproto.PlanDoc) {
		scope, capacity, cost, policy := "global", 1, 1, "queue"
		doc.Concurrency = []nodeproto.ConcurrencySpec{{Group: "plan", Scope: &scope, Capacity: &capacity, Cost: &cost, OnLimit: &policy}}
		for _, name := range []string{"node", "deploy"} {
			doc.Jobs[0].Concurrency = append(doc.Jobs[0].Concurrency, nodeproto.ConcurrencySpec{Group: name, Scope: &scope, Capacity: &capacity, Cost: &cost, OnLimit: &policy})
		}
	})
}

func (f *storeRecoveryFixture) claimOwner(ctx context.Context, runID, name string) (store.Actor, error) {
	authority := f.plan.Seal.Request.AuthorityRef
	issued, err := f.s.AsOperator().ProvisionCapability(ctx, capability.ProvisionSpec{Kind: capability.ProvisionMachine, Authority: &authority, Team: string(f.tenant.Team()), RegistrationID: "runner:planner:" + name, Subject: capability.Subject{Kind: capability.SubjectMachine, ID: name}, Grants: []capability.Grant{capability.TriggersClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Repositories: []string{"github.com/acme/*"}})
	if err != nil {
		return store.Actor{}, err
	}
	claim, err := f.s.AsOperator().ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: issued.Capability.Prefix, Target: &store.UnitRef{Kind: capability.UnitTrigger, RunID: runID}, Available: match.Resources{Cores: 8}})
	if err != nil {
		return store.Actor{}, err
	}
	return store.Actor{Kind: store.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Owner: claim.Owner}, nil
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

func checkRecoveryCapacity(t *testing.T) error {
	ctx := t.Context()
	f := newRecoveryCapacityFixture(t)
	competitor, err := f.capacityCompetitor(ctx)
	if err != nil {
		return err
	}
	for _, group := range []struct{ key, node string }{{"global:node", "job"}, {"global:plan", ""}} {
		request := store.AcquireSlotRequest{Key: group.key, HolderID: "original", RunID: f.ref.RunID, NodeID: group.node, Capacity: 1, Cost: 1, Lease: time.Hour, Policy: store.OnLimitQueue}
		if got, err := f.tenant.AcquireConcurrencySlot(ctx, request); err != nil || got.Kind != store.AcquireGranted {
			return fmt.Errorf("acquire %s: %+v, %v", group.key, got, err)
		}
		request.RunID, request.HolderID = competitor, "waiting"
		if got, err := f.tenant.AcquireConcurrencySlot(ctx, request); err != nil || got.Kind != store.AcquireQueued {
			return fmt.Errorf("queue %s: %+v, %v", group.key, got, err)
		}
	}
	if _, err := f.s.DB().ExecContext(ctx, `UPDATE triggers SET lease_expires_at=? WHERE team=? AND id=?`, time.Now().UnixNano(), f.tenant.Team(), f.ref.RunID); err != nil {
		return err
	}
	if _, err := f.tenant.RenewNodeClaim(ctx, f.ref, f.holder); err != nil {
		return err
	}
	if units, err := f.s.AsOperator().ReapUnits(ctx, f.lease, 10); err != nil || len(units) != 1 || !units[0].Requeued || units[0].RunID != f.ref.RunID {
		return fmt.Errorf("owner recovery: %+v, %v", units, err)
	}
	if err := f.reopen(ctx); err != nil {
		return err
	}
	owner, err := f.claimOwner(ctx, f.ref.RunID, "successor")
	if err != nil {
		return err
	}
	for _, group := range []struct{ key, node string }{{"global:node", "job"}, {"global:plan", ""}} {
		generation := f.holder.Generation
		if group.node == "" {
			generation = owner.Owner.Generation
		}
		adopted, err := f.tenant.AdoptCapacity(ctx, group.key, "original", store.CapacityBinding{RunID: f.ref.RunID, NodeID: group.node, Generation: generation}, owner)
		if err != nil || adopted.Kind != store.AdoptHolder || adopted.Holder == nil || adopted.Holder.Binding.Generation != generation {
			return fmt.Errorf("adopt %s: %+v, %v", group.key, adopted, err)
		}
		if promoted, err := f.tenant.PromoteNextWaiters(ctx, group.key, time.Hour); err != nil || len(promoted) != 0 {
			return fmt.Errorf("live %s capacity admitted competitor: %+v, %v", group.key, promoted, err)
		}
	}
	if _, err := f.tenant.TransitionNode(ctx, store.NodeTransition{Ref: f.ref, Actor: f.holder, To: store.NodeSuccess, Cause: store.CauseReport, SourceAttempt: acceptanceAttempt(1)}); err != nil {
		return err
	}
	if err := checkCapacityWinner(ctx, f.tenant, "global:node", competitor); err != nil {
		return err
	}
	if state, err := f.tenant.GetConcurrencyState(ctx, "global:plan"); err != nil || state.UsedCost != 1 || len(state.Holders) != 1 || state.Holders[0].HolderID != "original" {
		return fmt.Errorf("job completion released plan capacity: %+v, %v", state, err)
	}
	if _, err := f.tenant.FinishRunAndUnit(ctx, store.FinishRunRequest{Transition: store.RunTransition{RunID: f.ref.RunID, Actor: owner, To: store.RunSuccess, Cause: store.RunFinish}, Unit: owner.Owner}); err != nil {
		return err
	}
	return checkCapacityWinner(ctx, f.tenant, "global:plan", competitor)
}

func checkCapacityWinner(ctx context.Context, tenant *store.Tenant, key, competitor string) error {
	promoted, err := tenant.PromoteNextWaiters(ctx, key, time.Hour)
	if err != nil || len(promoted) != 1 || promoted[0].RunID != competitor {
		return fmt.Errorf("released %s capacity: %+v, %v", key, promoted, err)
	}
	if repeated, err := tenant.PromoteNextWaiters(ctx, key, time.Hour); err != nil || len(repeated) != 0 {
		return fmt.Errorf("duplicate %s promotion: %+v, %v", key, repeated, err)
	}
	state, err := tenant.GetConcurrencyState(ctx, key)
	if err != nil || state.UsedCost != 1 || len(state.Holders) != 1 || state.Holders[0].RunID != competitor || len(state.Waiters) != 0 {
		return fmt.Errorf("winner %s: %+v, %v", key, state, err)
	}
	return nil
}

func checkRecoveryCapacityMaintenance(t *testing.T) error {
	ctx := t.Context()
	f := newRecoveryCapacityFixture(t)
	competitor, err := f.capacityCompetitor(ctx)
	if err != nil {
		return err
	}
	request := store.AcquireSlotRequest{Key: "global:deploy", HolderID: "original", RunID: f.ref.RunID, NodeID: "job", Capacity: 1, Cost: 1, Lease: time.Hour, Policy: store.OnLimitQueue}
	if got, err := f.tenant.AcquireConcurrencySlot(ctx, request); err != nil || got.Kind != store.AcquireGranted {
		return fmt.Errorf("original capacity: %+v, %v", got, err)
	}
	request.HolderID, request.RunID = "waiting", competitor
	if got, err := f.tenant.AcquireConcurrencySlot(ctx, request); err != nil || got.Kind != store.AcquireQueued {
		return fmt.Errorf("competing capacity: %+v, %v", got, err)
	}
	if _, err := f.s.DB().ExecContext(ctx, `UPDATE concurrency_holders SET lease_expires_at=1 WHERE team=? AND key='global:deploy' AND holder_id='original'`, f.tenant.Team()); err != nil {
		return err
	}
	forged := f.lease
	forged.Generation++
	if _, err := f.s.AsOperator().MaintainConcurrency(ctx, forged, store.ConcurrencyMaintenanceOptions{}); err == nil {
		return errors.New("forged reaper swept capacity")
	}
	result, err := f.s.AsOperator().MaintainConcurrency(ctx, f.lease, store.ConcurrencyMaintenanceOptions{})
	if err != nil || len(result.StaleHolders) != 1 || result.Promoted != 1 {
		return fmt.Errorf("elected capacity recovery: %+v, %v", result, err)
	}
	if err := f.reopen(ctx); err != nil {
		return err
	}
	adopted, err := f.tenant.AdoptCapacity(ctx, "global:deploy", "original", store.CapacityBinding{RunID: f.ref.RunID, NodeID: "job", Generation: f.holder.Generation}, f.owner)
	if err != nil || adopted.Kind != store.AcquireAgain {
		return fmt.Errorf("expired adoption: %+v, %v", adopted, err)
	}
	request.HolderID, request.RunID = "original", f.ref.RunID
	if got, err := f.tenant.AcquireConcurrencySlot(ctx, request); err != nil || got.Kind != store.AcquireQueued {
		return fmt.Errorf("returning arrival: %+v, %v", got, err)
	}
	if repeated, err := f.s.AsOperator().MaintainConcurrency(ctx, f.lease, store.ConcurrencyMaintenanceOptions{}); err != nil || !reflect.DeepEqual(repeated, store.ConcurrencyMaintenanceResult{}) {
		return fmt.Errorf("repeated capacity pass: %+v, %v", repeated, err)
	}
	state, err := f.tenant.GetConcurrencyState(ctx, "global:deploy")
	if err != nil || state.UsedCost != 1 || len(state.Holders) != 1 || state.Holders[0].RunID != competitor || len(state.Waiters) != 1 || state.Waiters[0].RunID != f.ref.RunID {
		return fmt.Errorf("capacity after reopening: %+v, %v", state, err)
	}
	return nil
}

func TestStoreRecoveryCapacityAdoption(t *testing.T) {
	if err := checkRecoveryCapacity(t); err != nil {
		t.Fatal(err)
	}
}

func TestStoreRecoveryCapacityMaintenance(t *testing.T) {
	if err := checkRecoveryCapacityMaintenance(t); err != nil {
		t.Fatal(err)
	}
}
