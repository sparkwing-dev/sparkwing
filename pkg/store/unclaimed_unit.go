package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// NoCreditsHold is the credit hold for a team with neither a balance nor a payment method covering Cloud work.
const NoCreditsHold = "team has no remaining credits; add credits or a payment method"

// UnclaimedUnit is a live run's longest-waiting unit that no claimant holds.
type UnclaimedUnit struct {
	Kind   capability.UnitKind
	NodeID string
	Since  time.Time
	// CreditHold says why the team's credit standing keeps the launcher from claiming the unit; empty when it does not.
	CreditHold string
	// NoEligible says why no claimant can take a run-anywhere unit; empty when one can.
	NoEligible string
}

func readUnclaimedUnitTx(ctx context.Context, tx *storeTx, team Team, run RunRow) (*UnclaimedUnit, error) {
	if run.FinishedAt != nil || run.CancelRequestedAt != nil || run.Request.Request.Mode != runspec.IntakeExecute {
		return nil, nil
	}
	query := `SELECT unit_kind AS kind,'' AS node,available_at AS since FROM triggers WHERE team=? AND id=? AND status='pending' AND claimed_by IS NULL AND cancel_requested_at IS NULL`
	args := []any{team, run.ID}
	if run.Dispatch == string(runspec.DispatchController) {
		query += ` UNION ALL SELECT 'node',node_id,ready_at FROM nodes WHERE team=? AND run_id=? AND ready_at IS NOT NULL AND claimed_by IS NULL AND status='pending' AND cancel_requested_at IS NULL
 UNION ALL SELECT 'eval',e.source_node_id,n.finished_at FROM eval_units e JOIN nodes n ON n.team=e.team AND n.run_id=e.run_id AND n.node_id=e.source_node_id
 WHERE e.team=? AND e.run_id=? AND e.status='pending' AND e.claimed_by IS NULL AND n.finished_at IS NOT NULL`
		args = append(args, team, run.ID, team, run.ID)
	}
	rows, err := tx.QueryContext(ctx, `SELECT kind,node,since FROM (`+query+`) AS waiting ORDER BY since LIMIT 1`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var unit UnclaimedUnit
	var kind string
	var since int64
	if err := rows.Scan(&kind, &unit.NodeID, &since); err != nil {
		return nil, err
	}
	unit.Kind, unit.Since = capability.UnitKind(kind), time.Unix(0, since).UTC()
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	if run.Dispatch == string(runspec.DispatchController) {
		ref := UnitRef{Kind: unit.Kind, RunID: run.ID, NodeID: unit.NodeID}
		survey, err := surveyClaimantsTx(ctx, tx, team, ref)
		if err != nil {
			return nil, err
		}
		if !survey.unmetered {
			if unit.CreditHold, err = unitCreditHoldTx(ctx, tx, team, run, ref); err != nil || unit.CreditHold != "" {
				return &unit, err
			}
		}
		if runAnywhere(run.Request.Request) && !survey.online {
			unit.NoEligible, err = noEligibleCauseTx(ctx, tx, team, run, ref, survey)
		}
		return &unit, err
	}
	return &unit, nil
}

type claimantSurvey struct {
	unmetered, online bool
	dropped           []string
}

// safety: notices judge the same claimant credentials, matching and ranking checks that claims use.
func surveyClaimantsTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef) (claimantSurvey, error) {
	var out claimantSurvey
	now := tx.now()
	rows, err := tx.QueryContext(ctx, `SELECT prefix,COALESCE(last_used_at,0),claim_observation_json FROM capabilities
 WHERE (team=? AND subject_kind='machine' AND bind_kind IN ('none','push') OR team IS NULL AND subject_kind='service')
 AND parent_id IS NULL AND audience='controller' AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>?) ORDER BY prefix`, team, now.UnixNano())
	if err != nil {
		return out, err
	}
	type candidate struct {
		prefix string
		used   int64
		poll   runnerPoll
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		var body []byte
		if err := rows.Scan(&c.prefix, &c.used, &body); err != nil {
			return out, errors.Join(err, rows.Close())
		}
		if len(body) > 0 {
			if err := json.Unmarshal(body, &c.poll); err != nil {
				return out, errors.Join(err, rows.Close())
			}
		}
		candidates = append(candidates, c)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return out, err
	}
	for _, candidate := range candidates {
		chain, err := authorityChainTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: candidate.prefix}, false)
		if unavailableRunner(err) {
			continue
		}
		if err != nil {
			return out, err
		}
		c := chain[0].principal.Capability
		if !registeredClaimant(c) && !actionsClaimant(c) && !launcherClaimant(c) {
			continue
		}
		if _, err := claimKinds(c, UnitDemand{Target: &unit}); err != nil {
			continue
		}
		demand := candidate.poll.UnitDemand
		demand.CapabilityPrefix = candidate.prefix
		profile, required, err := readMatchingInputsTx(ctx, tx, demand, unit, false)
		var denied *apierr.Error
		dropped := errors.Is(err, errTierOutsideRanking)
		if !dropped && errors.As(err, &denied) && denied.Code == apierr.Forbidden {
			continue
		}
		if err != nil && !dropped {
			return out, err
		}
		if !match.Evaluate(profile, required).OK() {
			continue
		}
		run, err := sealedRequestTx(ctx, tx, team, unit.RunID)
		if err != nil {
			return out, err
		}
		if dropped {
			tier, err := claimTierTx(ctx, tx, chain, run.Request)
			if err != nil {
				return out, err
			}
			if !slices.Contains(out.dropped, tier) {
				out.dropped = append(out.dropped, tier)
			}
			continue
		}
		out.unmetered = out.unmetered || !meteredPlacement(c, chain[len(chain)-1].principal.Capability)
		out.online = out.online || candidate.used >= now.Add(-time.Minute).UnixNano()
	}
	return out, nil
}

var tierNames = map[string]string{TierHere: "The submitting machine", TierPersonal: "Personal machines", TierActions: "GitHub Actions runners", TierSelfHosted: "Self-hosted runners", TierCloud: "Cloud"}

func noEligibleCauseTx(ctx context.Context, tx *storeTx, team Team, run RunRow, unit UnitRef, survey claimantSurvey) (string, error) {
	if len(survey.dropped) > 0 {
		var names []string
		for _, tier := range DefaultClaimRanking {
			if slices.Contains(survey.dropped, tier) {
				names = append(names, tierNames[tier])
			}
		}
		list, verb := names[len(names)-1], " is"
		if len(names) > 1 {
			list, verb = strings.Join(names[:len(names)-1], ", ")+" and "+list, " are"
		} else if strings.HasSuffix(list, "s") {
			verb = " are"
		}
		return list + verb + " off in this team's runner ranking", nil
	}
	terms := slices.Clone(run.Request.Request.Options.Requires.Value)
	what := "this run"
	if unit.Kind == capability.UnitNode {
		var body []byte
		if err := tx.QueryRowContext(ctx, `SELECT execution_json FROM nodes WHERE team=? AND run_id=? AND node_id=?`, team, unit.RunID, unit.NodeID).Scan(&body); err != nil {
			return "", err
		}
		var execution runspec.JobExecutionSettings
		if err := json.Unmarshal(body, &execution); err != nil {
			return "", err
		}
		terms, what = execution.Requires.Value, "this job"
	}
	if pinned := slices.DeleteFunc(slices.Clone(terms), func(term string) bool { return !pinnedTerm(term) }); len(pinned) > 0 {
		return what + " targets " + strings.Join(pinned, " ") + " only", nil
	}
	ranking, err := claimRankingTx(ctx, tx, team)
	if err != nil {
		return "", err
	}
	allowed := "any tier"
	if len(ranking) > 0 {
		allowed = strings.Join(ranking, ", ")
	}
	return "no runner of the allowed tiers (" + allowed + ") is online", nil
}

func unitCreditHoldTx(ctx context.Context, tx *storeTx, team Team, run RunRow, unit UnitRef) (string, error) {
	resources, err := unitResourcesTx(ctx, tx, team, run, unit)
	if err != nil {
		return "", err
	}
	price, err := unitBillingPriceTx(ctx, tx, matchingResources(resources))
	var unpriced *UnpricedCPUClassError
	if errors.As(err, &unpriced) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	err = unitCreditRefusalTx(ctx, tx, team, unit, price)
	var short *InsufficientCreditsError
	if !errors.As(err, &short) {
		return "", err
	}
	if !short.Frozen && (short.Limit == "" || short.Limit == SpendLimitBalance) {
		return NoCreditsHold, nil
	}
	return short.Error(), nil
}
