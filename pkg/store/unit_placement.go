package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func unitPlacementTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef, claimant match.Profile, demand match.Demand) (placementDecision, error) {
	decision := placementDecision{reason: PlacementNone}
	if unit.Kind != capability.UnitNode || len(demand.Prefers) == 0 {
		return decision, nil
	}
	decision.reason = PlacementPreferred
	if match.Evaluate(claimant, demand).PreferenceScore > 0 {
		return decision, nil
	}
	decision.reason = PlacementFallback
	policy, _ := ClaimPlacementFromContext(ctx)
	if policy.Hold <= 0 {
		return decision, nil
	}
	var since sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT ready_at FROM nodes WHERE team=? AND run_id=? AND node_id=?`, team, unit.RunID, unit.NodeID).Scan(&since); err != nil {
		return decision, err
	}
	if !since.Valid || !tx.now().Before(time.Unix(0, since.Int64).Add(policy.Hold)) {
		return decision, nil
	}
	live, err := placementRunnersTx(ctx, tx, team, policy)
	if err != nil {
		return decision, err
	}
	for _, runner := range live {
		if runner.FreeSlots < 1 {
			continue
		}
		chain, err := authorityChainTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: runner.TokenPrefix}, false)
		if err == nil && (chain[0].principal.Capability.Team != string(team) || !registeredClaimant(chain[0].principal.Capability)) {
			continue
		}
		if err == nil {
			_, err = claimKinds(chain[0].principal.Capability, UnitDemand{Target: &unit})
		}
		var profile match.Profile
		var required match.Demand
		if err == nil {
			profile, required, err = readMatchingInputsTx(ctx, tx, UnitDemand{CapabilityPrefix: runner.TokenPrefix, Labels: runner.Labels, OS: runner.Profile.OS, Arch: runner.Profile.Arch, Capacity: runner.Profile.Capacity, Available: runner.Available}, unit, false)
		}
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
		if verdict := match.Evaluate(profile, required); verdict.OK() && verdict.PreferenceScore > 0 && match.Fits(profile.Capacity, runner.Available, required.Request).OK() {
			decision.hold = true
			break
		}
	}
	return decision, nil
}

func placementRunnersTx(ctx context.Context, tx *storeTx, team Team, policy ClaimPlacement) ([]RunnerPresence, error) {
	live := append([]RunnerPresence{}, policy.Live...)
	if policy.Liveness > 0 {
		rows, err := tx.QueryContext(ctx, `SELECT prefix,claim_observation_json FROM capabilities WHERE team=? AND last_used_at>=? AND claim_observation_json IS NOT NULL`, team, tx.now().Add(-policy.Liveness).UnixNano())
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var prefix string
			var body []byte
			var observation UnitDemand
			if err := rows.Scan(&prefix, &body); err != nil {
				return nil, errors.Join(err, rows.Close())
			}
			if err := json.Unmarshal(body, &observation); err != nil {
				return nil, errors.Join(err, rows.Close())
			}
			live = append(live, RunnerPresence{TokenPrefix: prefix, Labels: observation.Labels, Profile: match.Profile{OS: observation.OS, Arch: observation.Arch, Capacity: observation.Capacity}, Available: observation.Available, FreeSlots: 1})
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
	}
	return live, nil
}
