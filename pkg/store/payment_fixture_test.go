package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// TestOnlyReversePayment exercises the transaction owner with trusted fixture facts.
func (o *Operator) TestOnlyReversePayment(ctx context.Context, paymentID, reference, createdBy string, amountMicro int64) (out CreditReversal, err error) {
	err = o.s.inTx(ctx, func(tx *storeTx) error {
		out, err = reversePaymentTx(ctx, tx, paymentID, reference, createdBy, amountMicro)
		return err
	})
	if err != nil {
		return CreditReversal{}, err
	}
	return out, err
}

// TestOnlyPaymentTeam exercises the transaction owner with trusted fixture facts.
func (o *Operator) TestOnlyPaymentTeam(ctx context.Context, paymentID string) (Team, error) {
	var team Team
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		grant, found, err := paidGrantForPaymentTx(ctx, tx, strings.TrimSpace(paymentID))
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%w: %q", ErrUnknownPayment, paymentID)
		}
		team = grant.Team
		return nil
	})
	return team, err
}

// TestOnlyHoldForDispute exercises the transaction owner with trusted fixture facts.
func (t *Tenant) TestOnlyHoldForDispute(
	ctx context.Context, disputeID, paymentID, reason string, now time.Time,
) (bool, error) {
	return t.hold(ctx, disputeID, paymentID, reason, "", now)
}

// TestOnlySaveCard exercises the transaction owner with trusted fixture facts.
func (t *Tenant) TestOnlySaveCard(ctx context.Context, c Card, actor string, now time.Time) error {
	return t.s.inTx(ctx, func(tx *storeTx) error {
		return saveCardTx(ctx, tx, t.team, c, actor, now)
	})
}

// TestOnlyRecordPaymentWarning exercises the transaction owner with trusted fixture facts.
func (o *Operator) TestOnlyRecordPaymentWarning(ctx context.Context, w PaymentWarning, now time.Time) (team Team, err error) {
	err = o.s.inTx(ctx, func(tx *storeTx) error {
		team, err = recordPaymentWarningTx(ctx, tx, w, "", now)
		return err
	})
	if err != nil {
		return "", err
	}
	return team, nil
}

func settleCard(o *Operator, ctx context.Context, p CardPayment, now time.Time) (bool, *WarnedCardPayment, error) {
	settled, err := o.SettleCardPayment(ctx, p, now)
	return settled.Granted, settled.Warned, err
}
