package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// PaymentSettlement reports committed credit without accepting caller-selected credits.
type PaymentSettlement struct {
	Team        Team
	CheckoutID  string
	GrantID     string
	AmountMicro int64
	Replayed    bool
}

// SettlePayment settles verified provider events against committed billing facts.
func (s *Store) SettlePayment(ctx context.Context, proof capability.VerifiedPaymentEvent) (PaymentSettlement, error) {
	if proof == (capability.VerifiedPaymentEvent{}) {
		return PaymentSettlement{}, authorityError(apierr.Unauthenticated, "payment verification is required")
	}
	return s.settlePaymentFacts(ctx, proof.Event())
}

type paymentCheckout struct {
	result            PaymentSettlement
	payment, currency string
	amount            int64
	paid, closed      sql.NullInt64
	grant             sql.NullString
}

func (s *Store) settlePaymentFacts(ctx context.Context, event capability.PaymentEvent) (PaymentSettlement, error) {
	if event.Kind != capability.PaymentCheckoutPaid && event.Kind != capability.PaymentCheckoutExpired && event.Kind != capability.PaymentCheckoutFailed {
		return s.settleProviderPaymentFacts(ctx, event)
	}
	if event.EventID == "" || event.CheckoutID == "" || event.AmountMinor <= 0 || event.Currency == "" || (event.Mode != capability.PaymentTest && event.Mode != capability.PaymentLive) {
		return PaymentSettlement{}, authorityError(apierr.BadRequest, "invalid verified payment facts")
	}
	if event.Kind != capability.PaymentCheckoutPaid && event.Kind != capability.PaymentCheckoutExpired && event.Kind != capability.PaymentCheckoutFailed {
		return PaymentSettlement{}, authorityError(apierr.BadRequest, "unsupported payment outcome")
	}
	if event.Kind == capability.PaymentCheckoutPaid && event.PaymentID == "" {
		return PaymentSettlement{}, authorityError(apierr.BadRequest, "paid checkout has no payment identity")
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return PaymentSettlement{}, err
	}
	defer rollbackOrLog(tx)
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return PaymentSettlement{}, err
	}
	if err := configuredPaymentModeTx(ctx, tx, event.Mode); err != nil {
		return PaymentSettlement{}, err
	}
	var checkout paymentCheckout
	err = tx.QueryRowContext(ctx, `SELECT id,team,payment_id,currency,amount_minor,amount_micro,paid_at,closed_at,grant_id FROM credit_checkouts WHERE payment_mode = ? AND session_id = ? AND session_id <> id`+tx.forUpdate(), string(event.Mode), event.CheckoutID).Scan(&checkout.result.CheckoutID, &checkout.result.Team, &checkout.payment, &checkout.currency, &checkout.amount, &checkout.result.AmountMicro, &checkout.paid, &checkout.closed, &checkout.grant)
	if errors.Is(err, sql.ErrNoRows) {
		return PaymentSettlement{}, authorityError(apierr.NotFound, "committed checkout is unavailable")
	}
	if err != nil {
		return PaymentSettlement{}, err
	}
	if checkout.currency != event.Currency || checkout.amount != event.AmountMinor || (event.PaymentID != "" && checkout.payment != "" && checkout.payment != event.PaymentID) {
		return PaymentSettlement{}, authorityError(apierr.Conflict, "payment facts conflict with committed checkout")
	}
	if event.PaymentID != "" {
		var used int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM credit_checkouts WHERE payment_mode=? AND payment_id=? AND id<>?", string(event.Mode), event.PaymentID, checkout.result.CheckoutID).Scan(&used); err != nil {
			return PaymentSettlement{}, err
		}
		if used != 0 {
			return PaymentSettlement{}, errors.Join(ErrCreditGrantConflict, authorityError(apierr.Conflict, "payment identity belongs to another checkout"))
		}
		grant, found, err := paidGrantForPaymentTx(ctx, tx, event.PaymentID)
		if err != nil {
			return PaymentSettlement{}, err
		}
		if found && (grant.Team != checkout.result.Team || grant.ID != checkout.grant.String) {
			return PaymentSettlement{}, errors.Join(ErrCreditGrantConflict, authorityError(apierr.Conflict, "payment identity already funded another grant"))
		}
	}
	body, err := json.Marshal(event)
	if err != nil {
		return PaymentSettlement{}, err
	}
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	replayed, err := paymentEventReplayTx(ctx, tx, event, digest)
	if err != nil {
		return PaymentSettlement{}, err
	}
	checkout.result.GrantID = checkout.grant.String
	checkout.result.Replayed = replayed || checkout.paid.Valid
	if replayed {
		return checkout.result, tx.Commit()
	}
	if err := applyPaymentOutcomeTx(ctx, tx, event, &checkout); err != nil {
		return PaymentSettlement{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO payment_events(team,payment_mode,event_id,checkout_id,payment_id,event_kind,event_digest,grant_id,processed_at) VALUES (?,?,?,?,?,?,?,?,?)`, string(checkout.result.Team), string(event.Mode), event.EventID, checkout.result.CheckoutID, event.PaymentID, string(event.Kind), digest, optionalString(checkout.result.GrantID), tx.now().UnixNano())
	if err != nil {
		return PaymentSettlement{}, err
	}
	if err := tx.Commit(); err != nil {
		return PaymentSettlement{}, err
	}
	return checkout.result, nil
}

func configuredPaymentModeTx(ctx context.Context, tx *storeTx, eventMode capability.PaymentMode) error {
	mode, err := paymentModeTx(ctx, tx)
	if err != nil {
		return err
	}
	if mode != eventMode {
		return authorityError(apierr.Forbidden, "payment mode does not match configuration")
	}
	return nil
}

func paymentModeTx(ctx context.Context, tx *storeTx) (capability.PaymentMode, error) {
	var encoded []byte
	if err := tx.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = 'payment_mode'"+tx.forShare()).Scan(&encoded); err != nil {
		return "", authorityError(apierr.Unavailable, "payment mode is not configured")
	}
	var mode capability.PaymentMode
	if json.Unmarshal(encoded, &mode) != nil || (mode != capability.PaymentTest && mode != capability.PaymentLive) {
		return "", authorityError(apierr.Unavailable, "configured payment mode is invalid")
	}
	return mode, nil
}

func paymentEventReplayTx(ctx context.Context, tx *storeTx, event capability.PaymentEvent, digest string) (bool, error) {
	var existing string
	err := tx.QueryRowContext(ctx, "SELECT event_digest FROM payment_events WHERE payment_mode = ? AND event_id = ?", string(event.Mode), event.EventID).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if existing != digest {
		return false, authorityError(apierr.Conflict, "payment event identity has conflicting content")
	}
	return true, nil
}

func applyPaymentOutcomeTx(ctx context.Context, tx *storeTx, event capability.PaymentEvent, checkout *paymentCheckout) error {
	now := tx.now().UnixNano()
	if checkout.payment == "" && event.PaymentID != "" {
		if _, err := tx.ExecContext(ctx, "UPDATE credit_checkouts SET payment_id = ? WHERE team = ? AND id = ? AND payment_mode = ?", event.PaymentID, string(checkout.result.Team), checkout.result.CheckoutID, string(event.Mode)); err != nil {
			return err
		}
	}
	if checkout.paid.Valid {
		return nil
	}
	if event.Kind != capability.PaymentCheckoutPaid {
		if checkout.closed.Valid {
			return nil
		}
		if _, err := tx.ExecContext(ctx, "UPDATE credit_checkouts SET closed_at = ? WHERE team = ? AND id = ? AND payment_mode = ?", now, string(checkout.result.Team), checkout.result.CheckoutID, string(event.Mode)); err != nil {
			return err
		}
		kind := BusinessEventCheckoutFailed
		if event.Kind == capability.PaymentCheckoutExpired {
			kind = BusinessEventCheckoutExpired
		}
		return RecordBusinessEvent(tx, BusinessEvent{At: tx.now(), Team: checkout.result.Team, Kind: kind, SubjectID: checkout.result.CheckoutID, Actor: "payment_provider", Attrs: map[string]any{"session_id": event.CheckoutID, "payment_mode": event.Mode, "event_id": event.EventID}})
	}
	if err := refuseWarnedPaymentTx(ctx, tx, event.PaymentID); err != nil {
		return err
	}
	grant, err := newCreditID("paid")
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO credit_grants(id,team,kind,amount_micro,reference,created_by,created_at) VALUES (?,?,'paid',?,?,?,?)`, grant, string(checkout.result.Team), checkout.result.AmountMicro, "payment:"+string(event.Mode)+":"+checkout.result.CheckoutID, "payment_provider", now)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE credit_checkouts SET paid_at = ?, grant_id = ?, closed_at = COALESCE(closed_at,?) WHERE team = ? AND id = ? AND payment_mode = ?", now, grant, now, string(checkout.result.Team), checkout.result.CheckoutID, string(event.Mode))
	if err != nil {
		return err
	}
	checkout.result.GrantID = grant
	if err := recordGrantEventTx(tx, CreditGrant{ID: grant, Team: checkout.result.Team, Kind: CreditGrantPaid, AmountMicro: checkout.result.AmountMicro, Reference: "payment:" + string(event.Mode) + ":" + checkout.result.CheckoutID, CreatedBy: "payment_provider", CreatedAt: tx.now()}, event.CheckoutID, ""); err != nil {
		return err
	}
	room, err := headroomTx(ctx, tx, checkout.result.Team, tx.now())
	if err != nil {
		return err
	}
	if room > 0 {
		return clearTeamCreditExhaustedTx(ctx, tx, checkout.result.Team, now)
	}
	return nil
}
