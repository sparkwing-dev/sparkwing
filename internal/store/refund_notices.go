package store

import (
	"context"
	"errors"
	"time"
)

// FailedRefundNotice is a queued refund Stripe failed or canceled whose team owners have not been told.
type FailedRefundNotice struct {
	Team        Team
	AmountMicro int64
	PaidAt      time.Time
}

// ClaimFailedRefundNotices marks every failed or canceled queued refund as announced and returns those it marked.
// The caller mails after the commit, so each refund is announced at most once and a lost send loses only that email.
func (o *Operator) ClaimFailedRefundNotices(ctx context.Context) ([]FailedRefundNotice, error) {
	return inTxResult(ctx, o.s, func(tx *storeTx) ([]FailedRefundNotice, error) {
		mode, err := creditPaymentMode(ctx, tx)
		if err != nil {
			return nil, err
		}
		rows, err := tx.queryContext(ctx, `SELECT r.team,r.payment_intent,
	    CASE WHEN r.amount_micro>0 THEN r.amount_micro ELSE COALESCE(c.amount_micro,0) END,
	    COALESCE(c.paid_at,c.closed_at,r.created_at)
	    FROM card_refunds r LEFT JOIN credit_checkouts c ON c.payment_mode=r.payment_mode AND c.payment_id=r.payment_intent AND c.team=r.team
	    WHERE r.payment_mode=? AND r.status IN ('failed','canceled') AND r.owners_notified_at IS NULL
	    ORDER BY r.created_at,r.team,r.payment_intent`, mode)
		if err != nil {
			return nil, err
		}
		type candidate struct {
			notice  FailedRefundNotice
			payment string
		}
		var found []candidate
		for rows.Next() {
			var c candidate
			var paid int64
			if err := rows.Scan(&c.notice.Team, &c.payment, &c.notice.AmountMicro, &paid); err != nil {
				return nil, errors.Join(err, rows.Close())
			}
			c.notice.PaidAt = time.Unix(0, paid).UTC()
			found = append(found, c)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
		var out []FailedRefundNotice
		for _, c := range found {
			res, err := tx.execContext(ctx, `UPDATE card_refunds SET owners_notified_at=? WHERE payment_mode=? AND team=? AND payment_intent=? AND owners_notified_at IS NULL`,
				tx.now(ctx).UnixNano(), mode, string(c.notice.Team), c.payment)
			if err != nil {
				return nil, err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return nil, err
			}
			if n == 1 {
				out = append(out, c.notice)
			}
		}
		return out, nil
	})
}
