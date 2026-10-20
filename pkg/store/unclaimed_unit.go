package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// UnclaimedUnit is a live run's longest-waiting unit that no claimant holds.
type UnclaimedUnit struct {
	Kind   capability.UnitKind
	NodeID string
	Since  time.Time
	// CreditHold names the balance shortfall or hold that keeps the launcher from claiming the unit, and its remedy;
	// empty when nothing does.
	CreditHold string
	// NoEligible says why no claimant can take a run-anywhere unit; empty when one can.
	NoEligible string
}

func readUnclaimedUnitTx(ctx context.Context, tx *storeTx, team Team, run RunRow) (*UnclaimedUnit, error) {
	if run.FinishedAt != nil || run.CancelRequestedAt != nil || run.Request.Request.Mode != runspec.IntakeExecute {
		return nil, nil
	}
	controller := run.Dispatch == string(runspec.DispatchController) || run.Owner != nil && run.Owner.Kind == OwnerController
	query := `SELECT unit_kind AS kind,'' AS node,available_at AS since FROM triggers WHERE team=? AND id=? AND status='pending' AND claimed_by IS NULL AND cancel_requested_at IS NULL`
	args := []any{team, run.ID}
	if controller {
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
	if controller {
		ref := UnitRef{Kind: unit.Kind, RunID: run.ID, NodeID: unit.NodeID}
		standings, err := unitStandingsTx(ctx, tx, team, ref)
		if err != nil || slices.ContainsFunc(standings, func(e eligibility) bool { return e.ok() && !e.unobserved }) {
			return &unit, err
		}
		if slices.ContainsFunc(standings, func(e eligibility) bool { return e.failed == stepBilling }) {
			if unit.CreditHold, err = unitCreditHoldTx(ctx, tx, team, run, ref); err != nil || unit.CreditHold != "" {
				return &unit, err
			}
		}
		if runAnywhere(run.Request.Request) {
			unit.NoEligible, err = noEligibleCauseTx(ctx, tx, team, run, ref, standings)
		}
		return &unit, err
	}
	return &unit, nil
}

// safety: notices judge the live observations the ranking scans; an unpolled launcher only informs credit standing.
func unitStandingsTx(ctx context.Context, tx *storeTx, team Team, unit UnitRef) ([]eligibility, error) {
	polls, err := placementRunnersTx(ctx, tx, team, time.Minute, true)
	if err != nil {
		return nil, err
	}
	var standings []eligibility
	observed := map[string]bool{}
	for _, poll := range polls {
		observed[poll.CapabilityPrefix] = true
		standing, err := unitEligibilityTx(ctx, tx, team, unit, poll.UnitDemand, eligibilityChecks{fit: true, billing: true})
		if err != nil {
			return nil, err
		}
		standings = append(standings, standing)
	}
	rows, err := tx.QueryContext(ctx, `SELECT prefix FROM capabilities WHERE COALESCE(team,'')=? AND subject_kind='service' AND parent_id IS NULL AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>?) ORDER BY prefix LIMIT ?`, "", tx.now().UnixNano(), limits.RankingObservations)
	if err != nil {
		return nil, err
	}
	var launchers []string
	for rows.Next() {
		var prefix string
		if err := rows.Scan(&prefix); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		if !observed[prefix] {
			launchers = append(launchers, prefix)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for _, prefix := range launchers {
		standing, err := unitEligibilityTx(ctx, tx, team, unit, UnitDemand{CapabilityPrefix: prefix}, eligibilityChecks{billing: true})
		if err != nil {
			return nil, err
		}
		if standing.launcher {
			standing.unobserved = true
			standings = append(standings, standing)
		}
	}
	return standings, nil
}

var tierNames = map[string]string{TierHere: "The submitting machine", TierPersonal: "Personal machines", TierActions: "GitHub Actions runners", TierSelfHosted: "Self-hosted runners", TierCloud: "Cloud"}

func noEligibleCauseTx(ctx context.Context, tx *storeTx, team Team, run RunRow, unit UnitRef, standings []eligibility) (string, error) {
	var names []string
	for _, tier := range DefaultClaimRanking {
		if slices.ContainsFunc(standings, func(e eligibility) bool { return e.failed == stepTier && e.tier == tier }) {
			names = append(names, tierNames[tier])
		}
	}
	if len(names) > 0 {
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
	if slices.ContainsFunc(standings, func(e eligibility) bool { return !e.unobserved && e.failed == stepCapacity }) {
		return "", nil
	}
	ranking, err := claimRankingTx(ctx, tx, team)
	if err != nil {
		return "", err
	}
	allowed := "any tier"
	if len(ranking) > 0 {
		allowed = strings.Join(ranking, ", ")
	}
	return "no runner of the allowed tiers (" + allowed + ") is online; start or register a runner", nil
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
	return short.Error(), nil
}
