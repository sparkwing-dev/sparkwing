package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Billing trust an operator sets on a team. An empty value leaves the team to
// the automatic rule; granted trusts it whatever the rule says, and revoked
// holds it to the new-team limits whatever the rule says.
const (
	BillingTrustAutomatic = ""
	BillingTrustGranted   = "granted"
	BillingTrustRevoked   = "revoked"
)

// What a team may buy per [PurchaseLimitWindow]. A prepaid team cannot spend
// more than it bought, so this bounds what a stolen card can cost.
const (
	NewPurchaseLimitCents     = 5_000
	TrustedPurchaseLimitCents = 50_000
	// NewPurchaseMaxCents is the largest single purchase of an untrusted
	// team, which keeps the fee Stripe keeps on a disputed purchase small.
	NewPurchaseMaxCents = 5_000
	// MaxPurchaseLimitCents bounds an operator's limit override at the
	// balance cap, since no team can hold more than that anyway.
	MaxPurchaseLimitCents = MaxTeamBalanceMicro / MicroCreditsPerCent

	PurchaseLimitWindow = 30 * 24 * time.Hour

	// A team earns trust once its oldest unreversed purchase is this old,
	// it has spent at least autoTrustMinSpentCents, it has never been
	// held over a dispute, and no card charge of it failed in this window.
	AutoTrustPaymentAge    = 30 * 24 * time.Hour
	autoTrustMinSpentCents = 5_000
)

// BillingStanding is what a team may buy: its stored trust, whether it is
// trusted, and its 30-day purchase limit against what it has bought.
type BillingStanding struct {
	Trust              string
	TrustBy            string
	TrustAt            time.Time
	TrustReason        string
	LimitOverrideCents int64

	Trusted    bool
	LimitMicro int64
	// PurchasedMicro is what the team paid over [PurchaseLimitWindow], less
	// reversals of those payments, plus its checkouts still open or awaiting
	// a late payment for [CheckoutSettleWindow] after their session expired.
	PurchasedMicro int64
}

// PurchaseMaxCents is the largest single purchase the team may make.
func (b BillingStanding) PurchaseMaxCents() int64 {
	if b.Trusted {
		return CreditPurchaseMaxCents
	}
	return NewPurchaseMaxCents
}

// ErrPurchaseLimit is returned when a checkout would take a team past its
// 30-day purchase limit. The refusal is a [PurchaseLimitError].
var ErrPurchaseLimit = errors.New("credits: the 30-day purchase limit refuses this amount")

// PurchaseLimitError names the limit, what the team has bought against it,
// and what it asked for.
type PurchaseLimitError struct {
	Trusted        bool
	LimitMicro     int64
	PurchasedMicro int64
	AmountMicro    int64
}

func (e *PurchaseLimitError) Error() string {
	room := max(e.LimitMicro-e.PurchasedMicro, 0)
	return fmt.Sprintf("this team may buy $%s of credit per 30 days and has bought $%s, so at most $%s more fits, not $%s",
		microDollars(e.LimitMicro), microDollars(e.PurchasedMicro), microDollars(room), microDollars(e.AmountMicro))
}

// Unwrap reports [ErrPurchaseLimit].
func (e *PurchaseLimitError) Unwrap() error { return ErrPurchaseLimit }

var billingTrustCols = map[string]string{
	"billing_trust":                "TEXT NOT NULL DEFAULT ''",
	"billing_trust_by":             "TEXT NOT NULL DEFAULT ''",
	"billing_trust_at":             "INTEGER NOT NULL DEFAULT 0",
	"billing_trust_reason":         "TEXT NOT NULL DEFAULT ''",
	"billing_purchase_limit_cents": "INTEGER NOT NULL DEFAULT 0",
}

var billingTrustColsPostgres = map[string]string{
	"billing_trust":                "TEXT NOT NULL DEFAULT ''",
	"billing_trust_by":             "TEXT NOT NULL DEFAULT ''",
	"billing_trust_at":             "BIGINT NOT NULL DEFAULT 0",
	"billing_trust_reason":         "TEXT NOT NULL DEFAULT ''",
	"billing_purchase_limit_cents": "BIGINT NOT NULL DEFAULT 0",
}

// safety: v80 moves the purchase limit onto the team, so a binary predating
// it would open checkouts past the new-team limit; the requirement makes that
// binary refuse the store instead.
const billingTrustRequirement = "billing-trust-v1"

// BillingStanding reports what t may buy as of now.
func (t *Tenant) BillingStanding(ctx context.Context, now time.Time) (BillingStanding, error) {
	return billingStandingTx(ctx, storeRowQuerier{t.s}, t.team, now)
}

func billingStandingTx(ctx context.Context, q rowQuerier, team Team, now time.Time) (BillingStanding, error) {
	b, err := trustStandingTx(ctx, q, team, now)
	if err != nil {
		return b, err
	}
	paid, err := recentPaidGrantsMicro(ctx, q, team, now)
	if err != nil {
		return b, err
	}
	open, err := settlingCheckoutMicroTx(ctx, q, team, now)
	if err != nil {
		return b, err
	}
	b.PurchasedMicro = paid + open
	return b, nil
}

// perf: every claim and heartbeat asks for the team's trust under the ledger
// lock, so this reads the trust and limit without the purchase sums.
func trustStandingTx(ctx context.Context, q rowQuerier, team Team, now time.Time) (BillingStanding, error) {
	var b BillingStanding
	var atNS int64
	err := q.QueryRowContext(ctx, `SELECT billing_trust, billing_trust_by, billing_trust_at, billing_trust_reason,
	    billing_purchase_limit_cents FROM teams WHERE name = ?`, string(team)).
		Scan(&b.Trust, &b.TrustBy, &atNS, &b.TrustReason, &b.LimitOverrideCents)
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	if err != nil {
		return b, err
	}
	if atNS > 0 {
		b.TrustAt = time.Unix(0, atNS)
	}
	// safety: a hold of any cause returns the team to New, even over an
	// operator's grant, until it is released.
	held, err := rowPresentTx(ctx, q, `SELECT 1 FROM credit_freezes WHERE team = ? AND released_at IS NULL`, string(team))
	if err != nil {
		return b, err
	}
	switch {
	case held, b.Trust == BillingTrustRevoked:
	case b.Trust == BillingTrustGranted:
		b.Trusted = true
	default:
		if b.Trusted, err = autoTrustedTx(ctx, q, team, now); err != nil {
			return b, err
		}
	}
	limitCents := int64(NewPurchaseLimitCents)
	if b.Trusted {
		limitCents = TrustedPurchaseLimitCents
	}
	if b.Trust == BillingTrustGranted && b.LimitOverrideCents > 0 {
		limitCents = b.LimitOverrideCents
	}
	b.LimitMicro = limitCents * MicroCreditsPerCent
	return b, nil
}

// safety: a payment later reversed never counts toward its age, so a refund
// or lost dispute cannot leave behind the history that earns trust.
const autoTrustSQL = `SELECT
  (SELECT MIN(p.created_at) FROM credit_grants p WHERE p.team = ? AND p.kind = ?
     AND NOT EXISTS (SELECT 1 FROM credit_grants r
                      WHERE r.team = p.team AND r.kind = ? AND p.reference != '' AND r.reverses = p.reference)),
  (SELECT SUM(amount_micro) FROM team_spend_days WHERE team = ?),
  (SELECT COUNT(*) FROM credit_freezes WHERE team = ? AND cause != ?)
  + (SELECT COUNT(*) FROM card_attempts WHERE team = ? AND status = ? AND updated_at >= ?)`

func autoTrustedTx(ctx context.Context, q rowQuerier, team Team, now time.Time) (bool, error) {
	var oldest, spent sql.NullInt64
	var freezes int64
	if err := q.QueryRowContext(ctx, autoTrustSQL, string(team), CreditGrantPaid, CreditGrantReversal,
		string(team), string(team), FreezeCauseDecline, string(team), CardAttemptFailed,
		now.Add(-AutoTrustPaymentAge).UnixNano()).
		Scan(&oldest, &spent, &freezes); err != nil {
		return false, err
	}
	return oldest.Valid && oldest.Int64 <= now.Add(-AutoTrustPaymentAge).UnixNano() &&
		spent.Int64 >= autoTrustMinSpentCents*MicroCreditsPerCent &&
		freezes == 0, nil
}

// safety: runs under the ledger lock with the team's open checkouts, so two
// checkouts racing for the last of the limit cannot both pass it.
func refuseAbovePurchaseLimitTx(ctx context.Context, tx *storeTx, team Team, amount int64, now time.Time) error {
	b, err := billingStandingTx(ctx, tx, team, now)
	if err != nil {
		return err
	}
	if b.PurchasedMicro+amount <= b.LimitMicro {
		return nil
	}
	return &PurchaseLimitError{
		Trusted: b.Trusted, LimitMicro: b.LimitMicro, PurchasedMicro: b.PurchasedMicro, AmountMicro: amount,
	}
}

// Business event kinds for billing trust and the purchase limit.
const (
	BusinessEventBillingTrustChanged  = "billing.trust_changed"
	BusinessEventPurchaseLimitRefused = "checkout.purchase_limit_refused"
)

// BillingTrustChange is an operator's decision on a team's trust.
type BillingTrustChange struct {
	// Trust is [BillingTrustGranted], [BillingTrustRevoked], or
	// [BillingTrustAutomatic] to return the team to the automatic rule.
	Trust  string
	Actor  string
	Reason string
	// LimitCents replaces the trusted 30-day purchase limit and spend
	// ceiling of a granted team; zero keeps the default.
	LimitCents int64
	// DailyCapCents and RungCents replace a granted team's daily spend cap
	// and the debt at which its card is charged; zero keeps the default.
	DailyCapCents int64
	RungCents     int64
}

// SetBillingTrust records an operator's trust decision on t and returns the
// standing before and after it. The change and its business event commit
// together.
func (t *Tenant) SetBillingTrust(ctx context.Context, c BillingTrustChange, now time.Time) (before, after BillingStanding, err error) {
	if c.Trust != BillingTrustGranted && c.Trust != BillingTrustRevoked && c.Trust != BillingTrustAutomatic {
		return before, after, fmt.Errorf("%w: trust must be %q, %q or automatic", ErrInvalidInput, BillingTrustGranted, BillingTrustRevoked)
	}
	if c.Reason == "" || c.Actor == "" {
		return before, after, fmt.Errorf("%w: a trust change names its actor and reason", ErrInvalidInput)
	}
	for _, v := range []int64{c.LimitCents, c.DailyCapCents, c.RungCents} {
		if v < 0 || v > MaxPurchaseLimitCents || (v > 0 && c.Trust != BillingTrustGranted) {
			return before, after, fmt.Errorf("%w: a limit is up to %d cents and only for a granted team", ErrInvalidInput, MaxPurchaseLimitCents)
		}
	}
	if c.RungCents > 0 && c.RungCents < MinCardChargeCents {
		return before, after, fmt.Errorf("%w: a rung is at least %d cents, the smallest card charge", ErrInvalidInput, MinCardChargeCents)
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return before, after, err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return before, after, err
	}
	if err := t.lockTeamTx(ctx, tx); err != nil {
		return before, after, err
	}
	if before, err = billingStandingTx(ctx, tx, t.team, now); err != nil {
		return before, after, err
	}
	// safety: a revocation usually answers a chargeback, so only a grant that
	// names no limit restores trust; raising a limit never does it in passing.
	if before.Trust == BillingTrustRevoked && c.LimitCents+c.DailyCapCents+c.RungCents > 0 {
		return before, after, fmt.Errorf("%w: the team's trust is revoked; restore trust before setting a limit", ErrInvalidInput)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE teams SET billing_trust = ?, billing_trust_by = ?, billing_trust_at = ?,
	    billing_trust_reason = ?, billing_purchase_limit_cents = ?, billing_daily_cap_cents = ?,
	    billing_rung_cents = ? WHERE name = ?`,
		c.Trust, c.Actor, now.UnixNano(), c.Reason, c.LimitCents, c.DailyCapCents, c.RungCents,
		string(t.team)); err != nil {
		return before, after, err
	}
	if after, err = billingStandingTx(ctx, tx, t.team, now); err != nil {
		return before, after, err
	}
	subject, err := newCreditID("trust")
	if err != nil {
		return before, after, err
	}
	if err := RecordBusinessEvent(tx, BusinessEvent{
		Kind: BusinessEventBillingTrustChanged, Team: t.team, SubjectID: subject, Actor: c.Actor, At: now,
		Attrs: map[string]any{
			"reason":       c.Reason,
			"trust_before": before.Trust, "trust_after": after.Trust,
			"trusted_before": before.Trusted, "trusted_after": after.Trusted,
			"limit_cents_before": before.LimitMicro / MicroCreditsPerCent,
			"limit_cents_after":  after.LimitMicro / MicroCreditsPerCent,
			"daily_cap_cents":    c.DailyCapCents, "rung_cents": c.RungCents,
		},
	}); err != nil {
		return before, after, err
	}
	return before, after, tx.Commit()
}
