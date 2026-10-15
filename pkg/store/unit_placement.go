package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func placementRank(profile match.Profile, demand match.Demand, preferences []PlacementPreference) int {
	for i, preference := range preferences {
		demand.Prefers = preference.Prefers
		verdict := match.Evaluate(profile, demand)
		if verdict.OK() && (len(preference.Prefers) == 0 || verdict.PreferenceScore > 0) {
			return i
		}
	}
	return len(preferences)
}

func unitPlacementTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef, claimant match.Profile, demand match.Demand) (placementDecision, error) {
	decision := placementDecision{reason: PlacementNone}
	if unit.Kind != capability.UnitNode {
		return decision, nil
	}
	policy, _ := ClaimPlacementFromContext(ctx)
	preferences := policy.preferencesFor(demand.Prefers)
	if len(preferences) == 0 || len(preferences[0].Prefers) == 0 {
		return decision, nil
	}
	rank := placementRank(claimant, demand, preferences)
	decision.reason = PlacementFallback
	if rank == 0 && len(preferences[0].Prefers) != 0 {
		decision.reason = PlacementPreferred
	}
	var since sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT ready_at FROM nodes WHERE team=? AND run_id=? AND node_id=?`, team, unit.RunID, unit.NodeID).Scan(&since); err != nil {
		return decision, err
	}
	if !since.Valid {
		return decision, nil
	}
	deadline := time.Unix(0, since.Int64)
	held := make([]bool, len(preferences))
	for i, preference := range preferences {
		if preference.Hold < 0 {
			return decision, authorityError(apierr.BadRequest, "placement hold must not be negative")
		}
		deadline = deadline.Add(preference.Hold)
		held[i] = i < rank && tx.now().Before(deadline)
	}
	if !slices.Contains(held, true) {
		return decision, nil
	}
	live, err := placementRunnersTx(ctx, tx, team, policy)
	if err != nil {
		return decision, err
	}
	for _, runner := range live {
		profile, required, err := placementRunnerTx(ctx, tx, team, unit, runner)
		if err != nil {
			var denied *apierr.Error
			if errors.As(err, &denied) && (denied.Code == apierr.Unauthenticated || denied.Code == apierr.Forbidden) {
				continue
			}
			return decision, err
		}
		if profile.Capacity.Cores == 0 && profile.Capacity.MemoryBytes == 0 {
			continue
		}
		earlier := placementRank(profile, required, preferences)
		if earlier < len(held) && held[earlier] && match.Fits(profile.Capacity, runner.Available, required.Request).OK() {
			decision.hold = true
			break
		}
	}
	return decision, nil
}

func placementRunnerTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef, runner UnitDemand) (match.Profile, match.Demand, error) {
	chain, err := authorityChainTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: runner.CapabilityPrefix}, false)
	if err != nil {
		return match.Profile{}, match.Demand{}, err
	}
	c := chain[0].principal.Capability
	if c.Team != string(team) || !registeredClaimant(c) {
		return match.Profile{}, match.Demand{}, bindingRefusal()
	}
	if _, err := claimKinds(c, UnitDemand{Target: &unit}); err != nil {
		return match.Profile{}, match.Demand{}, err
	}
	return readMatchingInputsTx(ctx, tx, runner, unit, false)
}

func placementRunnersTx(ctx context.Context, tx *storeTx, team Team, policy ClaimPlacement) ([]UnitDemand, error) {
	var live []UnitDemand
	if policy.Liveness > 0 {
		rows, err := tx.QueryContext(ctx, `SELECT prefix,claim_observation_json FROM capabilities WHERE team=? AND last_used_at>=? AND claim_observation_json IS NOT NULL`, team, tx.now().Add(-policy.Liveness).UnixNano())
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var prefix string
			var body []byte
			var observation runnerPoll
			if err := rows.Scan(&prefix, &body); err != nil {
				return nil, errors.Join(err, rows.Close())
			}
			if err := json.Unmarshal(body, &observation); err != nil {
				return nil, errors.Join(err, rows.Close())
			}
			if observation.ObservedAt.Before(tx.now().Add(-policy.Liveness)) || observation.ObservedAt.After(tx.now()) {
				continue
			}
			observation.CapabilityPrefix = prefix
			live = append(live, observation.UnitDemand)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
	}
	return live, nil
}
