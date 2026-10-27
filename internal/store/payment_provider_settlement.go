package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func (s *Store) settlePaymentFacts(ctx context.Context, event capability.PaymentEvent) (PaymentSettlement, error) {
	if err := validatePaymentEvent(event); err != nil {
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
		teams, err := lockPaymentTeamsTx(ctx, tx, event)
		if err != nil {
			return err
		}
		if err := lockPaymentTx(ctx, tx, event.Mode, paymentStateID(event)); err != nil {
			return err
		}
		current := tx.operator.discoverPaymentTeamsTx(ctx, tx, event).normalized()
		if current.err != nil {
			return current.err
		}
		if !slices.Equal(teams.rows, current.rows) {
			return errAuthorityChanged
		}
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
		if event.ObjectID != "" {
			if err := checkPaymentObjectTx(ctx, tx, event); err != nil {
				return err
			}
		}
		result, err = tx.operator.applyPaymentStateTx(ctx, tx, event, auditActor{system: auditPaymentProvider})
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		var checkoutID any
		if event.CheckoutID != "" {
			checkoutID = result.CheckoutID
		}
		_, err = tx.execContext(ctx, `INSERT INTO payment_events(team,payment_mode,event_id,checkout_id,payment_id,event_kind,event_digest,grant_id,processed_at,object_id,event_json,settlement_json) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, optionalString(string(result.Team)), event.Mode, event.EventID, checkoutID, event.PaymentID, event.Kind, digest, optionalString(result.GrantID), tx.now(ctx).UnixNano(), event.ObjectID, string(body), string(encoded))
		return err
	})
	if err != nil {
		return PaymentSettlement{}, err
	}
	return result, nil
}

func paymentStateID(e capability.PaymentEvent) string {
	if e.PaymentID != "" {
		return e.PaymentID
	}
	if e.IntentID != "" {
		return e.IntentID
	}
	if e.CheckoutID != "" {
		return e.CheckoutID
	}
	return e.ObjectID
}

func lockPaymentTx(ctx context.Context, tx *storeTx, mode capability.PaymentMode, id string) error {
	return advisoryLockTx(ctx, tx, "sparkwing/payment/"+string(mode)+"/"+id)
}

func validatePaymentEvent(e capability.PaymentEvent) error {
	if e.Kind == capability.PaymentCheckoutPaid || e.Kind == capability.PaymentCheckoutExpired || e.Kind == capability.PaymentCheckoutFailed {
		if e.EventID == "" || e.CheckoutID == "" || e.AmountMinor <= 0 || e.Currency == "" || (e.Mode != capability.PaymentTest && e.Mode != capability.PaymentLive) || e.Kind == capability.PaymentCheckoutPaid && e.PaymentID == "" {
			return authorityError(apierr.BadRequest, "invalid verified payment facts")
		}
		return nil
	}
	return validateProviderPayment(e)
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
		cardPayment := e.Kind == capability.PaymentCardPaid || e.Kind == capability.PaymentCardFailed
		if e.PaymentID != "" && e.AmountMinor > 0 && e.Currency == "usd" && (e.RefillID != "") == cardPayment {
			_, err := billingProduct(e.AmountMinor, MicroCreditsPerCent)
			return err
		}
	}
	return authorityError(apierr.BadRequest, "unsupported or incomplete provider outcome")
}

func checkPaymentObjectTx(ctx context.Context, tx *storeTx, event capability.PaymentEvent) error {
	rows, err := tx.queryContext(ctx, "SELECT event_json FROM payment_events WHERE payment_mode=? AND object_id=?", event.Mode, event.ObjectID)
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
	if family(a.Kind) != family(b.Kind) || a.PaymentID != b.PaymentID || a.IntentID != b.IntentID || a.RefillID != b.RefillID || a.CustomerID != "" && b.CustomerID != "" && a.CustomerID != b.CustomerID {
		return false
	}
	if family(a.Kind) == capability.PaymentCardPaid && (a.Kind == capability.PaymentCardFailed || b.Kind == capability.PaymentCardFailed) {
		return true
	}
	if a.AmountMinor != b.AmountMinor || a.Currency != b.Currency || a.ChargeID != b.ChargeID {
		return false
	}
	return a.Kind != capability.PaymentDisputeClosed || b.Kind != capability.PaymentDisputeClosed || a.Status == b.Status
}

func (op *Operator) applyPaymentStateTx(ctx context.Context, tx *storeTx, e capability.PaymentEvent, actor auditActor) (PaymentSettlement, error) {
	set, err := lockPaymentTeamsTx(ctx, tx, e)
	if err != nil {
		return PaymentSettlement{}, err
	}
	for _, row := range set.rows {
		if _, err := lockLedgerTx(ctx, tx, Team(row.key.keys[0])); err != nil {
			return PaymentSettlement{}, err
		}
	}
	result, err := tx.operator.reducePaymentStateTx(ctx, tx, e)
	if err != nil {
		return result, err
	}
	if e.Kind == capability.PaymentCardSetup || e.Kind == capability.PaymentPageCompleted {
		intent := e.ObjectID
		if e.Kind == capability.PaymentPageCompleted {
			intent = e.IntentID
		}
		team, err := tx.operator.reconcileSetupStateTx(ctx, tx, e.Mode, intent)
		if err != nil {
			return result, err
		}
		if team != "" {
			result.Team = team
		}
	}
	if e.PaymentID == "" {
		return result, nil
	}
	if err := tx.operator.reconcilePaymentStateTx(ctx, tx, e.Mode, e.PaymentID, paymentAttribution{refund: e.ObjectID, actor: actor}); err != nil {
		return result, err
	}
	if result.Team == "" {
		result.Team, err = tx.operator.paymentTeamTx(ctx, tx, e.Mode, e.PaymentID)
		if err != nil {
			return result, err
		}
	}
	if e.Kind == capability.PaymentRefund || e.Kind == capability.PaymentDisputeClosed {
		kind := "dispute"
		if e.Kind == capability.PaymentRefund {
			kind = "refund"
		}
		reference := paymentConsequenceReference(e.Mode, kind, e.ObjectID)
		reversal, found, err := creditGrantByReferenceTx(ctx, tx, result.Team, creditGrantReversal, reference)
		if err != nil {
			return result, err
		}
		if found {
			result.GrantID, result.AmountMicro = reversal.ID, reversal.AmountMicro
		}
	}
	return result, nil
}

func (op *Operator) discoverPaymentTeamsTx(ctx context.Context, tx *storeTx, e capability.PaymentEvent) lockSet {
	var set lockSet
	rows, err := tx.queryContext(ctx, `SELECT team FROM credit_checkouts WHERE payment_mode=? AND ((payment_id<>'' AND payment_id=?) OR (session_id<>id AND session_id=?) OR (kind='refill' AND id=?))
 UNION SELECT team FROM payment_correlations WHERE payment_mode=? AND provider_id IN (?,?,?)`, e.Mode, e.PaymentID, e.CheckoutID, e.RefillID, e.Mode, e.ObjectID, e.IntentID, e.CheckoutID)
	if err != nil {
		set.err = err
		return set
	}
	for rows.Next() {
		var team string
		if err := rows.Scan(&team); err != nil {
			set.err = err
			break
		}
		set.add(lockTeam, true, team)
	}
	set.err = errors.Join(set.err, rows.Err(), rows.Close())
	return set
}

func lockPaymentTeamTx(ctx context.Context, tx *storeTx, team Team) error {
	lockErr := lockFinancialTeamsTx(ctx, tx, team)
	if lockErr != nil && !errors.Is(lockErr, ErrUnknownTeam) {
		return lockErr
	}
	var deleting int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM team_deletions WHERE slug=?`, string(team)).Scan(&deleting); err != nil {
		return err
	}
	if deleting != 0 {
		return ErrTeamBeingDeleted
	}
	return lockErr
}

func (op *Operator) reducePaymentStateTx(ctx context.Context, tx *storeTx, e capability.PaymentEvent) (PaymentSettlement, error) {
	var result PaymentSettlement
	switch e.Kind {
	case capability.PaymentCheckoutPaid, capability.PaymentCheckoutExpired, capability.PaymentCheckoutFailed:
		return tx.operator.applyCheckoutPaymentTx(ctx, tx, e)
	case capability.PaymentPageCompleted:
		return tx.operator.bindCompletedPageTx(ctx, tx, e)
	case capability.PaymentCardSetup:
		if e.CustomerID == "" {
			return result, ErrInvalidInput
		}
		res, err := tx.execContext(ctx, `INSERT INTO payment_setups(payment_mode,intent_id,customer_id,created_at) VALUES (?,?,?,?) ON CONFLICT(payment_mode,intent_id) DO NOTHING`, e.Mode, e.ObjectID, e.CustomerID, e.Created)
		if err == nil {
			n, rowsErr := res.RowsAffected()
			result.Replayed = n == 0
			err = rowsErr
		}
		return result, err
	case capability.PaymentCardPaid, capability.PaymentCardFailed:
		var checkout paymentCheckout
		var customer string
		// safety: the refill id in the payment's metadata finds the refill, and the committed row, not the metadata,
		// decides the team, amount and payer; a refill funds at most one payment.
		err := tx.QueryRowContext(ctx, `SELECT id,team,kind,payment_id,currency,amount_minor,amount_micro,paid_at,closed_at,grant_id,customer_id FROM credit_checkouts WHERE payment_mode=? AND id=? AND kind=?`+tx.forUpdate(), e.Mode, e.RefillID, checkoutKindRefill).Scan(append(checkout.scan(), &customer)...)
		if errors.Is(err, sql.ErrNoRows) {
			return result, authorityError(apierr.NotFound, "committed refill is unavailable")
		}
		if err != nil {
			return result, err
		}
		if checkout.currency != e.Currency || e.CustomerID != customer || e.Kind == capability.PaymentCardPaid && checkout.amount != e.AmountMinor ||
			checkout.payment != "" && checkout.payment != e.PaymentID {
			return result, authorityError(apierr.Conflict, "payment facts conflict with committed refill")
		}
		checkout.result.Payment, checkout.result.GrantID = e.PaymentID, checkout.grant.String
		checkout.result.Replayed = checkout.paid.Valid
		err = applyPaymentOutcomeTx(ctx, tx, e, &checkout)
		return checkout.result, err
	case capability.PaymentWarningEvent:
		// safety: an actionable warning holds only the team its payment funded; reconciliation places that hold.
		if e.ObjectID == "" || e.PaymentID == "" {
			return result, fmt.Errorf("%w: a warning names itself and its payment", ErrInvalidInput)
		}
		if _, err := tx.execContext(ctx, `INSERT INTO payment_warnings (payment_mode, warning_id, payment_intent, actionable,
	    created_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT (payment_mode,warning_id) DO NOTHING`,
			e.Mode, e.ObjectID, e.PaymentID, boolInt(e.Actionable), tx.now(ctx).UnixNano()); err != nil {
			return result, err
		}
		var payment string
		if err := tx.QueryRowContext(ctx, `SELECT payment_intent FROM payment_warnings WHERE payment_mode=? AND warning_id=?`, e.Mode, e.ObjectID).Scan(&payment); err != nil {
			return result, err
		}
		if payment != e.PaymentID {
			return result, fmt.Errorf("%w: warning identity names another payment", ErrCreditGrantConflict)
		}
		if !e.Actionable {
			return result, nil
		}
		if _, err := tx.execContext(ctx, "UPDATE payment_warnings SET actionable=1 WHERE payment_mode=? AND warning_id=?", e.Mode, e.ObjectID); err != nil {
			return result, err
		}
		result.Payment = payment
		return result, nil
	case capability.PaymentRefund:
		result.Payment = e.PaymentID
		return result, recordPaymentConsequenceTx(ctx, tx, e, "refund")
	case capability.PaymentDisputeOpened, capability.PaymentDisputeClosed:
		result.Payment = e.PaymentID
		if e.Kind == capability.PaymentDisputeOpened {
			e.Status = cardRefundPending
		} else if e.Status == "lost" {
			e.Status = cardRefundSucceeded
		} else {
			e.Status = "canceled"
		}
		return result, recordPaymentConsequenceTx(ctx, tx, e, "dispute")

	}
	return result, authorityError(apierr.BadRequest, "unsupported provider outcome")
}

func (op *Operator) bindCompletedPageTx(ctx context.Context, tx *storeTx, e capability.PaymentEvent) (PaymentSettlement, error) {
	var team, customer string
	err := tx.QueryRowContext(ctx, `SELECT team,customer_id FROM payment_correlations WHERE payment_mode=? AND provider_id=? AND kind='setup_session'`, e.Mode, e.ObjectID).Scan(&team, &customer)
	if errors.Is(err, sql.ErrNoRows) {
		return PaymentSettlement{}, authorityError(apierr.NotFound, "committed payment page is unavailable")
	}
	if err != nil {
		return PaymentSettlement{}, err
	}
	if customer != e.CustomerID {
		return PaymentSettlement{}, authorityError(apierr.Conflict, "completed page names another customer")
	}
	if err := lockPaymentTeamTx(ctx, tx, Team(team)); err != nil {
		return PaymentSettlement{}, err
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
	_, err = tx.execContext(ctx, `INSERT INTO payment_correlations(payment_mode,provider_id,kind,team,customer_id) VALUES (?,?,'setup',?,?)`, e.Mode, e.IntentID, team, customer)
	return result, err
}

// RecordsPaymentParent reports whether this deployment committed the checkout, refill, page or intent an event concerns.
func (o *Operator) RecordsPaymentParent(ctx context.Context, r capability.PaymentRoute) (bool, error) {
	var found int
	err := o.s.queryRow(ctx, `SELECT CASE WHEN
 EXISTS(SELECT 1 FROM credit_checkouts WHERE payment_mode=? AND (session_id=? OR payment_id<>'' AND payment_id=? OR kind='refill' AND id=?))
 OR EXISTS(SELECT 1 FROM payment_correlations WHERE payment_mode=? AND provider_id IN (?,?,?))
 THEN 1 ELSE 0 END`, r.Mode, r.CheckoutID, r.PaymentID, r.RefillID, r.Mode, r.CheckoutID, r.PaymentID, r.ObjectID).Scan(&found)
	return found == 1, err
}

func (op *Operator) reconcileSetupStateTx(ctx context.Context, tx *storeTx, mode capability.PaymentMode, intent string) (Team, error) {
	var card Card
	var created int64
	var applied int
	err := tx.QueryRowContext(ctx, `SELECT customer_id,created_at,applied FROM payment_setups WHERE payment_mode=? AND intent_id=?`, mode, intent).Scan(&card.Customer, &created, &applied)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var team Team
	var customer string
	err = tx.QueryRowContext(ctx, `SELECT team,customer_id FROM payment_correlations WHERE payment_mode=? AND provider_id=? AND kind='setup'`, mode, intent).Scan(&team, &customer)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if customer != card.Customer {
		return "", authorityError(apierr.Conflict, "setup names another customer")
	}
	if applied != 0 {
		return team, nil
	}
	card.AddedAt = time.Unix(created, 0)
	if err := saveCardTx(ctx, tx, team, card, intent, auditActor{system: auditPaymentProvider}, tx.now(ctx)); err != nil && !errors.Is(err, errStaleCardSetup) {
		return "", err
	}
	_, err = tx.execContext(ctx, `UPDATE payment_setups SET applied=1 WHERE payment_mode=? AND intent_id=?`, mode, intent)
	return team, err
}
