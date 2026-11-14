package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// RunEventDeliveryLease is how long a claimed delivery stays with its sender.
// It outlasts one attempt by a wide margin, so a lease only expires when the
// sender stopped; the row is then claimed again and resent.
const RunEventDeliveryLease = 2 * time.Minute

// RunEventDelivery is one claimed send of a stored run event to a subscription.
type RunEventDelivery struct {
	Team      Team
	EventID   string
	RunID     string
	EventType string
	Body      []byte
	// Attempts counts this attempt; completion is fenced on it.
	Attempts int64
	// Subscription carries the destination URL and the sealed signing secret.
	Subscription RunEventSubscription
}

// RunEventOutcome is the result of one attempt. A failed attempt is retried
// after Delay unless that lands past the delivery's deadline.
type RunEventOutcome struct {
	Delivered bool
	// Status is the HTTP status, or 0 when no response arrived.
	Status int
	// Code is a fixed safe error code, never receiver text.
	Code  string
	Delay time.Duration
}

// ClaimRunEventDeliveries moves up to limit due deliveries to sending under a
// lease and returns them. A due delivery is pending with next_at reached, or
// sending with an expired lease. Deliveries already past their deadline are
// marked failed instead.
//
// safety: each row moves with an UPDATE fenced on the state and attempt count
// just read, so two replicas that read the same row cannot both claim it.
func (op *Operator) ClaimRunEventDeliveries(ctx context.Context, limit int) ([]RunEventDelivery, error) {
	if limit <= 0 {
		return nil, ErrInvalidInput
	}
	var out []RunEventDelivery
	err := op.s.inTx(ctx, func(tx *storeTx) error {
		out = nil
		now := tx.now(ctx).UnixNano()
		if _, err := tx.execContext(ctx, `UPDATE run_event_deliveries SET state='failed',finished_at=?,send_until=NULL,last_error=CASE WHEN state='sending' THEN 'timeout' ELSE last_error END WHERE state IN ('pending','sending') AND next_at<=? AND deadline_at<=? AND (state='pending' OR send_until<=?)`, now, now, now, now); err != nil {
			return err
		}
		rows, err := tx.queryContext(ctx, `SELECT d.team,d.subscription_id,d.event_id,d.run_id,d.event_type,d.body,d.attempts,d.state,s.url,s.secret FROM run_event_deliveries d JOIN run_event_subscriptions s ON s.team=d.team AND s.id=d.subscription_id WHERE d.state IN ('pending','sending') AND d.next_at<=? AND (d.state='pending' OR d.send_until<=?) ORDER BY d.next_at,d.team,d.subscription_id,d.event_id LIMIT ?`, now, now, limit)
		if err != nil {
			return err
		}
		type due struct {
			d     RunEventDelivery
			state string
		}
		var candidates []due
		for rows.Next() {
			var c due
			if err := rows.Scan(&c.d.Team, &c.d.Subscription.ID, &c.d.EventID, &c.d.RunID, &c.d.EventType, &c.d.Body, &c.d.Attempts, &c.state, &c.d.Subscription.URL, &c.d.Subscription.Secret); err != nil {
				return errors.Join(err, rows.Close())
			}
			c.d.Subscription.Team = c.d.Team
			candidates = append(candidates, c)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		until := now + RunEventDeliveryLease.Nanoseconds()
		for _, c := range candidates {
			result, err := tx.execContext(ctx, `UPDATE run_event_deliveries SET state='sending',attempts=attempts+1,send_until=? WHERE team=? AND subscription_id=? AND event_id=? AND state=? AND attempts=?`, until, c.d.Team, c.d.Subscription.ID, c.d.EventID, c.state, c.d.Attempts)
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if n == 1 {
				c.d.Attempts++
				out = append(out, c.d)
			}
		}
		return nil
	})
	return out, err
}

// FinishRunEventDelivery records one attempt's outcome. It returns false,
// changing nothing, when the claim is no longer current: the lease expired
// and another sender claimed the row, or the row was redelivered or deleted.
func (op *Operator) FinishRunEventDelivery(ctx context.Context, d RunEventDelivery, outcome RunEventOutcome) (bool, error) {
	if !outcome.Delivered && (outcome.Code == "" || outcome.Delay < 0) {
		return false, ErrInvalidInput
	}
	var status any
	if outcome.Status != 0 {
		status = outcome.Status
	}
	var applied bool
	err := op.s.inTx(ctx, func(tx *storeTx) error {
		now := tx.now(ctx).UnixNano()
		var deadline int64
		err := tx.QueryRowContext(ctx, `SELECT deadline_at FROM run_event_deliveries WHERE team=? AND subscription_id=? AND event_id=? AND state='sending' AND attempts=?`, d.Team, d.Subscription.ID, d.EventID, d.Attempts).Scan(&deadline)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		state, next, finished, code := "succeeded", any(nil), any(now), ""
		if !outcome.Delivered {
			state, next, finished, code = "pending", now+outcome.Delay.Nanoseconds(), nil, outcome.Code
			if next.(int64) > deadline {
				state, finished = "failed", now
			}
		}
		result, err := tx.execContext(ctx, `UPDATE run_event_deliveries SET state=?,next_at=COALESCE(?,next_at),finished_at=?,send_until=NULL,last_status=?,last_error=? WHERE team=? AND subscription_id=? AND event_id=? AND state='sending' AND attempts=?`, state, next, finished, status, code, d.Team, d.Subscription.ID, d.EventID, d.Attempts)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		applied = n == 1
		return err
	})
	return applied, err
}
