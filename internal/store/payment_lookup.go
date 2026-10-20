package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// safety: every payment this deployment takes is an Add funds checkout or a refill, both rows of credit_checkouts,
// so that row is the one answer to whose payment this is and which grant it funded.
func (op *Operator) paidGrantForPaymentTx(ctx context.Context, tx *storeTx, paymentID string) (CreditGrant, bool, error) {
	if paymentID == "" {
		return CreditGrant{}, false, nil
	}
	mode, err := paymentModeTx(ctx, tx)
	if err != nil {
		return CreditGrant{}, false, err
	}
	var g CreditGrant
	var created int64
	err = tx.QueryRowContext(ctx, `SELECT g.team,g.id,g.kind,g.amount_micro,g.reference,g.reverses,g.created_by,g.created_at
	 FROM credit_checkouts c JOIN credit_grants g ON g.team=c.team AND g.id=c.grant_id
	 WHERE c.payment_mode=? AND c.payment_id=? AND c.paid_at IS NOT NULL`, mode, paymentID).Scan(&g.Team, &g.ID, &g.Kind, &g.AmountMicro, &g.Reference, &g.Reverses, &g.CreatedBy, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return CreditGrant{}, false, nil
	}
	if err != nil {
		return CreditGrant{}, false, err
	}
	g.CreatedAt = time.Unix(0, created).UTC()
	return g, true, nil
}

func (op *Operator) paymentTeamTx(ctx context.Context, tx *storeTx, mode capability.PaymentMode, paymentID string) (Team, error) {
	if mode == "" {
		var err error
		if mode, err = paymentModeTx(ctx, tx); err != nil {
			return "", err
		}
	}
	var team Team
	err := tx.QueryRowContext(ctx, `SELECT team FROM credit_checkouts WHERE payment_mode=? AND payment_id=? AND payment_id<>''`, mode, paymentID).Scan(&team)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return team, err
}
