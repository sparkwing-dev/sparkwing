package store

import (
	"context"
	"errors"
	"slices"
)

func freshClaimWorkTx(ctx context.Context, tx *storeTx, demand UnitDemand, template *claimSQL) (bool, error) {
	if template.placementFree {
		return false, nil
	}
	q := claimSQL{dialect: template.dialect, freshOnly: true, target: demand.Target}
	if template.facts[0].Cloud == 0 {
		q.team = template.facts[0].Team
	}
	clock := q.param(template.now)
	source := q.source(template.kinds)
	var fresh bool
	err := tx.QueryRowContext(ctx, `WITH clock(now) AS (SELECT CAST(`+clock+` AS BIGINT)) SELECT EXISTS (SELECT 1 FROM (`+source+`) ready LIMIT 1)`, q.args...).Scan(&fresh)
	return fresh, err
}

func recordQueuedAsksTx(ctx context.Context, tx *storeTx, demand UnitDemand, template *claimSQL, teams map[string]bool) error {
	if template.placementFree {
		return nil
	}
	if teams == nil {
		fresh, err := freshClaimWorkTx(ctx, tx, demand, template)
		if err != nil {
			return err
		}
		if !fresh {
			return nil
		}
		query, args, err := template.freshTeamsSQL(demand)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		teams = map[string]bool{}
		for rows.Next() {
			var team string
			if err := rows.Scan(&team); err != nil {
				return errors.Join(err, rows.Close())
			}
			teams[team] = true
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
	}
	if len(teams) == 0 {
		return nil
	}
	facts := claimFactsForTeams(template.facts, teams)
	funding := slices.DeleteFunc(slices.Clone(template.teams), func(f claimTeamFacts) bool { return !teams[f.Team] })
	// A broad request asks for every eligible unit, including units behind the award.
	q := claimSQL{dialect: template.dialect, policy: template.policy, overflow: template.overflow, freshOnly: true, preferencesFree: true}
	if _, err := q.query(facts, funding, template.rates, template.kinds, demand, template.now); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, q.askSQL, q.askArgs...)
	return err
}
