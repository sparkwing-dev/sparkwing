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
	rows, err := tx.QueryContext(ctx, `SELECT name FROM teams WHERE card_fingerprint = ? ORDER BY name`, fingerprint)
	if err != nil {
		return err
	}
	var teams []Team
	for rows.Next() {
		var team Team
		if err := rows.Scan(&team); err != nil {
			return errors.Join(err, rows.Close())
		}
		teams = append(teams, team)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, team := range teams {
		if err := forgetTeamCardTx(ctx, tx, team, fingerprint, reason, now); err != nil {
			return err
		}
	}
	return nil
}

func forgetTeamCardTx(ctx context.Context, tx *storeTx, team Team, fingerprint, reason string, now time.Time) error {
	var method string
	err := tx.QueryRowContext(ctx, `SELECT card_payment_method FROM teams WHERE name = ? AND card_fingerprint = ? AND card_fingerprint <> ''`, string(team), fingerprint).Scan(&method)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE teams SET card_customer = '', card_payment_method = '', card_fingerprint = '',
	    card_brand = '', card_last4 = '', refill_enabled = 0 WHERE name = ? AND card_fingerprint = ?`, string(team), fingerprint); err != nil {
		return err
	}
	return RecordBusinessEvent(tx, BusinessEvent{
		Team: team, Kind: BusinessEventCardBlocked, SubjectID: method, Actor: "payment_provider", At: now,
		Attrs: map[string]any{"reason": reason},
	})
}
