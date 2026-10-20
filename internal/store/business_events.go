package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Business event kinds recorded by this package. A kind is a free-form
// string, so a caller may record one not listed here.
const (
	BusinessEventAccountAdmitted   = "account.admitted"
	BusinessEventTeamCreated       = "team.created"
	BusinessEventCheckoutOpened    = "checkout.opened"
	BusinessEventCheckoutPaid      = "checkout.paid"
	BusinessEventCheckoutFailed    = "checkout.failed"
	BusinessEventCheckoutExpired   = "checkout.expired"
	BusinessEventCreditGranted     = "credit.granted"
	BusinessEventCreditReversed    = "credit.reversed"
	BusinessEventTeamFrozen        = "team.frozen"
	BusinessEventTeamUnfrozen      = "team.unfrozen"
	BusinessEventFirstRunSucceeded = "run.first_succeeded"
)

const maxBusinessEventAttrsBytes = 4096

// BusinessEvent is one durable fact about an account or a team: an
// admission, a purchase, a grant, a hold. It is written in the transaction
// that makes the fact true, so the table never records a change that rolled
// back and never misses one that committed.
//
// SubjectID names the event within its team and kind, and a second event
// with the same team, kind and subject is dropped. That makes a replayed
// webhook idempotent, and an empty SubjectID a once-per-team fact, the way
// [BusinessEventFirstRunSucceeded] is recorded.
type BusinessEvent struct {
	ID        string
	At        time.Time
	Team      Team
	Account   string
	Kind      string
	SubjectID string
	Actor     string
	Attrs     map[string]any
}

// RecordBusinessEvent writes ev inside tx, the transaction making the
// change it describes. A zero At is now, and Attrs is stored as JSON of at
// most 4 KiB. An event whose team, kind and subject are already recorded
// writes nothing.
func (op *Operator) RecordBusinessEvent(tx *storeTx, ev BusinessEvent) error {
	if strings.TrimSpace(ev.Kind) == "" {
		return fmt.Errorf("%w: a business event names its kind", ErrInvalidInput)
	}
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	attrs := []byte("{}")
	if len(ev.Attrs) > 0 {
		var err error
		if attrs, err = json.Marshal(ev.Attrs); err != nil {
			return fmt.Errorf("business event attrs: %w", err)
		}
		if len(attrs) > maxBusinessEventAttrsBytes {
			return fmt.Errorf("%w: business event attrs are %d bytes, over %d",
				ErrInvalidInput, len(attrs), maxBusinessEventAttrsBytes)
		}
	}
	id, err := newCreditID("event")
	if err != nil {
		return err
	}
	var team any = string(ev.Team)
	if ev.Team == "" {
		identity, err := json.Marshal([]string{ev.Kind, ev.SubjectID})
		if err != nil {
			return err
		}
		sum := sha256.Sum256(identity)
		id = "global:" + hex.EncodeToString(sum[:])
		team = nil
	}
	_, err = tx.Exec(`
		INSERT INTO business_events (id, ts, team, account, kind, subject_id, actor, attrs)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING`,
		id, ev.At.UnixNano(), team, ev.Account, ev.Kind, ev.SubjectID,
		truncate(ev.Actor, 200), string(attrs))
	return err
}

// BusinessEvents returns t's events of kind, or of every kind when kind is
// empty, oldest first.
func (t *Tenant) BusinessEvents(ctx context.Context, kind string) (_ []BusinessEvent, err error) {
	query := `SELECT id, ts, team, account, kind, subject_id, actor, attrs FROM business_events WHERE team = ?`
	args := []any{string(t.team)}
	if kind != "" {
		query, args = query+` AND kind = ?`, append(args, kind)
	}
	rows, err := t.s.query(ctx, query+` ORDER BY ts, id`, args...)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []BusinessEvent
	for rows.Next() {
		var ev BusinessEvent
		var ts int64
		var team, attrs string
		if err := rows.Scan(&ev.ID, &ts, &team, &ev.Account, &ev.Kind, &ev.SubjectID, &ev.Actor, &attrs); err != nil {
			return nil, err
		}
		ev.At, ev.Team = time.Unix(0, ts), Team(team)
		if err := json.Unmarshal([]byte(attrs), &ev.Attrs); err != nil {
			return nil, fmt.Errorf("business event %s attrs: %w", ev.ID, err)
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}
