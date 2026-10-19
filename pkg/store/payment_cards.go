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

func cardRefusalTx(ctx context.Context, tx *storeTx, team Team, paymentIntent, fingerprint string) (cardRefusal, error) {
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

// safety: a paid payment claims its card for its team with one insert, so two teams' first payments by one card
// settle under their own ledger locks and still only one of them wins the pin; a warning outranks the pin because it
// also holds the team.
func claimCardTx(ctx context.Context, tx *storeTx, team Team, paymentIntent, fingerprint string, now time.Time) (cardRefusal, error) {
	warning, err := paymentWarningTx(ctx, tx, paymentIntent, fingerprint)
	if err != nil || warning != "" {
		return cardRefusal{reason: CardRefusedWarning, warning: warning}, err
	}
	if fingerprint == "" {
		return cardRefusal{}, nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO payment_cards (fingerprint, first_team, payment_intent, pinned_at)
	    VALUES (?, ?, ?, ?) ON CONFLICT (fingerprint) DO NOTHING`, fingerprint, string(team), paymentIntent, now.UnixNano()); err != nil {
		return cardRefusal{}, err
	}
	var first Team
	if err := tx.QueryRowContext(ctx, `SELECT first_team FROM payment_cards WHERE fingerprint = ?`, fingerprint).Scan(&first); err != nil {
		return cardRefusal{}, err
	}
	if first != team {
		return cardRefusal{reason: CardRefusedPinned}, nil
	}
	return cardRefusal{}, nil
}

// safety: a blocked card is forgotten on every team that saved it, and its refill turned off, so no refill ever
// charges it again.
func forgetCardTx(ctx context.Context, tx *storeTx, fingerprint, reason string, now time.Time) error {
	if fingerprint == "" {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT name, card_payment_method FROM teams WHERE card_fingerprint = ?`, fingerprint)
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
		    card_brand = '', card_last4 = '', refill_enabled = 0 WHERE name = ? AND card_fingerprint = ?`, string(s.team), fingerprint); err != nil {
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
