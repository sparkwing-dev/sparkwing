package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
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
// with the same team, kind and subject is dropped for idempotent facts. Release
// and mutation kinds, including run.deletion_authorized, retain every occurrence.
// A replayed webhook is idempotent. An empty SubjectID is a once-per-team fact,
// the way [BusinessEventFirstRunSucceeded] is recorded.
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

// RecordBusinessEvent writes ev inside tx, the transaction making the
// change it describes. A zero At is now, and Attrs is stored as JSON of at
// most 4 KiB. An event whose team, kind and subject are already recorded
// writes nothing, except team.mutated, secret.released, git_credential.released
// and run.deletion_authorized, which append every occurrence.
func (op *Operator) RecordBusinessEvent(tx *storeTx, ev BusinessEvent) error {
	if strings.TrimSpace(ev.Kind) == "" {
		return fmt.Errorf("%w: a business event names its kind", ErrInvalidInput)
	}
	if ev.At.IsZero() {
		ev.At = tx.now()
	}
	attrs := []byte("{}")
	if len(ev.Attrs) > 0 {
		var err error
		if attrs, err = json.Marshal(ev.Attrs); err != nil {
			return fmt.Errorf("business event attrs: %w", err)
		}
		var fields map[string]any
		decoder := json.NewDecoder(bytes.NewReader(attrs))
		decoder.UseNumber()
		if err := decoder.Decode(&fields); err != nil {
			return err
		}
		stripAuditPersonalFields(fields)
		attrs, err = json.Marshal(fields)
		if err != nil {
			return err
		}
		if len(attrs) > maxBusinessEventAttrsBytes {
			return fmt.Errorf("%w: business event attrs are %d bytes, over %d",
				ErrInvalidInput, len(attrs), maxBusinessEventAttrsBytes)
		}
	}
	// Email addresses have no durable home in an append-only record.
	identityFields, err := json.Marshal([]string{string(ev.Team), ev.Kind, ev.Account, ev.SubjectID, ev.Actor, ev.RunID})
	if err != nil {
		return err
	}
	if bytes.ContainsRune(identityFields, '@') || bytes.ContainsRune(attrs, '@') {
		return fmt.Errorf("%w: audit events contain identifiers, never email addresses", ErrInvalidInput)
	}
	var id string
	switch ev.Kind {
	case "team.mutated", "secret.released", "git_credential.released", "run.deletion_authorized":
		var err error
		id, err = newCreditID("event")
		if err != nil {
			return err
		}
	default:
		identity, err := json.Marshal([]string{string(ev.Team), ev.Kind, ev.SubjectID})
		if err != nil {
			return err
		}
		sum := sha256.Sum256(identity)
		id = "fact:" + hex.EncodeToString(sum[:])
	}
	var team any = string(ev.Team)
	if ev.Team == "" {
		team = nil
	}
	_, err = tx.Exec(`
		INSERT INTO audit_events (id, ts, team, account, kind, subject_id, actor, attrs, run_id, actor_is_account, subject_is_account)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?,
        CASE WHEN EXISTS(SELECT 1 FROM accounts WHERE id=?) THEN 1 ELSE 0 END,
        CASE WHEN EXISTS(SELECT 1 FROM accounts WHERE id=?) THEN 1 ELSE 0 END)
		ON CONFLICT DO NOTHING`,
		id, ev.At.UnixNano(), team, ev.Account, ev.Kind, ev.SubjectID,
		truncate(ev.Actor, 200), string(attrs), ev.RunID, ev.Actor, ev.SubjectID)
	return err
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

// Free-form explanations and card/person details stay in mutable operational records.
func stripAuditPersonalFields(value any) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			switch strings.ToLower(key) {
			case "reason", "message", "failure", "email", "last4", "brand", "github_user_id":
				delete(v, key)
			default:
				stripAuditPersonalFields(child)
			}
		}
	case []any:
		for _, child := range v {
			stripAuditPersonalFields(child)
		}
	}
}
