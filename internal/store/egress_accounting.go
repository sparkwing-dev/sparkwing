package store

import (
	"context"
	"errors"
	"time"
)

// EgressUsage reports one team's egress bytes for a UTC day or month.
type EgressUsage struct {
	Team      Team
	Period    string
	Bytes     int64
	UpdatedAt time.Time
}

func egressPeriod(period string) (string, string, error) {
	layout := "2006-01-02"
	if len(period) == len("2006-01") {
		layout = "2006-01"
	}
	at, err := time.Parse(layout, period)
	if err != nil || at.Format(layout) != period {
		return "", "", errors.New("egress requires a UTC day or month")
	}
	if layout == "2006-01" {
		return period, at.AddDate(0, 1, 0).Format(layout), nil
	}
	return period, at.AddDate(0, 0, 1).Format(layout), nil
}

// EgressUsageAcrossTeams lists team totals for a UTC day or month, largest first.
func (o *Operator) EgressUsageAcrossTeams(ctx context.Context, period string) (_ []EgressUsage, err error) {
	start, end, err := egressPeriod(period)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.query(ctx, `SELECT team,SUM(bytes),MAX(updated_at),SUM(CASE WHEN bytes<0 THEN 1 ELSE 0 END) FROM egress_usage WHERE period>=? AND period<? GROUP BY team ORDER BY SUM(bytes) DESC,team`, start, end)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	out := []EgressUsage{}
	for rows.Next() {
		u := EgressUsage{Period: period}
		var updated, invalid int64
		if err := rows.Scan(&u.Team, &u.Bytes, &updated, &invalid); err != nil {
			return nil, err
		}
		if invalid != 0 || u.Bytes < 0 {
			return nil, errors.New("egress has invalid usage counters")
		}
		u.UpdatedAt = time.Unix(0, updated).UTC()
		out = append(out, u)
	}
	return out, rows.Err()
}

// egressUsageRetentionMonths preserves thirteen months of egress reports.
const egressUsageRetentionMonths = 13

// PruneEgressUsage expires old reports under the store clock.
func (o *Operator) PruneEgressUsage(ctx context.Context) (int64, error) {
	var removed int64
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		now := tx.now(ctx).UTC()
		cutoff := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -egressUsageRetentionMonths, 0).Format("2006-01")
		result, err := tx.execContext(ctx, "DELETE FROM egress_usage WHERE period<?", cutoff)
		if err != nil {
			return err
		}
		removed, err = result.RowsAffected()
		return err
	})
	return removed, err
}
