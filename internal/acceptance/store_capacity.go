package acceptance

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func checkRecoveryCapacity(ctx context.Context, root string) (err error) {
	path := filepath.Join(root, "capacity.db")
	f, err := newStoreRecoveryFixture(ctx, path, true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.s.Close()) }()
	if err := seedCapacityCompetitor(ctx, f.s); err != nil {
		return err
	}
	for _, group := range []struct{ key, node string }{{"node", "job"}, {"plan", ""}} {
		request := store.AcquireSlotRequest{Key: group.key, HolderID: "original", RunID: "run", NodeID: group.node, Capacity: 1, Cost: 1, Lease: time.Hour, Policy: store.OnLimitQueue}
		if got, err := acquireRecoveryCapacity(ctx, f, request); err != nil || got.Kind != store.AcquireGranted {
			return fmt.Errorf("acquire %s: %+v, %v", group.key, got, err)
		}
		request.RunID, request.HolderID = "competitor", "waiting"
		if got, err := acquireRecoveryCapacity(ctx, f, request); err != nil || got.Kind != store.AcquireQueued {
			return fmt.Errorf("queue %s: %+v, %v", group.key, got, err)
		}
	}
	if _, err := f.s.DB().ExecContext(ctx, `UPDATE triggers SET lease_expires_at=1 WHERE team=? AND id='run'`, store.DefaultTeam); err != nil {
		return err
	}
	if _, err := f.tenant.RenewNodeClaim(ctx, store.NodeRef{RunID: "run", NodeID: "job"}, f.holder); err != nil {
		return err
	}
	if units, err := f.s.ReapUnits(ctx, f.lease, 10); err != nil || len(units) != 1 || !units[0].Requeued || units[0].RunID != "run" {
		return fmt.Errorf("owner recovery: %+v, %v", units, err)
	}
	if err := f.reopen(ctx, path); err != nil {
		return err
	}
	until := time.Now().UTC().Add(time.Hour)
	claimant := capability.Capability{Digest: sha256.Sum256([]byte("successor")), Prefix: "successor", Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "successor"}, Audience: capability.Controller, Grants: []capability.Grant{capability.TriggersClaim, capability.NodesClaim}, Binding: capability.Binding{Kind: capability.BindingNone}, Issuance: capability.Delegate, IssuedAt: time.Now().UTC(), ExpiresAt: &until, ReservedPools: []string{}, Repositories: []string{"gh:1"}}
	if err := seedRecoveryCapability(ctx, f.s, claimant); err != nil {
		return err
	}
	claim, err := f.s.ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: claimant.Prefix, Target: &store.UnitRef{Kind: capability.UnitTrigger, RunID: "run"}})
	if err != nil {
		return err
	}
	owner := store.Actor{Kind: store.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Owner: claim.Owner}
	for _, group := range []struct{ key, node string }{{"node", "job"}, {"plan", ""}} {
		generation := int64(2)
		if group.node == "" {
			generation = owner.Owner.Generation
		}
		adopted, err := f.tenant.AdoptCapacity(ctx, group.key, "original", store.CapacityBinding{RunID: "run", NodeID: group.node, Generation: generation}, owner)
		if err != nil || adopted.Kind != store.AdoptHolder || adopted.Holder == nil || adopted.Holder.Binding.Generation != generation {
			return fmt.Errorf("adopt %s: %+v, %v", group.key, adopted, err)
		}
		if resolution, err := resolveRecoveryCapacity(ctx, f.tenant, group.key, group.node); err != nil || resolution.Status != store.WaiterStillWaiting {
			return fmt.Errorf("live %s capacity admitted competitor: %+v, %v", group.key, resolution, err)
		}
	}
	if _, err := f.tenant.TransitionNode(ctx, store.NodeTransition{Ref: store.NodeRef{RunID: "run", NodeID: "job"}, Actor: f.holder, To: store.NodeSuccess, Cause: store.CauseReport, SourceAttempt: recoverySourceAttempt(1)}); err != nil {
		return err
	}
	if err := checkCapacityWinner(ctx, f.tenant, "node"); err != nil {
		return err
	}
	if state, err := f.tenant.GetConcurrencyState(ctx, "plan"); err != nil || state.UsedCost != 1 || len(state.Holders) != 1 || state.Holders[0].HolderID != "original" {
		return fmt.Errorf("job completion released plan capacity: %+v, %v", state, err)
	}
	if _, err := f.tenant.FinishRunAndUnit(ctx, store.FinishRunRequest{Transition: store.RunTransition{RunID: "run", Actor: owner, To: store.RunSuccess, Cause: store.RunFinish}, Unit: owner.Owner}); err != nil {
		return err
	}
	return checkCapacityWinner(ctx, f.tenant, "plan")
}

func seedCapacityCompetitor(ctx context.Context, s *store.Store) error {
	_, err := s.DB().ExecContext(ctx, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days,spec_json,spec_hash,sealed_at,plan_json,plan_hash,plan_accepted_generation)
SELECT team,'competitor',2,pipeline,'running',request_json,request_hash,request_sealed_at,repo_key,'controller',intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days,spec_json,spec_hash,sealed_at,plan_json,plan_hash,1 FROM runs WHERE team=? AND id='run'`, store.DefaultTeam)
	if err != nil {
		return err
	}
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES (?,'competitor','job','pending',?,?)`, store.DefaultTeam, []byte(`{"id":"job"}`), []byte(`{}`)); err != nil {
		return err
	}
	_, err = s.AsOperator().AcquireSingletonLease(ctx, "controller.owner", "capacity", time.Hour)
	return err
}

func checkCapacityWinner(ctx context.Context, tenant *store.Tenant, key string) error {
	nodeID := "job"
	if key == "plan" {
		nodeID = ""
	}
	for range 2 {
		resolution, err := resolveRecoveryCapacity(ctx, tenant, key, nodeID)
		if err != nil || resolution.Status != store.WaiterPromoted || resolution.HolderID != "waiting" {
			return fmt.Errorf("released %s capacity: %+v, %v", key, resolution, err)
		}
	}
	state, err := tenant.GetConcurrencyState(ctx, key)
	if err != nil || state.UsedCost != 1 || len(state.Holders) != 1 || state.Holders[0].RunID != "competitor" || len(state.Waiters) != 0 {
		return fmt.Errorf("winner %s: %+v, %v", key, state, err)
	}
	return nil
}

func acquireRecoveryCapacity(ctx context.Context, f storeRecoveryFixture, request store.AcquireSlotRequest) (store.AcquireSlotResponse, error) {
	actor := f.owner
	if request.RunID != actor.Owner.RunID {
		run, err := f.tenant.ReadRun(ctx, request.RunID)
		if err != nil {
			return store.AcquireSlotResponse{}, err
		}
		if run.Owner == nil {
			return store.AcquireSlotResponse{}, errors.New("capacity competitor has no owner")
		}
		actor = store.Actor{Kind: store.ActorController, Owner: *run.Owner}
	}
	binding := store.CapacityBinding{RunID: request.RunID, NodeID: request.NodeID, Generation: actor.Owner.Generation}
	if request.NodeID != "" {
		row, err := f.tenant.ReadNode(ctx, store.NodeRef{RunID: request.RunID, NodeID: request.NodeID})
		if err != nil {
			return store.AcquireSlotResponse{}, err
		}
		binding.Generation = row.Generation
	}
	return f.tenant.AcquireCapacity(ctx, request, binding, actor)
}

func resolveRecoveryCapacity(ctx context.Context, tenant *store.Tenant, key, nodeID string) (store.WaiterResolution, error) {
	run, err := tenant.ReadRun(ctx, "competitor")
	if err != nil {
		return store.WaiterResolution{}, err
	}
	if run.Owner == nil {
		return store.WaiterResolution{}, errors.New("capacity competitor has no owner")
	}
	binding := store.CapacityBinding{RunID: run.ID, NodeID: nodeID, Generation: run.Owner.Generation}
	if nodeID != "" {
		row, err := tenant.ReadNode(ctx, store.NodeRef{RunID: run.ID, NodeID: nodeID})
		if err != nil {
			return store.WaiterResolution{}, err
		}
		binding.Generation = row.Generation
	}
	return tenant.ResolveCapacityWaiter(ctx, key, binding, store.Actor{Kind: store.ActorController, Owner: *run.Owner})
}
