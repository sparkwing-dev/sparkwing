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
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// LauncherAward contains persisted execution inputs without reusable bearer material.
type LauncherAward struct {
	// Gone confirms that the observed unit fence has ended or belongs to another launcher.
	Gone                   bool
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
func (o *Operator) ReconcileLauncherAwards(ctx context.Context, authority capability.AuthorityRef, observed, reservations, preparing, rejected []capability.LeaseRef) ([]LauncherAward, []LauncherPreparation, error) {
	if err := validateLauncherPreparationFences(preparing); err != nil {
		return nil, nil, err
	}
	selected := append(append(append(slices.Clone(observed), reservations...), preparing...), rejected...)
	owner, err := o.s.launcherAwardOwnership(ctx, authority, observed, selected)
	if err != nil {
		return nil, nil, err
	}
	var prepared []LauncherPreparation
	for _, fence := range preparing {
		if err := owner.apply(ctx, fence, func() error {
			rows, err := o.s.renewLauncherPreparation(ctx, authority, []capability.LeaseRef{fence})
			prepared = append(prepared, rows...)
			return err
		}); err != nil {
			return nil, nil, err
		}
	}
	for _, fence := range rejected {
		if err := owner.apply(ctx, fence, func() error { return o.s.rejectLauncherAwards(ctx, authority, []capability.LeaseRef{fence}) }); err != nil {
			return nil, nil, err
		}
	}
	tx, err := o.s.beginSnapshotReadTx(ctx)
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
	prefixes, err := tx.operator.launcherKeysTx(ctx, tx, root.Authority, claimant.Subject, observed, selected)
	if err != nil {
		return nil, nil, err
	}
	out := []LauncherAward{}
	for _, key := range prefixes {
		if owner.failed[key.fence] != nil {
			continue
		}
		owner.owned[key.fence] = true
		if !key.live {
			if observed != nil {
				out = append(out, LauncherAward{Fence: key.fence, Gone: true})
			}
			continue
		}
		if err := owner.apply(ctx, key.fence, func() error {
			award, live, err := readLauncherAwardTx(ctx, tx, key.prefix, claimant, root)
			if err == nil && !live && observed != nil {
				out = append(out, LauncherAward{Fence: key.fence, Gone: true})
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

// Unit rows retain the observed fence when capability lookup projections are damaged.
func (s *Store) launcherAwardOwnership(ctx context.Context, authority capability.AuthorityRef, observed, selected []capability.LeaseRef) (*launcherAwardResults, error) {
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
	keys, err := tx.operator.launcherKeysTx(ctx, tx, root, claimant.Subject, observed, selected)
	if err != nil {
		return nil, err
	}
	owner := &launcherAwardResults{failed: map[capability.LeaseRef]error{}, owned: map[capability.LeaseRef]bool{}}
	for _, key := range keys {
		owner.owned[key.fence] = true
		if key.live {
			if err := owner.apply(ctx, key.fence, func() error {
				_, _, err := readLauncherAwardTx(ctx, tx, key.prefix, claimant, chain[len(chain)-1].principal)
				return err
			}); err != nil {
				return nil, err
			}
		}
	}
	return owner, tx.Commit()
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
	if err == nil || !r.owned[fence] || !isIntegrityError(err) {
		return err
	}
	slog.ErrorContext(ctx, "launcher award deferred", "award", fence, "err", err)
	r.failed[fence] = err
	return nil
}

type launcherAwardKey struct {
	live   bool
	prefix string
	fence  capability.LeaseRef
}

func (op *Operator) launcherKeysTx(ctx context.Context, tx *storeTx, root capability.AuthorityRef, subject capability.Subject, observed, selected []capability.LeaseRef) ([]launcherAwardKey, error) {
	if observed == nil {
		return op.launcherPrefixesTx(ctx, tx, root, subject, selected)
	}
	var keys []launcherAwardKey
	seen := map[capability.LeaseRef]bool{}
	for _, fence := range selected {
		if seen[fence] {
			continue
		}
		seen[fence] = true
		unit, err := decodeAwardUnit(fence.Kind, fence.ID)
		if err != nil {
			if slices.Contains(selected[len(observed):], fence) {
				return nil, bindingRefusal()
			}
			keys = append(keys, launcherAwardKey{fence: fence})
			continue
		}
		var prefix, holder string
		var generation int64
		var live bool
		var query string
		args := []any{unit.RunID}
		switch fence.Kind {
		case capability.UnitPlan:
			query = `SELECT capability_prefix,COALESCE(claimed_by,''),generation,status='claimed' AND finished_at IS NULL FROM triggers WHERE id=? AND unit_kind='plan'`
		case capability.UnitNode:
			query = `SELECT capability_prefix,COALESCE(claimed_by,''),generation,status<>'done' FROM nodes WHERE run_id=? AND node_id=?`
			args = append(args, unit.NodeID)
		case capability.UnitEval:
			query = `SELECT capability_prefix,COALESCE(claimed_by,''),generation,status IN ('claimed','running') FROM eval_units WHERE run_id=? AND source_node_id=? AND source_generation=?`
			args = append(args, unit.NodeID, unit.SourceGeneration)
		default:
			return nil, bindingRefusal()
		}
		err = tx.QueryRowContext(ctx, query, args...).Scan(&prefix, &holder, &generation, &live)
		key := launcherAwardKey{fence: fence}
		if errors.Is(err, sql.ErrNoRows) || err == nil && (!live || holder != fence.Holder || generation != fence.Generation) {
			keys = append(keys, key)
			continue
		}
		if err != nil {
			return nil, err
		}
		var parentKind, parentID, subjectKind, subjectID string
		var body []byte
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(parent_kind,''),COALESCE(parent_id,''),subject_kind,subject_id,authority_json FROM capabilities WHERE prefix=?`, prefix).Scan(&parentKind, &parentID, &subjectKind, &subjectID, &body)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		owned := parentKind == string(root.Kind) && parentID == root.ID && subjectKind == string(subject.Kind) && subjectID == subject.ID
		var record CapabilityRecord
		if json.Unmarshal(body, &record) == nil && record.Validate() == nil {
			c := record.Value
			owned = owned || c.Parent != nil && *c.Parent == root && c.Subject == subject
		}
		if !owned && !errors.Is(err, sql.ErrNoRows) {
			if slices.Contains(selected[len(observed):], fence) {
				return nil, bindingRefusal()
			}
			if _, err := loadCapabilityTx(ctx, tx, prefix); err == nil {
				keys = append(keys, key)
				continue
			} else if !isIntegrityError(err) {
				return nil, err
			}
		}
		key.prefix, key.live = prefix, true
		keys = append(keys, key)
	}
	return keys, nil
}

func (op *Operator) launcherPrefixesTx(ctx context.Context, tx *storeTx, root capability.AuthorityRef, subject capability.Subject, selected []capability.LeaseRef) ([]launcherAwardKey, error) {
	query := `SELECT * FROM (SELECT c.prefix,c.unit_kind,c.unit_id,c.bind_holder,COALESCE(c.eval_unit_generation,c.bind_generation) AS generation,
` + liveCredentialSQL("c.") + ` AND (
EXISTS (SELECT 1 FROM nodes n WHERE n.team=c.team AND n.run_id=c.bind_run AND n.node_id=c.bind_node AND n.capability_prefix=c.prefix AND n.status<>'done' AND n.claimed_by IS NOT NULL)
OR EXISTS (SELECT 1 FROM triggers t WHERE t.team=c.team AND t.id=c.bind_run AND t.capability_prefix=c.prefix AND t.status='claimed' AND t.finished_at IS NULL)
OR EXISTS (SELECT 1 FROM eval_units e WHERE e.team=c.team AND e.run_id=c.bind_run AND e.source_node_id=c.bind_node AND e.source_generation=c.bind_generation AND e.capability_prefix=c.prefix AND e.status IN ('claimed','running'))) AS live
FROM capabilities c WHERE c.parent_kind=? AND c.parent_id=? AND c.subject_kind=? AND c.subject_id=? AND c.audience='controller' AND c.issuance IN ('award','exchange') AND c.unit_kind IN ('plan','eval','node')) a WHERE a.live`
	clockTime1, clockErr1 := tx.now(ctx)
	if clockErr1 != nil {
		return nil, clockErr1
	}
	args := []any{clockTime1.UnixNano(), clockTime1.UnixNano(), root.Kind, root.ID, subject.Kind, subject.ID}
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
	var missing *apierr.Error
	if errors.As(err, &missing) && missing.Code == apierr.Unauthenticated {
		return LauncherAward{}, false, authorityError(apierr.IntegrityFailure, "live launcher unit has no capability")
	}
	if err != nil {
		return LauncherAward{}, false, err
	}
	if c.Parent == nil || *c.Parent != root.Authority || c.Subject != claimant.Subject || c.Audience != capability.Controller || !slices.Contains([]capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode}, c.Unit) || c.Purpose != claimPurpose(c.Unit) || c.Billing == nil || c.Billing.ExecutorClass != launcherExecutor(claimant) || c.Delivery != capability.DeliveryBootstrap && c.Issuance != capability.Exchange {
		return LauncherAward{}, false, authorityError(apierr.IntegrityFailure, "invalid persisted launcher award")
	}
	clockTime2, clockErr2 := tx.now(ctx)
	if clockErr2 != nil {
		return LauncherAward{}, false, clockErr2
	}
	if c.RevokedAt != nil && !c.RevokedAt.After(clockTime2) || c.ExpiresAt != nil && !c.ExpiresAt.After(clockTime2) {
		return LauncherAward{}, false, nil
	}
	row, err := tx.operator.loadAwardRowTx(ctx, tx, capability.AwardSpec{Unit: c.Unit, UnitID: c.UnitID})
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
	fence, err := launcherFence(c)
	if err != nil {
		return LauncherAward{}, false, err
	}
	clockTime3, clockErr3 := tx.now(ctx)
	if clockErr3 != nil {
		return LauncherAward{}, false, clockErr3
	}
	if row.team != c.Team || row.run != c.Binding.RunID || row.node != c.Binding.NodeID || row.holder != fence.Holder || row.generation != fence.Generation || row.prefix != prefix || !row.lease.Valid || row.lease.Int64 <= clockTime3.UnixNano() || row.expiry.Valid && row.expiry.Int64 <= clockTime3.UnixNano() {
		return LauncherAward{}, false, nil
	}
	run, err := readRunTx(ctx, tx, Team(c.Team), string(unit.RunID))
	if errors.Is(err, ErrNotFound) {
		err = authorityError(apierr.IntegrityFailure, "launcher award has no run")
	}
	if err != nil {
		return LauncherAward{}, false, err
	}
	award := LauncherAward{Capability: c, Run: run, ParentCapabilityExpiry: root.Capability.ExpiresAt, Claim: UnitClaim{Team: Team(c.Team), Kind: c.Unit, RunID: string(unit.RunID), NodeID: string(unit.NodeID), SourceGeneration: unit.SourceGeneration, Award: ClaimAward{CapabilityPrefix: prefix, Holder: fence.Holder, Generation: runspec.ClaimGeneration(fence.Generation), LeaseExpiresAt: *stateTime(row.lease)}}}
	if c.Unit == capability.UnitPlan {
		award.Claim.Owner = OwnerLeaseRef{Kind: OwnerTrigger, EvalFence: runspec.EvalFence{RunID: unit.RunID, Holder: fence.Holder, Generation: runspec.ClaimGeneration(fence.Generation)}}
	} else if c.Unit == capability.UnitEval {
		award.Claim.Owner = OwnerLeaseRef{Kind: OwnerEval, EvalFence: runspec.EvalFence{RunID: unit.RunID, SourceNodeID: unit.NodeID, SourceGeneration: runspec.ClaimGeneration(unit.SourceGeneration), Holder: fence.Holder, Generation: runspec.ClaimGeneration(fence.Generation)}}
	}
	if c.Unit == capability.UnitNode || c.Unit == capability.UnitEval {
		award.Node, err = readNodeTx(ctx, tx, Team(c.Team), controllerwire.NodeRef{RunID: unit.RunID, NodeID: unit.NodeID})
		if errors.Is(err, ErrNotFound) {
			err = authorityError(apierr.IntegrityFailure, "launcher award has no job")
		}
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
	award.CloudResources, err = cloudAwardResourcesTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix})
	return award, err == nil, err
}

func launcherEvalLiveTx(ctx context.Context, tx *storeTx, award LauncherAward) (bool, error) {
	if award.Node.Generation != runspec.ClaimGeneration(award.Claim.SourceGeneration) || award.Node.Status != StatusDone || !scheduleSucceeded(string(award.Node.Outcome)) || award.Node.Expansion != ExpansionExpected {
		return false, nil
	}
	var live int
	clockTime4, clockErr4 := tx.now(ctx)
	if clockErr4 != nil {
		return false, clockErr4
	}
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM eval_units e JOIN leases l ON l.name=e.owner_lease_name WHERE e.team=? AND e.run_id=? AND e.source_node_id=? AND e.source_generation=? AND e.owner_lease_name='controller.owner' AND e.owner_generation=l.generation AND l.until>?`, award.Claim.Team, award.Claim.RunID, award.Claim.NodeID, award.Claim.SourceGeneration, clockTime4.UnixNano()).Scan(&live)
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
	if err := tx.operator.lockAuthorityTx(ctx, tx, refs, writes...); err != nil {
		return err
	}
	var units []UnitRef
	for _, fence := range fences {
		if unit, err := decodeAwardUnit(fence.Kind, fence.ID); err == nil {
			units = append(units, unit)
		}
	}
	if err := tx.operator.lockUnitsTx(ctx, tx, units); err != nil {
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
		prefix, err := tx.operator.launcherAwardPrefixTx(ctx, tx, fence)
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

func (op *Operator) launcherAwardPrefixTx(ctx context.Context, tx *storeTx, lease capability.LeaseRef) (string, error) {
	var prefix string
	err := tx.QueryRowContext(ctx, "SELECT prefix FROM capabilities WHERE unit_kind=? AND unit_id=? AND bind_holder=? AND COALESCE(eval_unit_generation,bind_generation)=? AND issuance='award' AND delivery_purpose='bootstrap'", string(lease.Kind), lease.ID, lease.Holder, lease.Generation).Scan(&prefix)
	if errors.Is(err, sql.ErrNoRows) {
		return "", bindingRefusal()
	}
	return prefix, err
}

func launcherReservationAwardTx(ctx context.Context, tx *storeTx, root capability.AuthorityRef, subject capability.Subject, lease capability.LeaseRef) error {
	prefix, err := tx.operator.launcherAwardPrefixTx(ctx, tx, lease)
	if err != nil {
		return err
	}
	c, err := loadCapabilityTx(ctx, tx, prefix)
	if err != nil {
		return err
	}
	fence, err := launcherFence(c)
	if err != nil {
		return err
	}
	if fence != lease || c.Parent == nil || *c.Parent != root || c.Subject != subject || c.Billing == nil || c.Delivery != capability.DeliveryBootstrap {
		return bindingRefusal()
	}
	return nil
}

func launcherExecutor(c capability.Capability) string {
	if capability.CloudLauncher(c) {
		return TierCloud
	}
	return "runner"
}

func launcherFence(c capability.Capability) (capability.LeaseRef, error) {
	fence := capability.LeaseRef{Kind: c.Unit, ID: c.UnitID, Holder: c.Binding.Holder, Generation: c.Binding.Generation}
	if c.Unit == capability.UnitEval {
		unit, err := decodeAwardUnit(c.Unit, c.UnitID)
		if err != nil {
			return capability.LeaseRef{}, err
		}
		if c.Binding.Expansion == nil || c.Binding.Expansion.Kind != c.Unit || c.Binding.Expansion.ID != c.UnitID || c.Binding.Generation != unit.SourceGeneration {
			return capability.LeaseRef{}, authorityError(apierr.IntegrityFailure, "invalid persisted eval fence")
		}
		fence = *c.Binding.Expansion
	}
	if fence.Holder == "" || fence.Generation <= 0 {
		return capability.LeaseRef{}, authorityError(apierr.IntegrityFailure, "invalid persisted launcher fence")
	}
	return fence, nil
}
