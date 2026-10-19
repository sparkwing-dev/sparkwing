package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	BillingTrustAutomatic = ""
	BillingTrustGranted   = "granted"
	BillingTrustRevoked   = "revoked"
)

// BillingStanding is the team's current Cloud standing and operator decision.
type BillingStanding struct {
	Tier           string
	CloudVCPU      int64
	MaxJobDuration time.Duration
	Trust          string
	TrustBy        string
	TrustAt        time.Time
	TrustReason    string
	Trusted        bool
	History        StandingHistory
	PrepayCeiling  string
}

// BillingStanding reports the team's history and balance backed standing.
func (t *Tenant) BillingStanding(ctx context.Context, now time.Time) (BillingStanding, error) {
	return trustStandingTx(ctx, storeRowQuerier{t.s}, t.team, now)
}

func trustStandingTx(ctx context.Context, q rowQuerier, team Team, now time.Time) (BillingStanding, error) {
	var b BillingStanding
	var at int64
	err := q.QueryRowContext(ctx, `SELECT billing_trust,billing_trust_by,billing_trust_at,billing_trust_reason FROM teams WHERE name=?`, team).Scan(&b.Trust, &b.TrustBy, &at, &b.TrustReason)
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	if err != nil {
		return b, err
	}
	if at > 0 {
		b.TrustAt = time.Unix(0, at)
	}
	held, err := rowPresentTx(ctx, q, `SELECT 1 FROM credit_freezes WHERE team=? AND released_at IS NULL`, team)
	if err != nil {
		return b, err
	}
	policy, err := standingPolicyTx(ctx, q, team, now, b.Trust, held)
	if err != nil {
		return b, err
	}
	b.Tier, b.CloudVCPU, b.MaxJobDuration = policy.row.tier, policy.row.vCPU, policy.row.duration
	b.History, b.PrepayCeiling = policy.history, policy.prepayCeiling
	b.Trusted = b.Tier == "Trusted" || b.Tier == "High"
	return b, nil
}

// BusinessEventBillingTrustChanged records an operator standing decision.
const BusinessEventBillingTrustChanged = "billing.trust_changed"

// BillingTrustChange is an operator's standing decision.
type BillingTrustChange struct {
	Trust  string
	Actor  string
	Reason string
}

// SetBillingTrust commits a standing decision with its audit event.
func (t *Tenant) SetBillingTrust(ctx context.Context, c BillingTrustChange, now time.Time) (before, after BillingStanding, err error) {
	err = t.s.inTx(ctx, func(tx *storeTx) error { before, after, err = t.setBillingTrustTx(ctx, tx, c, now); return err })
	return before, after, err
}

func (t *Tenant) setBillingTrustTx(ctx context.Context, tx *storeTx, c BillingTrustChange, now time.Time) (before, after BillingStanding, err error) {
	if c.Trust != BillingTrustGranted && c.Trust != BillingTrustRevoked && c.Trust != BillingTrustAutomatic {
		return before, after, fmt.Errorf("%w: trust must be granted, revoked or automatic", ErrInvalidInput)
	}
	if c.Actor == "" || c.Reason == "" {
		return before, after, fmt.Errorf("%w: a standing decision names its actor and reason", ErrInvalidInput)
	}
	if err = lockCreditLedgerTx(ctx, tx); err != nil {
		return before, after, err
	}
	if err = t.lockTeamTx(ctx, tx); err != nil {
		return before, after, err
	}
	if before, err = trustStandingTx(ctx, tx, t.team, now); err != nil {
		return before, after, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE teams SET billing_trust=?,billing_trust_by=?,billing_trust_at=?,billing_trust_reason=? WHERE name=?`, c.Trust, c.Actor, now.UnixNano(), c.Reason, t.team); err != nil {
		return before, after, err
	}
	if after, err = trustStandingTx(ctx, tx, t.team, now); err != nil {
		return before, after, err
	}
	if c.Trust == BillingTrustGranted {
		if _, _, err = decideStandingRequestTx(ctx, tx, t.team, StandingRequestApproved, c.Actor, c.Reason); err != nil {
			return before, after, err
		}
	}
	subject, err := newCreditID("trust")
	if err != nil {
		return before, after, err
	}
	err = RecordBusinessEvent(tx, BusinessEvent{Kind: BusinessEventBillingTrustChanged, Team: t.team, SubjectID: subject, Actor: c.Actor, At: now, Attrs: map[string]any{"reason": c.Reason, "trust_before": before.Trust, "trust_after": after.Trust, "standing_before": before.Tier, "standing_after": after.Tier}})
	return before, after, err
}
