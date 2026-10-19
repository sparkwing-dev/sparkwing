package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const (
	// CardRefusedWarning refuses a payment or card that an actionable early fraud warning blocked.
	CardRefusedWarning = "warning"
	// CardRefusedPinned refuses a card whose first successful payment funded another team.
	CardRefusedPinned = "card_pinned"

	BusinessEventCardRefused = "card.refused"
	BusinessEventCardBlocked = "card.blocked"
)

type cardRefusal struct {
	reason, warning string
}

// safety: callers hold the ledger lock, so the refusal and the pin that follows it cannot interleave with another
// team's first payment by the same card; a warning outranks the pin because it also holds the team.
func refuseCardTx(ctx context.Context, tx *storeTx, team Team, paymentIntent, fingerprint string) (cardRefusal, error) {
	warning, err := paymentWarningTx(ctx, tx, paymentIntent, fingerprint)
	if err != nil || warning != "" {
		return cardRefusal{reason: CardRefusedWarning, warning: warning}, err
	}
	if fingerprint == "" {
		return cardRefusal{}, nil
	}
	var first Team
	err = tx.QueryRowContext(ctx, `SELECT first_team FROM payment_cards WHERE fingerprint = ?`, fingerprint).Scan(&first)
	if errors.Is(err, sql.ErrNoRows) || err == nil && first == team {
		return cardRefusal{}, nil
	}
	if err != nil {
		return cardRefusal{}, err
	}
	return cardRefusal{reason: CardRefusedPinned}, nil
}

func pinCardTx(ctx context.Context, tx *storeTx, team Team, paymentIntent, fingerprint string, now time.Time) error {
	if fingerprint == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO payment_cards (fingerprint, first_team, payment_intent, pinned_at)
	    VALUES (?, ?, ?, ?) ON CONFLICT (fingerprint) DO NOTHING`, fingerprint, string(team), paymentIntent, now.UnixNano())
	return err
}

// safety: a card is forgotten wherever it is saved, so collection never charges it again for this or any team.
func forgetCardTx(ctx context.Context, tx *storeTx, fingerprint string, only Team, reason string, now time.Time) error {
	if fingerprint == "" {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT name, card_payment_method FROM teams WHERE card_fingerprint = ? AND (? = '' OR name = ?)`,
		fingerprint, string(only), string(only))
	if err != nil {
		return err
	}
	type saved struct {
		team   Team
		method string
	}
	var found []saved
	for rows.Next() {
		var s saved
		if err := rows.Scan(&s.team, &s.method); err != nil {
			return errors.Join(err, rows.Close())
		}
		found = append(found, s)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, s := range found {
		if _, err := tx.ExecContext(ctx, `UPDATE teams SET card_customer = '', card_payment_method = '', card_fingerprint = '',
		    card_brand = '', card_last4 = '' WHERE name = ? AND card_fingerprint = ?`, string(s.team), fingerprint); err != nil {
			return err
		}
		if err := RecordBusinessEvent(tx, BusinessEvent{
			Team: s.team, Kind: BusinessEventCardBlocked, SubjectID: s.method, Actor: "payment_provider", At: now,
			Attrs: map[string]any{"reason": reason},
		}); err != nil {
			return err
		}
	}
	return nil
}

// safety: a refused collection payment counts as a failed attempt, so the charge stays owed and is retried
// on another card, and the card is forgotten so collection does not charge it again.
func refusePinnedCardTx(ctx context.Context, tx *storeTx, team Team, p CardPayment, status string, now time.Time) error {
	if status == CardAttemptLive {
		if _, _, err := failCardAttemptTx(ctx, tx, p.AttemptID, p.PaymentIntent, CardRefusedPinned, now); err != nil {
			return err
		}
	}
	if err := forgetCardTx(ctx, tx, p.Fingerprint, team, CardRefusedPinned, now); err != nil {
		return err
	}
	return queueCardRefundTx(ctx, tx, team, p, CardRefusedPinned, now)
}
