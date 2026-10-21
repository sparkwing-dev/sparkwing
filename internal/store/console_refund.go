package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

// ConsoleRefund records a provider refund and its operator receipt in one transaction.
func (c *Console) ConsoleRefund(ctx context.Context, a capability.Authorized, req controllerwire.OperatorRefund, key string) (CreditReversal, error) {
	return consoleWrite(ctx, c, a, func(tx *storeTx, actor string) (CreditReversal, error) {
		req.PaymentID, req.RefundID = strings.TrimSpace(req.PaymentID), strings.TrimSpace(req.RefundID)
		reason, err := consoleReason(req.Reason)
		req.Reason = reason
		if err != nil {
			return CreditReversal{}, err
		}
		if key == "" || strings.TrimSpace(key) != key || req.PaymentID == "" || req.RefundID == "" || req.AmountCents <= 0 || req.AmountCents > MaxCreditGrantMicro/MicroCreditsPerCent {
			return CreditReversal{}, authorityError(apierr.BadRequest, "payment, refund, positive amount and idempotency key are required")
		}
		mode, err := paymentModeTx(ctx, tx)
		if err != nil {
			return CreditReversal{}, err
		}
		if err := lockPaymentTx(ctx, tx, mode, req.PaymentID); err != nil {
			return CreditReversal{}, err
		}
		if _, err := lockLedgerTx(ctx, tx, Team(a.Target.Team)); err != nil {
			return CreditReversal{}, err
		}
		t, err := consoleTeamTx(ctx, tx, a.Target.Team)
		if err != nil {
			return CreditReversal{}, err
		}
		identity, err := json.Marshal([]string{actor, key})
		if err != nil {
			return CreditReversal{}, err
		}
		sum := sha256.Sum256(identity)
		subject := hex.EncodeToString(sum[:])
		body, err := json.Marshal(struct {
			Mode    capability.PaymentMode
			Request controllerwire.OperatorRefund
		}{mode, req})
		if err != nil {
			return CreditReversal{}, err
		}
		digest := sha256.Sum256(body)
		var receipt struct {
			Digest [32]byte       `json:"digest"`
			Result CreditReversal `json:"result"`
		}
		err = tx.QueryRowContext(ctx, `SELECT attrs FROM business_events WHERE team=? AND kind='operator.refund_recorded' AND subject_id=?`, t.team, subject).Scan(&body)
		if err == nil {
			if err := json.Unmarshal(body, &receipt); err != nil {
				return CreditReversal{}, err
			}
			if receipt.Digest != digest {
				return CreditReversal{}, authorityError(apierr.Conflict, "idempotency key names another refund")
			}
			receipt.Result.Created = false
			return receipt.Result, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return CreditReversal{}, err
		}
		paid, found, err := tx.operator.paidGrantForPaymentTx(ctx, tx, req.PaymentID)
		if err != nil {
			return CreditReversal{}, err
		}
		if !found || paid.Team != t.team {
			return CreditReversal{}, authorityError(apierr.NotFound, "payment is unavailable in this team")
		}
		reference := paymentConsequenceReference(mode, "refund", req.RefundID)
		_, existed, err := creditGrantByReferenceTx(ctx, tx, paid.Team, CreditGrantReversal, reference)
		if err != nil {
			return CreditReversal{}, err
		}
		if _, err := tx.operator.applyPaymentStateTx(ctx, tx, capability.PaymentEvent{Kind: capability.PaymentRefund, Mode: mode, ObjectID: req.RefundID, PaymentID: req.PaymentID, AmountMinor: req.AmountCents, Status: CardRefundSucceeded}, actor); err != nil {
			return CreditReversal{}, err
		}
		result := CreditReversal{Team: paid.Team, PaidMicro: paid.AmountMicro}
		grant, found, err := creditGrantByReferenceTx(ctx, tx, paid.Team, CreditGrantReversal, reference)
		if err != nil {
			return CreditReversal{}, err
		}
		if found {
			result.Grant = &grant
			result.Created = !existed
		}
		result.ReversedMicro, err = reversedMicroTx(ctx, tx, paid.Team, paid.Reference)
		if err != nil {
			return CreditReversal{}, err
		}
		result.BalanceMicro, err = creditBalanceTx(ctx, tx, paid.Team)
		if err != nil {
			return CreditReversal{}, err
		}
		if err := tx.operator.RecordBusinessEvent(tx, BusinessEvent{At: tx.time, Team: t.team, Kind: "operator.refund_recorded", SubjectID: subject, Actor: actor, Attrs: map[string]any{"digest": digest, "result": result, "reason": req.Reason, "payment_id": req.PaymentID, "refund_id": req.RefundID}}); err != nil {
			return CreditReversal{}, err
		}
		return result, nil
	})
}
