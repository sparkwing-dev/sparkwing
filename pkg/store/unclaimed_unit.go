package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// UnclaimedUnit is a live run's longest-waiting unit that no claimant holds.
type UnclaimedUnit struct {
	Kind             capability.UnitKind
	NodeID           string
	SourceGeneration int64
	Since            time.Time
	Needs            []string
}

func readUnclaimedUnitTx(ctx context.Context, tx *storeTx, team Team, run RunRow) (*UnclaimedUnit, error) {
	if run.FinishedAt != nil || run.CancelRequestedAt != nil || run.Request.Request.Mode != runspec.IntakeExecute {
		return nil, nil
	}
	var oldest *UnclaimedUnit
	keep := func(unit UnclaimedUnit) {
		if oldest == nil || unit.Since.Before(oldest.Since) {
			oldest = &unit
		}
	}
	var kind string
	var since int64
	var needs []byte
	err := tx.QueryRowContext(ctx, `SELECT unit_kind,available_at,needs_labels FROM triggers WHERE team=? AND id=? AND status='pending' AND claimed_by IS NULL AND cancel_requested_at IS NULL`, team, run.ID).Scan(&kind, &since, &needs)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		unit := UnclaimedUnit{Kind: capability.UnitKind(kind), Since: time.Unix(0, since).UTC()}
		if err := json.Unmarshal(needs, &unit.Needs); err != nil {
			return nil, err
		}
		keep(unit)
	}
	if run.Dispatch != string(runspec.DispatchController) {
		return oldest, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT 'node',node_id,0,ready_at,execution_json FROM nodes WHERE team=? AND run_id=? AND ready_at IS NOT NULL AND claimed_by IS NULL AND status='pending' AND cancel_requested_at IS NULL
 UNION ALL SELECT 'eval',e.source_node_id,e.source_generation,n.finished_at,n.execution_json FROM eval_units e JOIN nodes n ON n.team=e.team AND n.run_id=e.run_id AND n.node_id=e.source_node_id
 WHERE e.team=? AND e.run_id=? AND e.status='pending' AND e.claimed_by IS NULL AND n.finished_at IS NOT NULL`, team, run.ID, team, run.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var unit UnclaimedUnit
		var execution []byte
		if err := rows.Scan(&kind, &unit.NodeID, &unit.SourceGeneration, &since, &execution); err != nil {
			return nil, err
		}
		var settings runspec.JobExecutionSettings
		if err := json.Unmarshal(execution, &settings); err != nil {
			return nil, err
		}
		unit.Kind, unit.Since, unit.Needs = capability.UnitKind(kind), time.Unix(0, since).UTC(), settings.Requires.Value
		keep(unit)
	}
	return oldest, rows.Err()
}

// UnitHasEligibleClaimant applies the claim selection predicates (claimant class and grants,
// bindings and repository or push scope, sealed demand and capacity) to every claimant whose
// last claim poll falls within window, and reports whether any of them would be awarded unit.
func (t *Tenant) UnitHasEligibleClaimant(ctx context.Context, runID string, unit UnclaimedUnit, window time.Duration) (bool, error) {
	tx, err := t.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackOrLog(tx)
	ref := UnitRef{Kind: unit.Kind, RunID: runID}
	if unit.Kind == capability.UnitNode || unit.Kind == capability.UnitEval {
		ref.NodeID, ref.SourceGeneration = unit.NodeID, unit.SourceGeneration
	}
	now := tx.now()
	rows, err := tx.QueryContext(ctx, `SELECT prefix,claim_observation_json FROM capabilities
 WHERE (team=? OR COALESCE(team,'')='') AND audience='controller' AND bind_kind IN ('none','push') AND claim_observation_json IS NOT NULL
 AND (revoked_at IS NULL OR revoked_at>?) AND (expires_at IS NULL OR expires_at>?) ORDER BY prefix`, t.team, now.UnixNano(), now.UnixNano())
	if err != nil {
		return false, err
	}
	type observed struct {
		prefix string
		poll   runnerPoll
	}
	var candidates []observed
	for rows.Next() {
		var candidate observed
		var body []byte
		if err := rows.Scan(&candidate.prefix, &body); err != nil {
			_ = rows.Close()
			return false, err
		}
		if json.Unmarshal(body, &candidate.poll) == nil && !candidate.poll.ObservedAt.Before(now.Add(-window)) {
			candidates = append(candidates, candidate)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return false, err
	}
	for _, candidate := range candidates {
		p, err := tx.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: candidate.prefix})
		if err != nil || !observedClaimant(p.Capability) {
			continue
		}
		if _, err := claimKinds(p.Capability, UnitDemand{Target: &ref}); err != nil {
			continue
		}
		demand := candidate.poll.UnitDemand
		demand.CapabilityPrefix, demand.Target = candidate.prefix, &ref
		profile, requirements, err := readMatchingInputsTx(ctx, tx, demand, ref, false)
		if err != nil {
			continue
		}
		if match.Evaluate(profile, requirements).OK() && match.Fits(profile.Capacity, profile.Capacity, requirements.Request).OK() {
			return true, nil
		}
	}
	return false, nil
}
