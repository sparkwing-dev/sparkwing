package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// ReadLauncherSync compares observed awards with live ownership without releasing holder rights.
func (o *Operator) ReadLauncherSync(ctx context.Context, authority capability.AuthorityRef, observed []controllerwire.LauncherObservation) (controllerwire.LauncherSyncResponse, error) {
	out := controllerwire.LauncherSyncResponse{Awards: []controllerwire.ClaimedUnit{}, Remove: []controllerwire.LauncherJobRef{}, RemoveReservations: []controllerwire.LauncherReservationRef{}}
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		p, err := tx.ResolveAuthority(ctx, authority)
		if err != nil {
			return err
		}
		if !launcherClaimant(p.Capability) {
			return bindingRefusal()
		}
		seen := map[capability.LeaseRef]bool{}
		jobs := map[controllerwire.LauncherJobRef]bool{}
		for _, observation := range observed {
			if err := observation.Validate(); err != nil {
				return err
			}
			reservation := controllerwire.LauncherReservationRef{Award: observation.Award, NodeUID: observation.NodeUID, ReservationID: observation.ReservationID}
			if seen[observation.Award] || observation.ReservationID == "" && jobs[observation.Job] {
				return authorityError(apierr.BadRequest, "duplicate launcher observation")
			}
			seen[observation.Award], jobs[observation.Job] = true, true
			if observation.ReservationID != "" {
				if err := launcherReservationAwardTx(ctx, tx, authority, observation.Award); err != nil {
					return err
				}
			}
			unit, live, err := launcherAwardTx(ctx, tx, authority, observation.Award)
			if err != nil {
				return err
			}
			if live {
				out.Awards = append(out.Awards, unit)
			} else if observation.ReservationID != "" {
				out.RemoveReservations = append(out.RemoveReservations, reservation)
			} else {
				out.Remove = append(out.Remove, observation.Job)
			}
		}
		return nil
	})
	if err != nil {
		return controllerwire.LauncherSyncResponse{}, err
	}
	return out, nil
}

func launcherReservationAwardTx(ctx context.Context, tx *storeTx, launcher capability.AuthorityRef, lease capability.LeaseRef) error {
	var prefix string
	err := tx.QueryRowContext(ctx, "SELECT prefix FROM capabilities WHERE unit_kind=? AND unit_id=? AND bind_holder=? AND COALESCE(eval_unit_generation,bind_generation)=? AND issuance='award' AND delivery_purpose='bootstrap'", string(lease.Kind), lease.ID, lease.Holder, lease.Generation).Scan(&prefix)
	if errors.Is(err, sql.ErrNoRows) {
		return bindingRefusal()
	}
	if err != nil {
		return err
	}
	c, err := loadCapabilityTx(ctx, tx, prefix)
	if err != nil {
		return err
	}
	fence := capability.LeaseRef{Kind: c.Unit, ID: c.UnitID, Holder: c.Binding.Holder, Generation: c.Binding.Generation}
	if c.Unit == capability.UnitEval && c.Binding.Expansion != nil {
		fence = *c.Binding.Expansion
	}
	if fence != lease || c.Billing == nil || c.Billing.ExecutorClass != "cloud" {
		return bindingRefusal()
	}
	seen := map[capability.AuthorityRef]bool{}
	for c.Parent != nil {
		parent := *c.Parent
		if parent == launcher {
			return nil
		}
		if seen[parent] || parent.Kind != capability.AuthorityCapability {
			return bindingRefusal()
		}
		seen[parent] = true
		c, err = loadCapabilityTx(ctx, tx, parent.ID)
		if err != nil {
			return err
		}
	}
	return bindingRefusal()
}

func launcherAwardTx(ctx context.Context, tx *storeTx, launcher capability.AuthorityRef, lease capability.LeaseRef) (controllerwire.ClaimedUnit, bool, error) {
	unit, err := decodeAwardUnit(lease.Kind, lease.ID)
	if err != nil {
		return controllerwire.ClaimedUnit{}, false, authorityError(apierr.BadRequest, "invalid launcher award identity")
	}
	row, err := loadAwardRowTx(ctx, tx, capability.AwardSpec{Unit: lease.Kind, UnitID: lease.ID})
	var refusal *apierr.Error
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrNotFound) || errors.As(err, &refusal) && refusal.Code == apierr.LockHeld {
		return controllerwire.ClaimedUnit{}, false, nil
	}
	if err != nil {
		return controllerwire.ClaimedUnit{}, false, err
	}
	if row.prefix == "" || row.holder != lease.Holder || row.generation != lease.Generation || !row.lease.Valid || row.lease.Int64 <= tx.now().UnixNano() || row.cancel.Valid {
		return controllerwire.ClaimedUnit{}, false, nil
	}
	chain, err := authorityChainTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: row.prefix}, true)
	if err != nil {
		var failure *apierr.Error
		if errors.As(err, &failure) && failure.Code == apierr.Unauthenticated {
			return controllerwire.ClaimedUnit{}, false, nil
		}
		return controllerwire.ClaimedUnit{}, false, err
	}
	if len(chain) < 2 || !slices.ContainsFunc(chain[1:], func(n authorityNode) bool { return n.principal.Authority == launcher }) {
		return controllerwire.ClaimedUnit{}, false, nil
	}
	c := chain[0].principal.Capability
	if c.Unit != lease.Kind || c.UnitID != lease.ID || c.Billing == nil || c.Billing.ExecutorClass != "cloud" {
		return controllerwire.ClaimedUnit{}, false, nil
	}
	run, err := readRunTx(ctx, tx, Team(c.Team), unit.RunID)
	if err != nil {
		return controllerwire.ClaimedUnit{}, false, err
	}
	if run.CancelRequestedAt != nil || run.FinishedAt != nil {
		return controllerwire.ClaimedUnit{}, false, nil
	}
	result := controllerwire.ClaimedUnit{Unit: c.Unit, Purpose: c.Purpose, Delivery: capability.DeliveryBootstrap, UnitID: c.UnitID, RunID: unit.RunID, JobID: unit.NodeID, Binding: c.Binding, Request: run.Request, Runner: nodeproto.RunnerIdentity{Name: row.holder, Type: c.Billing.ExecutorClass}, LeaseExpiresAt: time.Unix(0, row.lease.Int64).UTC(), OwnerDeadline: c.Binding.Deadline, SourceGeneration: unit.SourceGeneration}
	for _, ancestor := range chain[1:] {
		for _, expiry := range []*time.Time{ancestor.principal.Capability.ExpiresAt, ancestor.principal.Capability.RevokedAt, ancestor.principal.Capability.Binding.Deadline} {
			if expiry != nil && (result.ParentCapabilityExpiry == nil || expiry.Before(*result.ParentCapabilityExpiry)) {
				result.ParentCapabilityExpiry = expiry
			}
		}
	}
	if run.Spec != nil {
		result.Accepted = &controllerwire.PlanAccepted{Spec: *run.Spec, Plan: run.Plan, PlanHash: runspec.Hash(run.PlanHash)}
		result.Engine = &run.Spec.Resolved.Engine
	}
	if unit.Kind == capability.UnitNode {
		node, err := readNodeTx(ctx, tx, Team(c.Team), NodeRef{unit.RunID, unit.NodeID})
		if err != nil {
			return controllerwire.ClaimedUnit{}, false, err
		}
		result.Execution = &node.Execution
	}
	return result, true, nil
}
