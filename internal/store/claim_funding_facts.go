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

func (o *Operator) claimOverflowFactsTx(ctx context.Context, tx *storeTx, team string, cloud bool) ([]claimOverflowFacts, error) {
	q := claimSQL{dialect: tx.dialect}
	query := `SELECT DISTINCT team,created_principal FROM runs WHERE team IN (` + liveTeamsSQL + `) AND status IN ('pending','running') AND ` + q.at(q.blob("request_json"), "request", "burst") + ` IS NOT NULL`
	if !cloud {
		query += ` AND team=` + q.param(team)
	}
	rows, err := tx.queryContext(ctx, query+` ORDER BY team,created_principal`, q.args...)
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
