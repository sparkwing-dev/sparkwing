package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runevents"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

// MaxRunEventSubscriptions bounds a team's subscriptions, because every run
// transition writes one delivery row for each one that matches.
const MaxRunEventSubscriptions = 20

const metaKeyRunEventSource = "run_event_source_id"

// ErrRunEventSubscriptionLimit refuses a subscription past MaxRunEventSubscriptions.
var ErrRunEventSubscriptionLimit = fmt.Errorf("store: a team holds at most %d run event subscriptions", MaxRunEventSubscriptions)

// ErrRunEventSubscriptionConflict refuses an idempotency key reused for a different subscription.
var ErrRunEventSubscriptionConflict = errors.New("store: this idempotency key created a different subscription")

// RunEventSubscription is a team's request to receive run events at a URL.
type RunEventSubscription struct {
	Team   Team
	ID     string
	URL    string
	Filter runevents.Filter
	// Secret is the sealed signing secret; it never holds plaintext.
	Secret           string
	Description      string
	CreatedPrincipal string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// SealedRunEventSubscription is a subscription write whose secret is sealed to its team and ID.
type SealedRunEventSubscription struct {
	team Team
	row  RunEventSubscription
}

// Row returns the sealed row for inspection.
func (s SealedRunEventSubscription) Row() RunEventSubscription { return s.row }

// RunEventSecretName is the bound-envelope name of a subscription's signing secret.
func RunEventSecretName(id string) string { return "run-event-subscription:" + id }

// SealRunEventSubscription seals row.Secret, a plaintext signing secret, to the team and row.ID.
func SealRunEventSubscription(cipher SecretSealer, team Team, row RunEventSubscription) (SealedRunEventSubscription, error) {
	if cipher == nil || row.ID == "" || row.Secret == "" {
		return SealedRunEventSubscription{}, ErrInvalidInput
	}
	value, err := cipher.SealBound(secrets.PurposeEventSigning, string(team), RunEventSecretName(row.ID), "", false, true, row.Secret)
	if err != nil {
		return SealedRunEventSubscription{}, err
	}
	if !secrets.IsBound(value) {
		return SealedRunEventSubscription{}, ErrInvalidInput
	}
	row.Secret = value
	return SealedRunEventSubscription{team: team, row: row}, nil
}

const runEventSubscriptionCols = `id,url,event_types,statuses,pipelines,repositories,secret,description,created_principal,created_at,updated_at`

func scanRunEventSubscription(team Team, row interface{ Scan(...any) error }) (RunEventSubscription, error) {
	s := RunEventSubscription{Team: team}
	var types, statuses, pipelines, repos string
	var created, updated int64
	if err := row.Scan(&s.ID, &s.URL, &types, &statuses, &pipelines, &repos, &s.Secret, &s.Description, &s.CreatedPrincipal, &created, &updated); err != nil {
		return RunEventSubscription{}, err
	}
	for _, field := range []struct {
		raw string
		out *[]string
	}{{types, &s.Filter.EventTypes}, {statuses, &s.Filter.Statuses}, {pipelines, &s.Filter.Pipelines}, {repos, &s.Filter.Repositories}} {
		if err := json.Unmarshal([]byte(field.raw), field.out); err != nil {
			return RunEventSubscription{}, fmt.Errorf("decode run event subscription %s: %w", s.ID, err)
		}
		if *field.out == nil {
			*field.out = []string{}
		}
	}
	s.CreatedAt, s.UpdatedAt = time.Unix(0, created).UTC(), time.Unix(0, updated).UTC()
	return s, nil
}

func runEventSubscriptionsTx(ctx context.Context, tx *storeTx, team Team) (_ []RunEventSubscription, err error) {
	rows, err := tx.queryContext(ctx, `SELECT `+runEventSubscriptionCols+` FROM run_event_subscriptions WHERE team=? ORDER BY id`, team)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	out := []RunEventSubscription{}
	for rows.Next() {
		s, err := scanRunEventSubscription(team, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func runEventSubscriptionTx(ctx context.Context, tx *storeTx, team Team, id string) (RunEventSubscription, error) {
	s, err := scanRunEventSubscription(team, tx.QueryRowContext(ctx, `SELECT `+runEventSubscriptionCols+` FROM run_event_subscriptions WHERE team=? AND id=?`, team, id))
	if errors.Is(err, sql.ErrNoRows) {
		return RunEventSubscription{}, ErrNotFound
	}
	return s, err
}

// safety: the store ID lives in settings, which restore copies, so a restored store keeps its event source.
func runEventSourceTx(ctx context.Context, tx *storeTx, team Team) (string, error) {
	minted := make([]byte, 16)
	if _, err := rand.Read(minted); err != nil {
		return "", err
	}
	if _, err := tx.execContext(ctx, `INSERT INTO settings(key,value) VALUES (?,?) ON CONFLICT(key) DO NOTHING`, metaKeyRunEventSource, []byte(hex.EncodeToString(minted))); err != nil {
		return "", err
	}
	var id []byte
	if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, metaKeyRunEventSource).Scan(&id); err != nil {
		return "", err
	}
	return runevents.Source(string(id), string(team)), nil
}

// RunEventDeliveryWindow is how long a delivery keeps retrying before it fails.
const RunEventDeliveryWindow = 24 * time.Hour

// runEventDelivery is one event queued for one subscription.
type runEventDelivery struct {
	SubscriptionID string
	EventID        string
	RunID          string
	EventType      string
	Body           []byte
}

// safety: a delivery keeps the exact body it will send, so a retry or redelivery never rebuilds the event.
func insertRunEventDeliveryTx(ctx context.Context, tx *storeTx, team Team, d runEventDelivery, now time.Time) error {
	at := now.UTC().UnixNano()
	_, err := tx.execContext(ctx, `INSERT INTO run_event_deliveries(team,subscription_id,event_id,run_id,event_type,body,state,next_at,deadline_at,created_at) VALUES (?,?,?,?,?,?,'pending',?,?,?)`,
		team, d.SubscriptionID, d.EventID, d.RunID, d.EventType, d.Body, at, now.Add(RunEventDeliveryWindow).UTC().UnixNano(), at)
	return err
}

// RunEventSubscriptions lists the team's subscriptions by ID.
func (t *Tenant) RunEventSubscriptions(ctx context.Context) ([]RunEventSubscription, error) {
	return inTxResult(ctx, t.s, func(tx *storeTx) ([]RunEventSubscription, error) {
		return runEventSubscriptionsTx(ctx, tx, t.team)
	})
}

// RunEventSubscription reads one subscription; another team's ID is ErrNotFound.
func (t *Tenant) RunEventSubscription(ctx context.Context, id string) (RunEventSubscription, error) {
	return inTxResult(ctx, t.s, func(tx *storeTx) (RunEventSubscription, error) {
		return runEventSubscriptionTx(ctx, tx, t.team, id)
	})
}

// CreateRunEventSubscription stores a subscription under team administration
// authority and audits it. An existing row with the same ID and the same URL,
// filter and description is a replay: it returns that row and true.
func (t *Tenant) CreateRunEventSubscription(ctx context.Context, a capability.Authorized, sealed SealedRunEventSubscription) (RunEventSubscription, bool, error) {
	row := sealed.row
	if sealed.team != t.team || sealed.team == "" || !validRunEventSubscriptionID(row.ID) || row.URL == "" || !secrets.IsBound(row.Secret) || row.Filter.Validate() != nil {
		return RunEventSubscription{}, false, ErrInvalidInput
	}
	var out RunEventSubscription
	var replayed bool
	err := t.teamMutationTx(ctx, a, "CreateRunEventSubscription", row.ID, func(tx *storeTx, p capability.Principal) error {
		existing, err := runEventSubscriptionTx(ctx, tx, t.team, row.ID)
		if err == nil {
			if existing.URL != row.URL || existing.Description != row.Description || !sameFilter(existing.Filter, row.Filter) {
				return ErrRunEventSubscriptionConflict
			}
			out, replayed = existing, true
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_event_subscriptions WHERE team=?`, t.team).Scan(&count); err != nil {
			return err
		}
		if count >= MaxRunEventSubscriptions {
			return ErrRunEventSubscriptionLimit
		}
		at := tx.now(ctx).UTC()
		row.Team, row.CreatedPrincipal, row.CreatedAt, row.UpdatedAt = t.team, p.Capability.Subject.ID, at, at
		encoded := make([]string, 4)
		for i, values := range [][]string{row.Filter.EventTypes, row.Filter.Statuses, row.Filter.Pipelines, row.Filter.Repositories} {
			raw, err := json.Marshal(nonNil(values))
			if err != nil {
				return err
			}
			encoded[i] = string(raw)
		}
		if _, err := tx.execContext(ctx, `INSERT INTO run_event_subscriptions(team,id,url,event_types,statuses,pipelines,repositories,secret,description,created_principal,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			t.team, row.ID, row.URL, encoded[0], encoded[1], encoded[2], encoded[3], row.Secret, row.Description, row.CreatedPrincipal, at.UnixNano(), at.UnixNano()); err != nil {
			return err
		}
		out = row
		out.Filter = runevents.Filter{EventTypes: nonNil(row.Filter.EventTypes), Statuses: nonNil(row.Filter.Statuses), Pipelines: nonNil(row.Filter.Pipelines), Repositories: nonNil(row.Filter.Repositories)}
		return nil
	})
	if err != nil {
		return RunEventSubscription{}, false, err
	}
	return out, replayed, nil
}

// DeleteRunEventSubscription removes a subscription and its deliveries; it
// reports false when the team holds no such subscription.
func (t *Tenant) DeleteRunEventSubscription(ctx context.Context, a capability.Authorized, id string) (bool, error) {
	var deleted bool
	err := t.teamMutationTx(ctx, a, "DeleteRunEventSubscription", id, func(tx *storeTx, _ capability.Principal) error {
		result, err := tx.execContext(ctx, `DELETE FROM run_event_subscriptions WHERE team=? AND id=?`, t.team, id)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		deleted = n == 1
		return err
	})
	return deleted, err
}

// RotateRunEventSubscriptionSecret replaces a subscription's sealed signing secret.
func (t *Tenant) RotateRunEventSubscriptionSecret(ctx context.Context, a capability.Authorized, sealed SealedRunEventSubscription) (RunEventSubscription, error) {
	if sealed.team != t.team || sealed.team == "" || sealed.row.ID == "" || !secrets.IsBound(sealed.row.Secret) {
		return RunEventSubscription{}, ErrInvalidInput
	}
	var out RunEventSubscription
	err := t.teamMutationTx(ctx, a, "RotateRunEventSubscriptionSecret", sealed.row.ID, func(tx *storeTx, _ capability.Principal) error {
		result, err := tx.execContext(ctx, `UPDATE run_event_subscriptions SET secret=?,updated_at=? WHERE team=? AND id=?`, sealed.row.Secret, tx.now(ctx).UTC().UnixNano(), t.team, sealed.row.ID)
		if err := credentialRowChanged(result, err); err != nil {
			return err
		}
		out, err = runEventSubscriptionTx(ctx, tx, t.team, sealed.row.ID)
		return err
	})
	return out, err
}

func (r *sealedRotationTx) runEventSubscriptions(ctx context.Context, team Team) error {
	subs, err := runEventSubscriptionsTx(ctx, r.tx, team)
	if err != nil {
		return err
	}
	for _, sub := range subs {
		value, err := r.reseal(secrets.PurposeEventSigning, team, RunEventSecretName(sub.ID), "", false, true, sub.Secret)
		if errors.Is(err, errSealedUnreadable) {
			continue
		}
		if err != nil {
			return err
		}
		r.out.RunEventSubscriptions++
		if r.verify {
			continue
		}
		if _, err := r.tx.execContext(ctx, `UPDATE run_event_subscriptions SET secret=? WHERE team=? AND id=?`, value, team, sub.ID); err != nil {
			return err
		}
	}
	return nil
}

func sameFilter(a, b runevents.Filter) bool {
	return slices.Equal(nonNil(a.EventTypes), nonNil(b.EventTypes)) && slices.Equal(nonNil(a.Statuses), nonNil(b.Statuses)) &&
		slices.Equal(nonNil(a.Pipelines), nonNil(b.Pipelines)) && slices.Equal(nonNil(a.Repositories), nonNil(b.Repositories))
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// RunEventSubscriptionID derives a subscription's ID from its team and the
// creating request's idempotency key, so a retried create names the same row.
func RunEventSubscriptionID(team Team, idempotencyKey string) string {
	digest := sha256.Sum256([]byte(string(team) + "\x00" + idempotencyKey))
	return "sub_" + hex.EncodeToString(digest[:12])
}

// safety: subscription IDs appear in URLs and audit rows, so they hold only lowercase hex after the prefix.
func validRunEventSubscriptionID(id string) bool {
	rest, ok := strings.CutPrefix(id, "sub_")
	if !ok || len(rest) != 24 {
		return false
	}
	_, err := hex.DecodeString(rest)
	return err == nil && strings.ToLower(rest) == rest
}
