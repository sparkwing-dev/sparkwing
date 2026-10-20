package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// LauncherAward contains persisted execution inputs without reusable bearer material.
type LauncherAward struct {
	CloudResources         *match.Resources
	Fence                  capability.LeaseRef
	Failure                error
	Claim                  UnitClaim
	Capability             capability.Capability
	Run                    RunRow
	Node                   NodeRow
	ParentCapabilityExpiry *time.Time
}

// ReconcileLauncherAwards isolates each persisted award through renewal, rejection and snapshot validation.
func (s *Store) ReconcileLauncherAwards(ctx context.Context, authority capability.AuthorityRef, reservations, preparing, rejected []capability.LeaseRef) ([]LauncherAward, []LauncherPreparation, error) {
	if err := validateLauncherPreparationFences(preparing); err != nil {
		return nil, nil, err
	}
	selected := append(append(slices.Clone(reservations), preparing...), rejected...)
	owned, err := s.launcherAwardOwnership(ctx, authority, selected)
	if err != nil {
		return nil, nil, err
	}
	owner := launcherAwardResults{failed: map[capability.LeaseRef]error{}, owned: owned}
	var prepared []LauncherPreparation
	for _, fence := range preparing {
		if err := owner.apply(ctx, fence, func() error {
			rows, err := s.renewLauncherPreparation(ctx, authority, []capability.LeaseRef{fence})
			prepared = append(prepared, rows...)
			return err
		}); err != nil {
			return nil, nil, err
		}
	}
	for _, fence := range rejected {
		if err := owner.apply(ctx, fence, func() error { return s.rejectLauncherAwards(ctx, authority, []capability.LeaseRef{fence}) }); err != nil {
			return nil, nil, err
		}
	}
	tx, err := s.beginSnapshotReadTx(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer rollbackOrLog(tx)
	chain, err := authorityChainTx(ctx, tx, authority, false)
	if err != nil {
		return nil, nil, err
	}
	claimant := chain[0].principal.Capability
	if authority.Kind != capability.AuthorityCapability || claimant.Audience != capability.Controller || !launcherClaimant(claimant) {
		return nil, nil, bindingRefusal()
	}
	root := chain[len(chain)-1].principal
	for _, fence := range reservations {
		if err := owner.apply(ctx, fence, func() error { return launcherReservationAwardTx(ctx, tx, root.Authority, claimant.Subject, fence) }); err != nil {
			return nil, nil, err
		}
	}
	prefixes, err := launcherPrefixesTx(ctx, tx, root.Authority, claimant.Subject, selected)
	if err != nil {
		return nil, nil, err
	}
	out := []LauncherAward{}
	for _, key := range prefixes {
		owner.owned[key.fence] = true
		if !key.live {
			continue
		}
		if err := owner.apply(ctx, key.fence, func() error {
			award, live, err := readLauncherAwardTx(ctx, tx, key.prefix, claimant, root)
			if err == nil && live && slices.Contains(reservations, key.fence) {
				award.CloudResources, err = cloudAwardResourcesTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: key.prefix})
			}
			if err == nil && live {
				award.Fence = key.fence
				out = append(out, award)
			}
			return err
		}); err != nil {
			return nil, nil, err
		}
	}
	for fence, err := range owner.failed {
		out = append(out, LauncherAward{Fence: fence, Failure: err})
	}

	if _, err := authorityChainTx(ctx, tx, authority, false); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return out, prepared, nil
}

// Raw projected ownership survives an unreadable authority envelope without granting a foreign fence a deferred result.
func (s *Store) launcherAwardOwnership(ctx context.Context, authority capability.AuthorityRef, selected []capability.LeaseRef) (map[capability.LeaseRef]bool, error) {
	tx, err := s.beginSnapshotReadTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackOrLog(tx)
	chain, err := authorityChainTx(ctx, tx, authority, false)
	if err != nil {
		return nil, err
	}
	claimant := chain[0].principal.Capability
	if authority.Kind != capability.AuthorityCapability || claimant.Audience != capability.Controller || !launcherClaimant(claimant) {
		return nil, bindingRefusal()
	}
	root := chain[len(chain)-1].principal.Authority
	keys, err := launcherPrefixesTx(ctx, tx, root, claimant.Subject, selected)
	if err != nil {
		return nil, err
	}
	owned := map[capability.LeaseRef]bool{}
	for _, key := range keys {
		owned[key.fence] = true
	}

	return owned, tx.Commit()
}

type launcherAwardResults struct {
	failed map[capability.LeaseRef]error
	owned  map[capability.LeaseRef]bool
}

func (r *launcherAwardResults) apply(ctx context.Context, fence capability.LeaseRef, fn func() error) error {
	if r.failed[fence] != nil {
		return nil
	}
	err := fn()
	if err == nil || !r.owned[fence] || !corruptLauncherAward(err) {
		return err
	}
	slog.ErrorContext(ctx, "launcher award deferred", "award", fence, "err", err)
	r.failed[fence] = err
	return nil
}

// corruptLauncherAward separates damaged persisted input from a failed database read.
func corruptLauncherAward(err error) bool {
	var integrity *apierr.Error
	var syntax *json.SyntaxError
	var shape *json.UnmarshalTypeError
	return errors.As(err, &integrity) && integrity.Code == apierr.IntegrityFailure || errors.As(err, &syntax) || errors.As(err, &shape) || errors.Is(err, ErrNotFound)
}

type launcherAwardKey struct {
	live   bool
	prefix string
	fence  capability.LeaseRef
}

func launcherPrefixesTx(ctx context.Context, tx *storeTx, root capability.AuthorityRef, subject capability.Subject, selected []capability.LeaseRef) ([]launcherAwardKey, error) {
	query := `SELECT * FROM (SELECT c.prefix,c.unit_kind,c.unit_id,c.bind_holder,COALESCE(c.eval_unit_generation,c.bind_generation) AS generation,
(c.revoked_at IS NULL OR c.revoked_at>?) AND (c.expires_at IS NULL OR c.expires_at>?) AND (
EXISTS (SELECT 1 FROM nodes n WHERE n.team=c.team AND n.run_id=c.bind_run AND n.node_id=c.bind_node AND n.capability_prefix=c.prefix AND n.status<>'done' AND n.claimed_by IS NOT NULL)
OR EXISTS (SELECT 1 FROM triggers t WHERE t.team=c.team AND t.id=c.bind_run AND t.capability_prefix=c.prefix AND t.status='claimed' AND t.finished_at IS NULL)
OR EXISTS (SELECT 1 FROM eval_units e WHERE e.team=c.team AND e.run_id=c.bind_run AND e.source_node_id=c.bind_node AND e.source_generation=c.bind_generation AND e.capability_prefix=c.prefix AND e.status IN ('claimed','running'))) AS live
FROM capabilities c WHERE c.parent_kind=? AND c.parent_id=? AND c.subject_kind=? AND c.subject_id=? AND c.audience='controller' AND c.issuance IN ('award','exchange') AND c.unit_kind IN ('plan','eval','node')) a WHERE a.live`
	args := []any{tx.now().UnixNano(), tx.now().UnixNano(), root.Kind, root.ID, subject.Kind, subject.ID}
	var wanted []string
	for _, fence := range selected {
		wanted = append(wanted, `(a.unit_kind=? AND a.unit_id=? AND a.bind_holder=? AND a.generation=?)`)
		args = append(args, fence.Kind, fence.ID, fence.Holder, fence.Generation)
	}
	if len(wanted) > 0 {
		query += ` OR ` + strings.Join(wanted, ` OR `)
	}
	rows, err := tx.QueryContext(ctx, query+` ORDER BY a.prefix`, args...)
	if err != nil {
		return nil, err
	}
	var keys []launcherAwardKey
	for rows.Next() {
		var key launcherAwardKey
		if err := rows.Scan(&key.prefix, &key.fence.Kind, &key.fence.ID, &key.fence.Holder, &key.fence.Generation, &key.live); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		keys = append(keys, key)
	}
	return keys, errors.Join(rows.Err(), rows.Close())
}

func readLauncherAwardTx(ctx context.Context, tx *storeTx, prefix string, claimant capability.Capability, root capability.Principal) (LauncherAward, bool, error) {
	c, err := loadCapabilityTx(ctx, tx, prefix)
	if err != nil {
		return LauncherAward{}, false, err
	}
	if c.Parent == nil || *c.Parent != root.Authority || c.Subject != claimant.Subject || c.Audience != capability.Controller || !slices.Contains([]capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode}, c.Unit) || c.Purpose != claimPurpose(c.Unit) || c.Billing == nil || c.Billing.ExecutorClass != launcherExecutor(claimant) || c.Delivery != capability.DeliveryBootstrap && c.Issuance != capability.Exchange {
		return LauncherAward{}, false, authorityError(apierr.IntegrityFailure, "invalid persisted launcher award")
	}
	row, err := loadAwardRowTx(ctx, tx, capability.AwardSpec{Unit: c.Unit, UnitID: c.UnitID})
	var refusal *apierr.Error
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrNotFound) || errors.As(err, &refusal) && refusal.Code == apierr.LockHeld {
		return LauncherAward{}, false, nil
	}
	if err != nil {
		return LauncherAward{}, false, err
	}
	unit, err := decodeAwardUnit(c.Unit, c.UnitID)
	if err != nil {
		return LauncherAward{}, false, err
	}
	fence := capability.LeaseRef{Kind: c.Unit, ID: c.UnitID, Holder: c.Binding.Holder, Generation: c.Binding.Generation}
	if c.Unit == capability.UnitEval {
		if c.Binding.Expansion == nil || c.Binding.Expansion.Kind != c.Unit || c.Binding.Expansion.ID != c.UnitID || c.Binding.Generation != unit.SourceGeneration {
			return LauncherAward{}, false, authorityError(apierr.IntegrityFailure, "invalid persisted eval fence")
		}
		fence = *c.Binding.Expansion
	}
	if row.team != c.Team || row.run != c.Binding.RunID || row.node != c.Binding.NodeID || row.holder != fence.Holder || row.generation != fence.Generation || row.prefix != prefix || !row.lease.Valid || row.lease.Int64 <= tx.now().UnixNano() || row.expiry.Valid && row.expiry.Int64 <= tx.now().UnixNano() {
		return LauncherAward{}, false, nil
	}
	run, err := readRunTx(ctx, tx, Team(c.Team), unit.RunID)
	if err != nil {
		return LauncherAward{}, false, err
	}
	award := LauncherAward{Capability: c, Run: run, ParentCapabilityExpiry: root.Capability.ExpiresAt, Claim: UnitClaim{Team: Team(c.Team), Kind: c.Unit, RunID: unit.RunID, NodeID: unit.NodeID, SourceGeneration: unit.SourceGeneration, Award: ClaimAward{CapabilityPrefix: prefix, Holder: fence.Holder, Generation: fence.Generation, LeaseExpiresAt: *stateTime(row.lease)}}}
	if c.Unit == capability.UnitPlan {
		award.Claim.Owner = OwnerLeaseRef{Kind: OwnerTrigger, RunID: unit.RunID, Holder: fence.Holder, Generation: fence.Generation}
	} else if c.Unit == capability.UnitEval {
		award.Claim.Owner = OwnerLeaseRef{Kind: OwnerEval, RunID: unit.RunID, SourceNodeID: unit.NodeID, SourceGeneration: unit.SourceGeneration, Holder: fence.Holder, Generation: fence.Generation}
	}
	if c.Unit == capability.UnitNode || c.Unit == capability.UnitEval {
		award.Node, err = readNodeTx(ctx, tx, Team(c.Team), controllerwire.NodeRef{RunID: unit.RunID, NodeID: unit.NodeID})
		if err != nil {
			return LauncherAward{}, false, err
		}
		if c.Unit == capability.UnitEval {
			live, err := launcherEvalLiveTx(ctx, tx, award)
			if err != nil || !live {
				return LauncherAward{}, false, err
			}
		}
	}
	return award, true, nil
}

func launcherEvalLiveTx(ctx context.Context, tx *storeTx, award LauncherAward) (bool, error) {
	if award.Node.Generation != award.Claim.SourceGeneration || award.Node.Status != StatusDone || !scheduleSucceeded(string(award.Node.Outcome)) || award.Node.Expansion != ExpansionExpected {
		return false, nil
	}
	var live int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM eval_units e JOIN leases l ON l.name=e.owner_lease_name WHERE e.team=? AND e.run_id=? AND e.source_node_id=? AND e.source_generation=? AND e.owner_lease_name='controller.owner' AND e.owner_generation=l.generation AND l.until>?`, award.Claim.Team, award.Claim.RunID, award.Claim.NodeID, award.Claim.SourceGeneration, tx.now().UnixNano()).Scan(&live)
	return live == 1, err
}

var errLauncherAuthorityChanged = errors.New("launcher authority changed during lock discovery")

func (s *Store) inLauncherBatchTx(ctx context.Context, authority capability.AuthorityRef, fences []capability.LeaseRef, observed bool, fn func(*storeTx) error) error {
	for range 5 {
		err := s.inTx(ctx, func(tx *storeTx) error {
			if err := lockClaimControllerTx(ctx, tx); err != nil {
				return err
			}
			if err := lockLauncherBatchTx(ctx, tx, authority, fences, observed); err != nil {
				return err
			}
			return fn(tx)
		})
		if !errors.Is(err, errLauncherAuthorityChanged) {
			return err
		}
	}
	return authorityError(apierr.Conflict, "launcher authority kept changing during lock discovery")
}

func lockLauncherBatchTx(ctx context.Context, tx *storeTx, authority capability.AuthorityRef, fences []capability.LeaseRef, observed bool) error {
	prefixes, err := launcherBatchPrefixesTx(ctx, tx, fences)
	if err != nil {
		return err
	}
	refs := make([]capability.AuthorityRef, 1, len(prefixes)+1)
	refs[0] = authority
	for _, prefix := range prefixes {
		refs = append(refs, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix})
	}
	var writes []string
	if observed {
		writes = prefixes
	}
	if err := lockAuthorityTx(ctx, tx, refs, writes...); err != nil {
		return err
	}
	var units []UnitRef
	for _, fence := range fences {
		if unit, err := decodeAwardUnit(fence.Kind, fence.ID); err == nil {
			units = append(units, unit)
		}
	}
	if err := lockUnitsTx(ctx, tx, units); err != nil {
		return err
	}
	current, err := launcherBatchPrefixesTx(ctx, tx, fences)
	if err != nil {
		return err
	}
	if !slices.Equal(prefixes, current) {
		return errLauncherAuthorityChanged
	}
	return nil
}

func launcherBatchPrefixesTx(ctx context.Context, tx *storeTx, fences []capability.LeaseRef) ([]string, error) {
	var prefixes []string
	for _, fence := range fences {
		prefix, err := launcherAwardPrefixTx(ctx, tx, fence)
		var refused *apierr.Error
		if errors.As(err, &refused) {
			continue
		}
		if err != nil {
			return nil, err
		}
		prefixes = append(prefixes, prefix)
		bootstrap, err := loadCapabilityTx(ctx, tx, prefix)
		if err != nil {
			return nil, err
		}
		unit, err := decodeAwardUnit(fence.Kind, fence.ID)
		if err != nil {
			return nil, err
		}
		query, args := "", []any{bootstrap.Team, unit.RunID}
		switch unit.Kind {
		case capability.UnitPlan:
			query = `SELECT capability_prefix FROM triggers WHERE team=? AND id=?`
		case capability.UnitNode:
			query, args = `SELECT capability_prefix FROM nodes WHERE team=? AND run_id=? AND node_id=?`, append(args, unit.NodeID)
		case capability.UnitEval:
			query, args = `SELECT capability_prefix FROM eval_units WHERE team=? AND run_id=? AND source_node_id=? AND source_generation=?`, append(args, unit.NodeID, unit.SourceGeneration)
		default:
			return nil, bindingRefusal()
		}
		err = tx.QueryRowContext(ctx, query, args...).Scan(&prefix)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if prefix != "" {
			prefixes = append(prefixes, prefix)
		}
	}
	slices.Sort(prefixes)
	return slices.Compact(prefixes), nil
}

func launcherAwardPrefixTx(ctx context.Context, tx *storeTx, lease capability.LeaseRef) (string, error) {
	var prefix string
	err := tx.QueryRowContext(ctx, "SELECT prefix FROM capabilities WHERE unit_kind=? AND unit_id=? AND bind_holder=? AND COALESCE(eval_unit_generation,bind_generation)=? AND issuance='award' AND delivery_purpose='bootstrap'", string(lease.Kind), lease.ID, lease.Holder, lease.Generation).Scan(&prefix)
	if errors.Is(err, sql.ErrNoRows) {
		return "", bindingRefusal()
	}
	return prefix, err
}

func launcherReservationAwardTx(ctx context.Context, tx *storeTx, root capability.AuthorityRef, subject capability.Subject, lease capability.LeaseRef) error {
	prefix, err := launcherAwardPrefixTx(ctx, tx, lease)
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
	if fence != lease || c.Parent == nil || *c.Parent != root || c.Subject != subject || c.Billing == nil || c.Delivery != capability.DeliveryBootstrap {
		return bindingRefusal()
	}
	return nil
}

func launcherExecutor(c capability.Capability) string {
	if capability.CloudLauncher(c) {
		return "cloud"
	}
	return "runner"
}
