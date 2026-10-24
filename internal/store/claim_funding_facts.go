package store

import (
	"context"
	"errors"
)

type claimOverflowFacts struct {
	Team           string `json:"team"`
	Account        string `json:"account"`
	UsedSeconds    int64  `json:"used_seconds"`
	MonthlyMinutes *int64 `json:"monthly_minutes"`
}

func (o *Operator) claimOverflowFactsTx(ctx context.Context, tx *storeTx, team string, current claimFacts, target *UnitRef) ([]claimOverflowFacts, error) {
	q := claimSQL{dialect: tx.dialect}
	scope := `tm.name IN (` + liveTeamsSQL + `)`
	if team != "" {
		scope += ` AND tm.name=` + q.param(team)
	}
	// Index seeks skip whole policy ranges belonging to the same account.
	query := `WITH RECURSIVE accounts(team,account) AS (
 SELECT tm.name,(SELECT MIN(overflow_account) FROM claim_contexts WHERE team=tm.name AND overflow_account IS NOT NULL) FROM teams tm WHERE ` + scope + `
 UNION ALL SELECT accounts.team,(SELECT MIN(overflow_account) FROM claim_contexts WHERE team=accounts.team AND overflow_account>accounts.account) FROM accounts WHERE account IS NOT NULL
 ) SELECT team,account FROM accounts WHERE account IS NOT NULL`
	// Narrow claims gather funding only after their indexed binding lookup.
	bound := claimSQL{dialect: tx.dialect}
	if scope := bound.boundRunScope(current, target); scope != "" {
		q = bound
		query = `WITH bound_runs AS MATERIALIZED (SELECT br.id,br.team,br.created_principal,br.status,br.request_json FROM runs br WHERE ` + scope + `) SELECT DISTINCT team,created_principal AS account FROM bound_runs WHERE team IN (` + liveTeamsSQL + `) AND status IN ('pending','running') AND ` + q.at(q.blob("request_json"), "request", "burst") + ` IS NOT NULL`
		if team != "" {
			query += ` AND team=` + q.param(team)
		}
	}
	rows, err := tx.queryContext(ctx, query+` ORDER BY team,account`, q.args...)
	if err != nil {
		return nil, err
	}
	var out []claimOverflowFacts
	for rows.Next() {
		var facts claimOverflowFacts
		if err := rows.Scan(&facts.Team, &facts.Account); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		out = append(out, facts)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for i := range out {
		control, err := overflowControlTx(ctx, tx, Team(out[i].Team), out[i].Account)
		if err != nil {
			return nil, err
		}
		out[i].UsedSeconds, out[i].MonthlyMinutes = control.UsedSeconds, control.MonthlyMinutes
	}
	return out, nil
}
