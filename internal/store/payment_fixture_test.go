package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// TestOnlyReversePayment exercises the transaction owner with trusted fixture facts.
func (o *Operator) TestOnlyReversePayment(ctx context.Context, paymentID, reference, createdBy string, amountMicro int64) (out creditReversal, err error) {
	err = o.s.inTx(ctx, func(tx *storeTx) error {
		out, err = reversePaymentTx(ctx, tx, paymentID, reference, auditActor{system: auditPaymentProvider}, amountMicro)
		return err
	})
	if err != nil {
		return creditReversal{}, err
	}
	return out, err
}

// TestOnlyPaymentTeam exercises the transaction owner with trusted fixture facts.
func (o *Operator) TestOnlyPaymentTeam(ctx context.Context, paymentID string) (Team, error) {
	var team Team
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		grant, found, err := tx.operator.paidGrantForPaymentTx(ctx, tx, strings.TrimSpace(paymentID))
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%w: %q", errUnknownPayment, paymentID)
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
	var created bool
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		var err error
		created, err = holdCreditFreezeTx(ctx, tx, t.team, disputeID, paymentID, reason, "", now, auditActor{system: auditBreakGlass})
		return err
	})
	return created, err
}

// TestOnlySaveCard exercises the transaction owner with trusted fixture facts.
func (t *Tenant) TestOnlySaveCard(ctx context.Context, c Card, actor string, now time.Time) error {
	return t.s.inTx(ctx, func(tx *storeTx) error {
		return saveCardTx(ctx, tx, t.team, c, "seti_"+c.Customer, auditActor{system: auditPaymentProvider}, now)
	})
}

// paymentWarning is an early fraud warning a test records directly.
type paymentWarning struct {
	WarningID     string
	PaymentIntent string
	Actionable    bool
}

// TestOnlyRecordPaymentWarning exercises the transaction owner with trusted fixture facts.
func (o *Operator) TestOnlyRecordPaymentWarning(ctx context.Context, w paymentWarning, now time.Time) (team Team, err error) {
	err = o.s.inTx(ctx, func(tx *storeTx) error {
		mode, modeErr := paymentModeTx(ctx, tx)
		if modeErr != nil {
			return modeErr
		}
		if err := lockPaymentTx(ctx, tx, mode, w.PaymentIntent); err != nil {
			return err
		}
		result, applyErr := tx.operator.applyPaymentStateTx(ctx, tx, capability.PaymentEvent{Kind: capability.PaymentWarningEvent, Mode: mode, ObjectID: w.WarningID, PaymentID: w.PaymentIntent, Actionable: w.Actionable}, auditActor{system: auditPaymentProvider})
		team, err = result.Team, applyErr
		return err
	})
	if err != nil {
		return "", err
	}
	return team, nil
}

// TestOnlySettleRefund records trusted provider facts through the settlement transaction.
func (s *Store) TestOnlySettleRefund(ctx context.Context, event capability.PaymentEvent) error {
	_, err := s.settlePaymentFacts(ctx, event)
	return err
}
