package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func (s *Store) settleProviderPaymentFacts(ctx context.Context, event capability.PaymentEvent) (PaymentSettlement, error) {
	if err := validateProviderPayment(event); err != nil {
		return PaymentSettlement{}, err
	}
	body, err := json.Marshal(event)
	if err != nil {
		return PaymentSettlement{}, err
	}
	digest, err := paymentEventDigest(event)
	if err != nil {
		return PaymentSettlement{}, err
	}
	var result PaymentSettlement
	err = s.inTx(ctx, func(tx *storeTx) error {
		if err := configuredPaymentModeTx(ctx, tx, event.Mode); err != nil {
			return err
		}
		replayed, err := paymentEventReplayTx(ctx, tx, event, digest)
		if err != nil {
			return err
		}
		if replayed {
			var encoded []byte
			if err := tx.QueryRowContext(ctx, "SELECT settlement_json FROM payment_events WHERE payment_mode=? AND event_id=?", event.Mode, event.EventID).Scan(&encoded); err != nil {
				return err
			}
			if err := json.Unmarshal(encoded, &result); err != nil {
				return err
			}
			result.Replayed = true
			return nil
		}
		if err := checkPaymentObjectTx(ctx, tx, event); err != nil {
			return err
		}
		result, err = applyProviderPaymentTx(ctx, tx, event)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO payment_events(team,payment_mode,event_id,checkout_id,payment_id,event_kind,event_digest,grant_id,processed_at,object_id,event_json,settlement_json) VALUES (?,?,?,NULL,?,?,?,?,?,?,?,?)`, optionalString(string(result.Team)), event.Mode, event.EventID, event.PaymentID, event.Kind, digest, optionalString(result.GrantID), tx.now().UnixNano(), event.ObjectID, string(body), string(encoded))
		return err
	})
	if err != nil {
		return PaymentSettlement{}, err
	}
	return result, nil
}

func validateProviderPayment(e capability.PaymentEvent) error {
	if e.EventID == "" || e.ObjectID == "" || e.CheckoutID != "" || e.Created <= 0 || e.Created > math.MaxInt64/int64(time.Second) || (e.Mode != capability.PaymentTest && e.Mode != capability.PaymentLive) {
		return authorityError(apierr.BadRequest, "invalid verified provider facts")
	}
	switch e.Kind {
	case capability.PaymentCardSetup:
		return nil
	case capability.PaymentPageCompleted:
		if e.IntentID != "" && e.CustomerID != "" {
			return nil
		}
	case capability.PaymentWarningEvent:
		if e.PaymentID != "" {
			return nil
		}
	case capability.PaymentCardPaid, capability.PaymentCardFailed, capability.PaymentRefund, capability.PaymentDisputeOpened, capability.PaymentDisputeClosed:
		if e.PaymentID != "" && e.AmountMinor > 0 && e.Currency == "usd" {
			_, err := billingProduct(e.AmountMinor, MicroCreditsPerCent)
			return err
		}
	}
	return authorityError(apierr.BadRequest, "unsupported or incomplete provider outcome")
}

func checkPaymentObjectTx(ctx context.Context, tx *storeTx, event capability.PaymentEvent) error {
	rows, err := tx.QueryContext(ctx, "SELECT event_json FROM payment_events WHERE payment_mode=? AND object_id=?", event.Mode, event.ObjectID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var encoded []byte
		if err := rows.Scan(&encoded); err != nil {
			return errors.Join(err, rows.Close())
		}
		var previous capability.PaymentEvent
		if err := json.Unmarshal(encoded, &previous); err != nil {
			return errors.Join(err, rows.Close())
		}
		if !samePaymentObject(previous, event) {
			return errors.Join(authorityError(apierr.Conflict, "provider object changed its committed association"), rows.Close())
		}
	}
	return errors.Join(rows.Err(), rows.Close())
}

func samePaymentObject(a, b capability.PaymentEvent) bool {
	family := func(kind capability.PaymentEventKind) capability.PaymentEventKind {
		switch kind {
		case capability.PaymentCardFailed:
			return capability.PaymentCardPaid
		case capability.PaymentDisputeClosed:
			return capability.PaymentDisputeOpened
		default:
			return kind
		}
	}
	if family(a.Kind) != family(b.Kind) || a.PaymentID != b.PaymentID || a.IntentID != b.IntentID || a.CustomerID != "" && b.CustomerID != "" && a.CustomerID != b.CustomerID {
		return false
	}
	if family(a.Kind) == capability.PaymentCardPaid && (a.Kind == capability.PaymentCardFailed || b.Kind == capability.PaymentCardFailed) {
		return true
	}
	if a.AmountMinor != b.AmountMinor || a.Currency != b.Currency || a.ChargeID != b.ChargeID {
		return false
	}
	for _, values := range [][2]string{{a.CustomerID, b.CustomerID}, {a.MethodID, b.MethodID}, {a.Fingerprint, b.Fingerprint}} {
		if values[0] != "" && values[1] != "" && values[0] != values[1] {
			return false
		}
	}
	if a.Kind == capability.PaymentCardSetup && (a.Brand != b.Brand || a.Last4 != b.Last4) {
		return false
	}
	return a.Kind != capability.PaymentDisputeClosed || b.Kind != capability.PaymentDisputeClosed || a.Status == b.Status
}

func readSetupCorrelationTx(ctx context.Context, tx *storeTx, event capability.PaymentEvent) (Team, string, error) {
	var team Team
	var customer string
	err := tx.QueryRowContext(ctx, `SELECT team,customer_id FROM payment_correlations WHERE payment_mode=? AND provider_id=? AND kind='setup'`+tx.forShare(), event.Mode, event.ObjectID).Scan(&team, &customer)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", authorityError(apierr.NotFound, "committed provider association is unavailable")
	}
	if err != nil {
		return "", "", err
	}
	if team == "" || customer == "" || event.CustomerID != "" && customer != event.CustomerID {
		return "", "", authorityError(apierr.Conflict, "provider facts conflict with committed billing association")
	}
	return team, customer, nil
}

func applyProviderPaymentTx(ctx context.Context, tx *storeTx, e capability.PaymentEvent) (PaymentSettlement, error) {
	var result PaymentSettlement
	switch e.Kind {
	case capability.PaymentPageCompleted:
		return bindCompletedPageTx(ctx, tx, e)
	case capability.PaymentCardSetup:
		team, customer, err := readSetupCorrelationTx(ctx, tx, e)
		if err != nil {
			return result, err
		}
		result.Team = team
		var saved int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM payment_events WHERE payment_mode=? AND object_id=? AND event_kind=?", e.Mode, e.ObjectID, e.Kind).Scan(&saved); err != nil {
			return result, err
		}
		if saved != 0 {
			result.Replayed = true
			return result, nil
		}
		err = saveCardTx(ctx, tx, team, Card{Customer: customer, PaymentMethod: e.MethodID, Fingerprint: e.Fingerprint, Brand: e.Brand, Last4: e.Last4, AddedAt: time.Unix(e.Created, 0)}, "payment_provider", tx.now())
		return result, err
	case capability.PaymentCardPaid, capability.PaymentCardFailed:
		var checkout paymentCheckout
		var customer string
		err := tx.QueryRowContext(ctx, `SELECT id,team,kind,payment_id,currency,amount_minor,amount_micro,paid_at,closed_at,grant_id,customer_id FROM credit_checkouts WHERE payment_mode=? AND payment_id=? AND kind=?`+tx.forUpdate(), e.Mode, e.PaymentID, CheckoutKindRefill).Scan(append(checkout.scan(), &customer)...)
		if errors.Is(err, sql.ErrNoRows) {
			return result, authorityError(apierr.NotFound, "committed refill is unavailable")
		}
		if err != nil {
			return result, err
		}
		if checkout.currency != e.Currency || e.CustomerID != customer || e.Kind == capability.PaymentCardPaid && checkout.amount != e.AmountMinor {
			return result, authorityError(apierr.Conflict, "payment facts conflict with committed refill")
		}
		checkout.result.Payment, checkout.result.GrantID = e.PaymentID, checkout.grant.String
		checkout.result.Replayed = checkout.paid.Valid
		err = applyPaymentOutcomeTx(ctx, tx, e, &checkout)
		return checkout.result, err
	case capability.PaymentWarningEvent:
		owner, err := paymentTeamTx(ctx, tx, e.Mode, e.PaymentID)
		if err != nil {
			return result, err
		}
		team, err := recordPaymentWarningTx(ctx, tx, PaymentWarning{WarningID: e.ObjectID, PaymentIntent: e.PaymentID, Fingerprint: e.Fingerprint, Actionable: e.Actionable}, tx.now())
		if err == nil && team != "" && team != owner {
			return result, authorityError(apierr.Conflict, "warning changed its persisted payer")
		}
		result.Team, result.Payment = owner, e.PaymentID
		return result, err
	case capability.PaymentRefund:
		return settleProviderRefundTx(ctx, tx, e)
	case capability.PaymentDisputeOpened, capability.PaymentDisputeClosed:
		team, err := paymentTeamTx(ctx, tx, e.Mode, e.PaymentID)
		if err != nil {
			return result, err
		}
		if team == "" {
			return result, authorityError(apierr.NotFound, "disputed payment is unavailable")
		}
		result.Team = team
		if _, err := holdCreditFreezeTx(ctx, tx, team, e.ObjectID, e.PaymentID, e.Reason, "", tx.now()); err != nil {
			return result, err
		}
		if e.Kind == capability.PaymentDisputeOpened {
			return result, nil
		}
		if e.Status == "lost" {
			e.Status = CardRefundSucceeded
			if err := recordPaymentConsequenceTx(ctx, tx, e, "dispute"); err != nil {
				return result, err
			}
			if err := applyPaymentConsequencesTx(ctx, tx, e.PaymentID); err != nil {
				return result, err
			}
			reversal, found, err := creditGrantByReferenceTx(ctx, tx, team, CreditGrantReversal, e.ObjectID)
			if found {
				result.GrantID, result.AmountMicro = reversal.ID, reversal.AmountMicro
			}
			return result, err
		}
		_, err = releaseCreditFreezesTx(ctx, tx, team, e.ObjectID, false, false, "", "", tx.now())
		return result, err
	}
	return result, authorityError(apierr.BadRequest, "unsupported provider outcome")
}

func settleProviderRefundTx(ctx context.Context, tx *storeTx, e capability.PaymentEvent) (PaymentSettlement, error) {
	team, err := paymentTeamTx(ctx, tx, e.Mode, e.PaymentID)
	if err != nil {
		return PaymentSettlement{}, err
	}
	result := PaymentSettlement{Team: team, Payment: e.PaymentID}
	if err := recordPaymentConsequenceTx(ctx, tx, e, "refund"); err != nil {
		return result, err
	}
	if err := applyPaymentConsequencesTx(ctx, tx, e.PaymentID); err != nil {
		return result, err
	}
	if err := closeRefundedQueueTx(ctx, tx, e.PaymentID); err != nil {
		return result, err
	}
	if team == "" {
		return result, nil
	}
	reversal, found, err := creditGrantByReferenceTx(ctx, tx, team, CreditGrantReversal, "refund:"+string(e.Mode)+":"+e.ObjectID)
	if err != nil {
		return result, err
	}
	if found {
		result.GrantID, result.AmountMicro = reversal.ID, reversal.AmountMicro
	}
	if e.Status == CardRefundSucceeded || e.Status == "failed" || e.Status == "canceled" {
		_, err = applyCardRefundTx(ctx, tx, team, e.PaymentID, e.ObjectID, e.Status, `refund_id = ? AND status = 'pending'`, []any{e.ObjectID}, tx.now())
	}
	return result, err
}

func bindCompletedPageTx(ctx context.Context, tx *storeTx, e capability.PaymentEvent) (PaymentSettlement, error) {
	var team, customer string
	err := tx.QueryRowContext(ctx, `SELECT team,customer_id FROM payment_correlations WHERE payment_mode=? AND provider_id=? AND kind='setup_session'`+tx.forShare(), e.Mode, e.ObjectID).Scan(&team, &customer)
	if errors.Is(err, sql.ErrNoRows) {
		return PaymentSettlement{}, authorityError(apierr.NotFound, "committed payment page is unavailable")
	}
	if err != nil {
		return PaymentSettlement{}, err
	}
	if customer != e.CustomerID {
		return PaymentSettlement{}, authorityError(apierr.Conflict, "completed page names another customer")
	}
	result := PaymentSettlement{Team: Team(team)}
	var bound string
	err = tx.QueryRowContext(ctx, `SELECT team FROM payment_correlations WHERE payment_mode=? AND provider_id=? AND kind='setup' AND customer_id=?`, e.Mode, e.IntentID, customer).Scan(&bound)
	switch {
	case err == nil && bound == team:
		return result, nil
	case err == nil:
		return PaymentSettlement{}, authorityError(apierr.Conflict, "setup intent belongs to another team")
	case !errors.Is(err, sql.ErrNoRows):
		return PaymentSettlement{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO payment_correlations(payment_mode,provider_id,kind,team,customer_id) VALUES (?,?,'setup',?,?)`, e.Mode, e.IntentID, team, customer)
	return result, err
}

// RecordsPaymentParent reports whether this deployment committed the checkout, refill, page or intent an event concerns.
func (s *Store) RecordsPaymentParent(ctx context.Context, r capability.PaymentRoute) (bool, error) {
	var found int
	err := s.queryRow(ctx, `SELECT CASE WHEN
 EXISTS(SELECT 1 FROM credit_checkouts WHERE payment_mode=? AND (session_id=? OR payment_id<>'' AND payment_id=?))
 OR EXISTS(SELECT 1 FROM payment_correlations WHERE payment_mode=? AND provider_id IN (?,?,?))
 THEN 1 ELSE 0 END`, r.Mode, r.CheckoutID, r.PaymentID, r.Mode, r.CheckoutID, r.PaymentID, r.ObjectID).Scan(&found)
	return found == 1, err
}
