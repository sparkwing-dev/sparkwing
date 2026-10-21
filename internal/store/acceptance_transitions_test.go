package store_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func checkStoreTransitions(t *testing.T) (err error) {
	ctx := t.Context()
	target := storetest.NewSQLite(t)
	s, err := target.TryOpen(store.WithPolicies(sealStorePolicies))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	tenant, plan, err := storePlanSealRequest(ctx, s)
	if err != nil {
		return err
	}
	if _, err := tenant.AcceptPlan(ctx, plan); err != nil {
		return err
	}
	ref := controllerwire.NodeRef{RunID: plan.RunID, NodeID: "alpha"}
	ready := store.ReadyNode{Ref: ref, Actor: plan.Actor}
	withdraw := store.RevokeNode{Ref: ref, Actor: plan.Actor}
	for _, change := range []store.NodeCommand{ready, withdraw} {
		if _, err := tenant.TransitionNode(ctx, change); err != nil {
			return err
		}
	}
	demand := store.UnitDemand{CapabilityPrefix: plan.Actor.CapabilityPrefix, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: runspec.RunID(ref.RunID), NodeID: runspec.NodeID(ref.NodeID)}, Available: match.Resources{Cores: 8}}
	if _, err := s.AsOperator().ClaimUnit(ctx, demand); err == nil {
		return errors.New("withdrawn job remained claimable")
	}
	if err := s.Close(); err != nil {
		return err
	}
	reopened, err := target.TryOpen(store.WithPolicies(sealStorePolicies))
	if err != nil {
		return err
	}
	s = reopened
	tenant, err = s.ForTeam(ctx, acceptanceTeam)
	if err != nil {
		return err
	}
	if replay, err := tenant.TransitionNode(ctx, withdraw); err != nil || !replay.Replayed || replay.StateSeq != 2 {
		return fmt.Errorf("withdrawal replay after reopen: %+v, %v", replay, err)
	}
	if _, err := tenant.TransitionNode(ctx, ready); err != nil {
		return err
	}
	claim, err := s.AsOperator().ClaimUnit(ctx, demand)
	if err != nil {
		return err
	}
	return checkStoreHeldTransitions(ctx, tenant, withdraw, claim)
}

func checkStoreHeldTransitions(ctx context.Context, tenant *store.Tenant, withdraw store.RevokeNode, claim store.UnitClaim) error {
	ref := withdraw.Ref
	before, err := tenant.ReadNode(ctx, ref)
	if err != nil {
		return err
	}
	if _, err := tenant.TransitionNode(ctx, withdraw); err == nil {
		return errors.New("owner withdrew a claimed job")
	}
	after, err := tenant.ReadNode(ctx, ref)
	if err != nil || !reflect.DeepEqual(before, after) {
		return fmt.Errorf("refused withdrawal changed holder: %v", err)
	}
	holder := store.Actor{Kind: store.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Holder: claim.Award.Holder, Generation: claim.Award.Generation}
	stale := holder
	stale.Generation++
	if _, err := tenant.TransitionNode(ctx, store.StartNode{Ref: ref, Actor: stale, SourceAttempt: acceptanceAttempt(0)}); err == nil {
		return errors.New("stale generation started a job")
	}
	after, err = tenant.ReadNode(ctx, ref)
	if err != nil || !reflect.DeepEqual(before, after) {
		return fmt.Errorf("refused start changed holder: %v", err)
	}
	start, err := tenant.TransitionNode(ctx, store.StartNode{Ref: ref, Actor: holder, SourceAttempt: acceptanceAttempt(0)})
	if err != nil || start.Attempt != 1 {
		return fmt.Errorf("first start: %+v, %v", start, err)
	}
	finish := store.ReportNode{Ref: ref, Actor: holder, SourceAttempt: acceptanceAttempt(int64(start.Attempt)), Outcome: store.NodeSuccess}
	if _, err := tenant.TransitionNode(ctx, finish); err != nil {
		return err
	}
	return checkStoreTerminalReplay(ctx, tenant, finish, withdraw)
}

func checkStoreTerminalReplay(ctx context.Context, tenant *store.Tenant, finish store.ReportNode, withdraw store.RevokeNode) error {
	ref := finish.Ref
	for range 2 {
		replay, err := tenant.TransitionNode(ctx, finish)
		if err != nil || !replay.Replayed || replay.StateSeq != 6 || replay.Generation != finish.Actor.Generation {
			return fmt.Errorf("terminal replay: %+v, %v", replay, err)
		}
	}
	before, err := tenant.ReadNode(ctx, ref)
	if err != nil || before.Outcome != store.NodeSuccess || before.ClaimedBy != "" || before.LeaseExpiresAt != nil || before.CapabilityPrefix != "" {
		return fmt.Errorf("terminal claim not cleared: %+v, %v", before, err)
	}
	conflict := finish
	conflict.Outcome = store.NodeFailed
	ready := store.ReadyNode{Ref: ref, Actor: withdraw.Actor}
	for _, change := range []store.NodeCommand{ready, withdraw, conflict} {
		if _, err := tenant.TransitionNode(ctx, change); err == nil {
			return errors.New("terminal job accepted a different transition")
		}
	}
	after, err := tenant.ReadNode(ctx, ref)
	if err != nil || !reflect.DeepEqual(before, after) {
		return fmt.Errorf("terminal refusal changed state: %v", err)
	}
	events, err := tenant.ReadStateEvents(ctx, ref.RunID, 0, 100)
	if err != nil {
		return err
	}
	var kinds []string
	for _, event := range events {
		if event.NodeID == ref.NodeID {
			kinds = append(kinds, event.Kind)
			if event.StateSeq != int64(len(kinds)) {
				return errors.New("job event sequence is not continuous")
			}
		}
	}
	if want := []string{"job_ready", "job_revoked", "job_ready", "job_claimed", "job_started", "job_succeeded"}; !reflect.DeepEqual(kinds, want) {
		return fmt.Errorf("transition events: %v", kinds)
	}
	return nil
}

func TestStoreTransitions(t *testing.T) {
	if err := checkStoreTransitions(t); err != nil {
		t.Fatal(err)
	}
}
