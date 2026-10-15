package acceptance

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// CheckStoreTransitions checks withdrawal, holder fencing and terminal replay through SQLite reopening.
func CheckStoreTransitions(ctx context.Context, root string) (err error) {
	path := filepath.Join(root, "transitions.db")
	s, err := store.Open(path)
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
	ref := store.NodeRef{RunID: plan.RunID, NodeID: "alpha"}
	ready := store.NodeTransition{Ref: ref, Actor: plan.Actor, To: store.NodeReady, Cause: store.CauseRelease}
	withdraw := store.NodeTransition{Ref: ref, Actor: plan.Actor, To: store.NodePending, Cause: store.CauseRevoke}
	for _, change := range []store.NodeTransition{ready, withdraw} {
		if _, err := tenant.TransitionNode(ctx, change); err != nil {
			return err
		}
	}
	demand := store.UnitDemand{CapabilityPrefix: plan.Actor.CapabilityPrefix, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: ref.RunID, NodeID: ref.NodeID}, Available: match.Resources{Cores: 8}}
	if _, err := s.ClaimUnit(ctx, demand); err == nil {
		return errors.New("withdrawn job remained claimable")
	}
	if err := s.Close(); err != nil {
		return err
	}
	reopened, err := store.Open(path)
	if err != nil {
		return err
	}
	s = reopened
	s.SetClaimProfileResolver(recoveryEnrollment{})
	tenant, err = s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}
	if replay, err := tenant.TransitionNode(ctx, withdraw); err != nil || !replay.Replayed || replay.StateSeq != 2 {
		return fmt.Errorf("withdrawal replay after reopen: %+v, %v", replay, err)
	}
	if _, err := tenant.TransitionNode(ctx, ready); err != nil {
		return err
	}
	claim, err := s.ClaimUnit(ctx, demand)
	if err != nil {
		return err
	}
	return checkStoreHeldTransitions(ctx, tenant, withdraw, claim)
}

func checkStoreHeldTransitions(ctx context.Context, tenant *store.Tenant, withdraw store.NodeTransition, claim store.UnitClaim) error {
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
	if _, err := tenant.TransitionNode(ctx, store.NodeTransition{Ref: ref, Actor: stale, To: store.NodeRunning, Cause: store.CauseStart, SourceAttempt: recoverySourceAttempt(0)}); err == nil {
		return errors.New("stale generation started a job")
	}
	after, err = tenant.ReadNode(ctx, ref)
	if err != nil || !reflect.DeepEqual(before, after) {
		return fmt.Errorf("refused start changed holder: %v", err)
	}
	start, err := tenant.TransitionNode(ctx, store.NodeTransition{Ref: ref, Actor: holder, To: store.NodeRunning, Cause: store.CauseStart, SourceAttempt: recoverySourceAttempt(0)})
	if err != nil || start.Attempt != 1 {
		return fmt.Errorf("first start: %+v, %v", start, err)
	}
	finish := store.NodeTransition{Ref: ref, Actor: holder, To: store.NodeSuccess, Cause: store.CauseReport, SourceAttempt: recoverySourceAttempt(start.Attempt)}
	if _, err := tenant.TransitionNode(ctx, finish); err != nil {
		return err
	}
	return checkStoreTerminalReplay(ctx, tenant, finish, withdraw)
}

func checkStoreTerminalReplay(ctx context.Context, tenant *store.Tenant, finish, withdraw store.NodeTransition) error {
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
	conflict.To = store.NodeFailed
	ready := store.NodeTransition{Ref: ref, Actor: withdraw.Actor, To: store.NodeReady, Cause: store.CauseRelease}
	for _, change := range []store.NodeTransition{ready, withdraw, conflict} {
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
