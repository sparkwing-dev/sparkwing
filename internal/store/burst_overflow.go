package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// OverflowControl reports the authenticated user's monthly Cloud overflow budget.
type OverflowControl struct {
	MonthlyMinutes *int64    `json:"monthly_minutes"`
	UsedSeconds    int64     `json:"used_seconds"`
	PeriodStart    time.Time `json:"period_start"`
}

func overflowAccountTx(ctx context.Context, tx *storeTx, team Team, authority capability.AuthorityRef, grant capability.Grant) (string, error) {
	p, err := resolveTeamAuthorityTx(ctx, tx, team, authority, sameTeam)
	if err != nil {
		return "", err
	}
	c := p.Capability
	allowed := slices.Contains(c.Grants, grant) || grant == capability.RunsRead && slices.Contains(c.Grants, capability.RunsWrite)
	if c.Subject.Kind != capability.SubjectAccount || c.Binding.Kind != capability.BindingNone || c.Unit != "" || !allowed {
		return "", bindingRefusal()
	}
	return c.Subject.ID, nil
}

// SetOverflowLimit changes only the requesting account's monthly overflow allowance.
func (t *Tenant) SetOverflowLimit(ctx context.Context, authority capability.AuthorityRef, minutes *int64) error {
	if minutes != nil && (*minutes < 0 || *minutes > math.MaxInt64/60) {
		return authorityError(apierr.BadRequest, "invalid overflow minutes")
	}
	return t.s.inTx(ctx, func(tx *storeTx) error {
		account, err := overflowAccountTx(ctx, tx, t.team, authority, capability.RunsWrite)
		if err != nil {
			return err
		}
		if _, err := lockLedgerTx(ctx, tx, t.team); err != nil {
			return err
		}
		if minutes == nil {
			_, err = tx.execContext(ctx, `DELETE FROM overflow_controls WHERE team=? AND account_id=?`, t.team, account)
			return err
		}
		_, err = tx.execContext(ctx, `INSERT INTO overflow_controls(team,account_id,monthly_minutes) VALUES (?,?,?) ON CONFLICT(team,account_id) DO UPDATE SET monthly_minutes=excluded.monthly_minutes`, t.team, account, *minutes)
		return err
	})
}

// ReadOverflowControl reads only the requesting account's budget and usage.
func (t *Tenant) ReadOverflowControl(ctx context.Context, authority capability.AuthorityRef) (OverflowControl, error) {
	var out OverflowControl
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		account, err := overflowAccountTx(ctx, tx, t.team, authority, capability.RunsRead)
		if err != nil {
			return err
		}
		out, err = overflowControlTx(ctx, tx, t.team, account)
		return err
	})
	return out, err
}

func overflowControlTx(ctx context.Context, tx *storeTx, team Team, account string) (OverflowControl, error) {
	now := tx.now(ctx).UTC()
	out := OverflowControl{PeriodStart: time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)}
	var limit int64
	err := tx.QueryRowContext(ctx, `SELECT monthly_minutes FROM overflow_controls WHERE team=? AND account_id=?`, team, account).Scan(&limit)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if err == nil {
		out.MonthlyMinutes = &limit
	}
	mode, err := creditPaymentMode(ctx, tx)
	if err != nil {
		return out, err
	}
	rows, err := tx.queryContext(ctx, `SELECT a.capability_prefix,COALESCE((SELECT SUM(c.seconds) FROM credit_charges c WHERE c.team=a.team AND c.capability_prefix=a.capability_prefix AND c.kind IN ('reservation','slice','usage','refund')),0),i.started_at,i.finished_at,a.settled_at,a.awarded_at FROM billing_awards a LEFT JOIN billable_intervals i ON i.team=a.team AND i.reservation_id=a.reservation_id WHERE a.team=? AND a.requester_account=? AND a.executor_class='cloud' AND a.metered=1 AND EXISTS(SELECT 1 FROM credit_charges c WHERE c.team=a.team AND c.capability_prefix=a.capability_prefix AND c.kind='reservation' AND c.payment_mode=?) AND (a.awarded_at>=? OR a.settled_at IS NULL OR a.settled_at>=?)`, team, account, mode, out.PeriodStart.UnixNano(), out.PeriodStart.UnixNano())
	if err != nil {
		return out, err
	}
	type usage struct {
		charged, elapsed int64
		active           bool
		previousPeriod   bool
	}
	awards := map[string]usage{}
	for rows.Next() {
		var prefix string
		var charged, awarded int64
		var start, end, settled sql.NullInt64
		if err := rows.Scan(&prefix, &charged, &start, &end, &settled, &awarded); err != nil {
			return out, errors.Join(err, rows.Close())
		}
		a := awards[prefix]
		a.charged = charged
		a.active = !settled.Valid
		a.previousPeriod = awarded < out.PeriodStart.UnixNano()
		if start.Valid {
			until := now.UnixNano()
			if end.Valid {
				until = end.Int64
			}
			start.Int64 = max(start.Int64, out.PeriodStart.UnixNano())
			if until > start.Int64 {
				a.elapsed += (until - start.Int64 + int64(time.Second) - 1) / int64(time.Second)
			}
		}
		awards[prefix] = a
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return out, err
	}
	for _, a := range awards {
		seconds := a.charged
		if a.previousPeriod {
			seconds = a.elapsed
		} else if a.active {
			seconds = max(seconds, a.elapsed)
		}
		if seconds > math.MaxInt64-out.UsedSeconds {
			out.UsedSeconds = math.MaxInt64
			break
		}
		out.UsedSeconds += seconds
	}
	return out, nil
}

func checkOverflowTx(ctx context.Context, tx *storeTx, team Team, run string) error {
	burst, err := burstPlacementTx(ctx, tx, team, run)
	if err != nil || burst == nil {
		return err
	}
	var account string
	if err := tx.QueryRowContext(ctx, `SELECT created_principal FROM runs WHERE team=? AND id=?`, team, run).Scan(&account); err != nil {
		return err
	}
	control, err := overflowControlTx(ctx, tx, team, account)
	if err != nil {
		return err
	}
	limit := control.MonthlyMinutes
	if burst.OverflowMinutes != nil && (limit == nil || *burst.OverflowMinutes < *limit) {
		limit = burst.OverflowMinutes
	}
	if limit != nil && (control.UsedSeconds > *limit*60 || SliceSeconds > *limit*60-control.UsedSeconds) {
		return authorityError(apierr.LockHeld, "user monthly overflow budget reached; local capacity remains available")
	}
	return nil
}
