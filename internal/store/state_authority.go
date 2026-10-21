package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func holderRefusal() error { return &ErrIllegalTransition{Reason: ReasonHolderRights} }

func checkPublisherTx(ctx context.Context, tx *storeTx, team Team, actor Actor, runID string) error {
	principal, err := tx.ResolveAuthority(ctx, actor.Authority)
	if err != nil {
		return err
	}
	c := principal.Capability
	if actor.Kind != ActorOperator || c.Team != string(team) || c.Audience != capability.Controller || c.Prefix != actor.CapabilityPrefix || c.Binding.Kind != capability.BindingRun || c.Binding.RunID != runID || !slices.Contains(c.Grants, capability.DataWrite) || c.Data == nil || c.Data.Use != capability.DataPublish || c.Data.RunID != runID {
		return holderRefusal()
	}
	var mode, repo string
	if err := tx.QueryRowContext(ctx, `SELECT intake_mode,repo_key FROM runs WHERE team=? AND id=?`, team, runID).Scan(&mode, &repo); err != nil {
		return err
	}
	if mode != "publish" || c.Data.RepositoryKey != repo {
		return holderRefusal()
	}
	return nil
}

func checkOwnerTx(ctx context.Context, tx *storeTx, team Team, actor Actor, runID string, now time.Time) error {
	if actor.Owner.Kind != OwnerController {
		principal, err := tx.ResolveAuthority(ctx, actor.Authority)
		if err != nil {
			return err
		}
		if principal.Capability.Unit == capability.UnitPlan {
			return holderRefusal()
		}
	}
	return checkOwnerLeaseTx(ctx, tx, team, actor, runID, now)
}

func checkOwnerLeaseTx(ctx context.Context, tx *storeTx, team Team, actor Actor, runID string, now time.Time) error {
	ref := actor.Owner
	if ref.RunID != "" && ref.RunID != runID {
		return holderRefusal()
	}
	if ref.Kind == OwnerController {
		if actor.Kind != ActorController || ref.Name != "controller.owner" {
			return holderRefusal()
		}
		scheduled, err := controllerOwnershipTx(ctx, tx, team, runID)
		if err != nil {
			return err
		}
		if !scheduled {
			return holderRefusal()
		}
		return assertSingletonLeaseTx(ctx, tx, SingletonLease{Name: ref.Name, Holder: ref.Holder, Generation: ref.Generation}, "controller.owner", now)
	}
	if ref.Kind != OwnerTrigger || ref.RunID != runID || ref.Holder == "" || ref.Generation <= 0 {
		return holderRefusal()
	}
	principal, err := tx.ResolveAuthority(ctx, actor.Authority)
	if err != nil {
		return err
	}
	c := principal.Capability
	if c.Team != string(team) || c.Binding.RunID != runID || c.Binding.Holder != ref.Holder || c.Binding.Generation != ref.Generation || c.Prefix != actor.CapabilityPrefix {
		return holderRefusal()
	}
	if c.Unit == capability.UnitPlan {
		accepted, err := planAcceptedTx(ctx, tx, team, runID)
		if err != nil {
			return err
		}
		if accepted || c.Binding.Kind != capability.BindingRun || !slices.Contains(c.Grants, capability.NodeResult) {
			return holderRefusal()
		}
	}
	var holder, prefix, status string
	var generation int64
	var until sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(claimed_by,''),capability_prefix,status,generation,lease_expires_at FROM triggers WHERE team=? AND id=?`+tx.forUpdate(), team, runID).Scan(&holder, &prefix, &status, &generation, &until)
	if err != nil {
		return err
	}
	now, err = tx.now(ctx)
	if err != nil {
		return err
	}
	if status != "claimed" || holder != ref.Holder || generation != ref.Generation || prefix != actor.CapabilityPrefix || !until.Valid || until.Int64 <= now.UnixNano() {
		return holderRefusal()
	}
	return nil
}

func checkReaperTx(ctx context.Context, tx *storeTx, actor Actor, now time.Time) error {
	ref := actor.Owner
	if actor.Kind != ActorReaper || ref.Kind != OwnerController || ref.Name != "controller.reaper" {
		return holderRefusal()
	}
	return assertSingletonLeaseTx(ctx, tx, SingletonLease{Name: ref.Name, Holder: ref.Holder, Generation: ref.Generation}, "controller.reaper", now)
}

func checkHolderTx(ctx context.Context, tx *storeTx, team Team, row NodeRow, actor Actor, now time.Time) error {
	var err error
	now, err = tx.now(ctx)
	if err != nil {
		return err
	}
	claimed := nodeFence(runspec.RunID(row.Ref.RunID), runspec.NodeID(row.Ref.NodeID), row.ClaimedBy, row.Generation)
	if actor.Kind != ActorHolder || actor.Holder == "" || actor.CapabilityPrefix == "" || claimed != nodeFence(runspec.RunID(row.Ref.RunID), runspec.NodeID(row.Ref.NodeID), actor.Holder, actor.Generation) || row.CapabilityPrefix != actor.CapabilityPrefix || row.LeaseExpiresAt == nil || !row.LeaseExpiresAt.After(now) {
		return holderRefusal()
	}
	principal, err := tx.ResolveAuthority(ctx, actor.Authority)
	if err != nil {
		return err
	}
	c := principal.Capability
	if c.Team != string(team) || c.Prefix != actor.CapabilityPrefix || c.Binding.Kind != capability.BindingNode || claimed != nodeFence(runspec.RunID(c.Binding.RunID), runspec.NodeID(c.Binding.NodeID), c.Binding.Holder, runspec.ClaimGeneration(c.Binding.Generation)) {
		return holderRefusal()
	}
	return nil
}

func checkCancelActorTx(ctx context.Context, tx *storeTx, team Team, actor Actor, runID string, now time.Time) error {
	if storeCancelActor(tx, team, actor) {
		return nil
	}
	switch actor.Kind {
	case ActorController, ActorRunHost:
		return checkOwnerLeaseTx(ctx, tx, team, actor, runID, now)
	case ActorReaper:
		return checkReaperTx(ctx, tx, actor, now)
	case ActorOperator:
		principal, err := tx.ResolveAuthority(ctx, actor.Authority)
		if err != nil {
			return err
		}
		c := principal.Capability
		if c.Team != string(team) || !slices.Contains(c.Grants, capability.Grant("runs.control")) || (c.Binding.RunID != "" && c.Binding.RunID != runID) {
			return holderRefusal()
		}
		return nil
	default:
		return holderRefusal()
	}
}

func lockStateRunTx(ctx context.Context, tx *storeTx, team Team, id string) error {
	var found string
	err := tx.QueryRowContext(ctx, `SELECT id FROM runs WHERE team=? AND id=?`+tx.forNoKeyUpdate(), team, id).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("run", id)
	}
	return err
}

func prepareStateActorTx(ctx context.Context, tx *storeTx, team Team, runID string, actor Actor, now time.Time) error {
	if actor.Kind == ActorLauncher {
		_, err := checkLauncherActorTx(ctx, tx, team, runID, actor)
		return err
	}
	if storeCancelActor(tx, team, actor) {
		return nil
	}
	if actor.Kind == ActorReaper {
		return checkReaperTx(ctx, tx, actor, now)
	}
	if actor.Kind == ActorController {
		ref := actor.Owner
		if ref.Kind != OwnerController || ref.Name != "controller.owner" || (ref.RunID != "" && ref.RunID != runID) {
			return holderRefusal()
		}
		return assertSingletonLeaseTx(ctx, tx, SingletonLease{Name: ref.Name, Holder: ref.Holder, Generation: ref.Generation}, "controller.owner", now)
	}
	principal, err := tx.ResolveAuthority(ctx, actor.Authority)
	if err != nil {
		return err
	}
	c := principal.Capability
	if c.Team != string(team) && !(actor.Kind == ActorClaimant && capability.CloudLauncher(c)) || (c.Binding.RunID != "" && c.Binding.RunID != runID) {
		return holderRefusal()
	}
	if actor.CapabilityPrefix != "" && actor.CapabilityPrefix != c.Prefix {
		return holderRefusal()
	}
	if actor.Kind == ActorRunHost {
		var id string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM triggers WHERE team=? AND id=?`+tx.forUpdate(), team, runID).Scan(&id); err != nil {
			return err
		}
	}
	return nil
}

// safety: the store's own cancellation of a team's work, for a team deletion or a funding stop, needs no credential.
func storeCancelActor(tx *storeTx, team Team, actor Actor) bool {
	return team != "" && tx.cancelingTeam == team && actor == (Actor{Kind: ActorOperator})
}

func planAcceptedTx(ctx context.Context, tx *storeTx, team Team, runID string) (bool, error) {
	var accepted bool
	err := tx.QueryRowContext(ctx, `SELECT plan_accepted_generation>0 FROM runs WHERE team=? AND id=?`, team, runID).Scan(&accepted)
	return accepted, err
}
