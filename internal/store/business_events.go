package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Business event kinds returned by the audit reader.
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

// BusinessEvent is one durable fact about an account or a team: an
// admission, a purchase, a grant, a hold. It is written in the transaction
// that makes the fact true, so the table never records a change that rolled
// back and never misses one that committed.
//
// SubjectID names the event within its team and kind, and a second event
// with the same team, kind and subject is dropped for idempotent facts. Release
// and mutation kinds, including run.deletion_authorized, retain every occurrence.
// A replayed webhook is idempotent. The first successful run is a durable team
// fact independent of audit retention.
type BusinessEvent struct {
	ID        string
	At        time.Time
	Team      Team
	Account   string
	Kind      string
	SubjectID string
	RunID     string
	Actor     string
	Attrs     map[string]any
}

// BusinessEvents returns at most limit events, newest first, with IDs breaking
// timestamp ties. An empty kind selects every kind. The limit is 1 to 1000.
// A nonempty beforeID resumes strictly before that event in the same selection.
func (t *Tenant) BusinessEvents(ctx context.Context, kind string, limit int, beforeID string) (_ []BusinessEvent, err error) {
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("%w: event limit is 1 to 1000", ErrInvalidInput)
	}
	query := `SELECT id, ts, team,
 CASE WHEN account<>'' AND NOT EXISTS(SELECT 1 FROM accounts WHERE accounts.id=audit_events.account) THEN 'deleted account' ELSE account END,
 kind,
 CASE WHEN subject_is_account=1 AND NOT EXISTS(SELECT 1 FROM accounts WHERE accounts.id=audit_events.subject_id) THEN 'deleted account' ELSE subject_id END,
 CASE WHEN actor_is_account=1 AND NOT EXISTS(SELECT 1 FROM accounts WHERE accounts.id=audit_events.actor) THEN 'deleted account' ELSE actor END,
 attrs, run_id FROM audit_events WHERE team = ?`
	args := []any{string(t.team)}
	if kind != "" {
		query, args = query+` AND kind = ?`, append(args, kind)
	}
	if beforeID != "" {
		cursorQuery := `SELECT ts FROM audit_events WHERE team = ? AND id = ?`
		cursorArgs := []any{string(t.team), beforeID}
		if kind != "" {
			cursorQuery, cursorArgs = cursorQuery+` AND kind = ?`, append(cursorArgs, kind)
		}
		var ts int64
		if err := t.s.queryRow(ctx, cursorQuery, cursorArgs...).Scan(&ts); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("%w: event cursor does not match this selection", ErrInvalidInput)
			}
			return nil, err
		}
		query, args = query+` AND (ts, id) < (?, ?)`, append(args, ts, beforeID)
	}
	rows, err := t.s.query(ctx, query+` ORDER BY ts DESC, id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []BusinessEvent
	for rows.Next() {
		var ev BusinessEvent
		var ts int64
		var team, attrs string
		if err := rows.Scan(&ev.ID, &ts, &team, &ev.Account, &ev.Kind, &ev.SubjectID, &ev.Actor, &attrs, &ev.RunID); err != nil {
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
