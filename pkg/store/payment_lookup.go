package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// safety: provider payment IDs identify the payer before any tenant is known.
// Grant references are ledger identities; only the persisted checkout can link them.
func paidGrantForPaymentTx(ctx context.Context, tx *storeTx, paymentID string) (CreditGrant, bool, error) {
	if paymentID == "" {
		return CreditGrant{}, false, nil
	}
	var checkouts int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM credit_checkouts WHERE payment_id=?", paymentID).Scan(&checkouts); err != nil {
		return CreditGrant{}, false, err
	}
	var mode capability.PaymentMode
	if checkouts != 0 {
		var err error
		mode, err = paymentModeTx(ctx, tx)
		if err != nil {
			return CreditGrant{}, false, err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT g.team,g.id,g.kind,g.amount_micro,g.reference,g.reverses,g.created_by,g.created_at,c.team,c.amount_micro
	 FROM credit_checkouts c JOIN credit_grants g ON g.id=c.grant_id
	 WHERE c.payment_mode=? AND c.payment_id=? AND c.paid_at IS NOT NULL
	 UNION ALL
	 SELECT g.team,g.id,g.kind,g.amount_micro,g.reference,g.reverses,g.created_by,g.created_at,g.team,g.amount_micro
	 FROM credit_grants g WHERE g.kind='paid' AND g.reference=?
	 AND NOT EXISTS (SELECT 1 FROM credit_checkouts c WHERE c.grant_id=g.id)`, mode, paymentID, paymentID)
	if err != nil {
		return CreditGrant{}, false, err
	}
	var result CreditGrant
	found := false
	for rows.Next() {
		var grant CreditGrant
		var owner Team
		var amount, created int64
		if err := rows.Scan(&grant.Team, &grant.ID, &grant.Kind, &grant.AmountMicro, &grant.Reference, &grant.Reverses, &grant.CreatedBy, &created, &owner, &amount); err != nil {
			return CreditGrant{}, false, errors.Join(err, rows.Close())
		}
		if found || grant.Team != owner || grant.AmountMicro != amount || grant.Kind != CreditGrantPaid {
			return CreditGrant{}, false, errors.Join(ErrCreditGrantConflict, rows.Close())
		}
		grant.CreatedAt = time.Unix(0, created).UTC()
		result, found = grant, true
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return CreditGrant{}, false, err
	}
	return result, found, nil
}

func paymentWarningTeamTx(ctx context.Context, tx *storeTx, paymentID string) (Team, error) {
	grant, found, err := paidGrantForPaymentTx(ctx, tx, paymentID)
	if err != nil || found {
		return grant.Team, err
	}
	var team Team
	err = tx.QueryRowContext(ctx, "SELECT team FROM card_attempts WHERE payment_intent=? LIMIT 1", paymentID).Scan(&team)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return team, err
}
