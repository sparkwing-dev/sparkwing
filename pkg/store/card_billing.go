package store

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Spend limits per level, in cents. A team may spend its 30-day ceiling over
// [SpendWindowDays] and its daily cap in one UTC day, whether prepaid or billed
// to its card, and the card it pays with is held to the same limits across
// every team it pays for; a trusted team with a card on file may also run its
// balance down to twice its rung before new work waits for the charge.
const (
	NewSpendCeilingCents     = 5_000
	NewDailyCapCents         = 2_500
	TrustedSpendCeilingCents = 50_000
	TrustedDailyCapCents     = 20_000
	TrustedRungCents         = 10_000
	// MinCardChargeCents is Stripe's smallest US card charge; a smaller debt
	// waits for the next rung or month-end.
	MinCardChargeCents = 50
	// SpendWindowDays is the ceiling's window: today plus 30 whole UTC days
	// before it, so spend leaves the window up to a day late and never early.
	SpendWindowDays = 30
)

// Why a claim was refused, carried on [InsufficientCreditsError].
const (
	SpendLimitBalance      = "balance"
	SpendLimitCeiling      = "ceiling"
	SpendLimitDailyCap     = "daily_cap"
	SpendLimitCardCeiling  = "card_ceiling"
	SpendLimitCardDailyCap = "card_daily_cap"
	SpendLimitBudget       = "budget"
	SpendLimitChargeFailed = "charge_failed"
)

// Business event kinds for card billing.
const (
	BusinessEventCardAdded      = "card.added"
	BusinessEventCardCharged    = "card.charge_paid"
	BusinessEventCardFailed     = "card.charge_failed"
	BusinessEventCardRefundDue  = "card.refund_due"
	BusinessEventCardWarned     = "card.payment_warned"
	BusinessEventBudgetAlert    = "billing.budget_alert"
	BusinessEventCardChargeOpen = "card.charge_opened"
)

// Card charge and attempt states.
const (
	CardChargeOpen    = "open"
	CardChargePaid    = "paid"
	CardAttemptLive   = "live"
	CardAttemptPaid   = "succeeded"
	CardAttemptFailed = "failed"
	// CardAttemptWarned is an attempt whose payment drew an early fraud
	// warning; it is refunded and grants nothing.
	CardAttemptWarned = "warned"

	CardAttemptOffSession = "off_session"
	CardAttemptRecovery   = "recovery"
)

// Why a team is held, on credit_freezes.cause. A hold written before v87
// reads empty, which is a dispute or an operator's hold.
const (
	FreezeCauseDecline = "decline"
	FreezeCauseWarning = "early_fraud_warning"
)

// safety: past the last retry only the owner's "pay now" or a new card
// tries a failed charge again, so a dead card is not hammered.
var cardRetryAfter = []time.Duration{24 * time.Hour, 3 * 24 * time.Hour, 7 * 24 * time.Hour}

// safety: this outlives the 31-minute Checkout session a recovery attempt
// opens, after which the session can no longer be paid.
const recoveryAttemptLife = 35 * time.Minute

// perf: an off-session attempt answers within one call; one still live after
// this is a worker that crashed mid-call, and is asked again.
const stalledAttemptAge = 2 * time.Minute

// safety: Stripe keeps an idempotency key for 24 hours. An attempt that never
// learned its payment intent by then is closed and a new one minted, rather
// than reusing a key Stripe may have forgotten; its intent, if Stripe made
// one, was never confirmed, so it moved no money.
const cardKeyLife = 23 * time.Hour

const dayNS = int64(24 * time.Hour)

var cardBillingTeamCols = map[string]string{
	"card_customer":           "TEXT NOT NULL DEFAULT ''",
	"card_payment_method":     "TEXT NOT NULL DEFAULT ''",
	"card_fingerprint":        "TEXT NOT NULL DEFAULT ''",
	"card_brand":              "TEXT NOT NULL DEFAULT ''",
	"card_last4":              "TEXT NOT NULL DEFAULT ''",
	"card_added_at":           "INTEGER NOT NULL DEFAULT 0",
	"card_owed_since":         "INTEGER NOT NULL DEFAULT 0",
	"billing_rung_cents":      "INTEGER NOT NULL DEFAULT 0",
	"billing_daily_cap_cents": "INTEGER NOT NULL DEFAULT 0",
	"billing_budget_cents":    "INTEGER NOT NULL DEFAULT 0",
	"billing_runner_cap":      "INTEGER NOT NULL DEFAULT 0",
}

var freezeCauseCols = map[string]string{"cause": "TEXT NOT NULL DEFAULT ''"}

// safety: a charge row names the card its team paid with when it was written,
// so a later refund debits that card's bucket even after the card is replaced.
var chargeCardCols = map[string]string{"card_fingerprint": "TEXT NOT NULL DEFAULT ''"}

// safety: one open charge per team and one live attempt per charge are
// unique indexes, so a second worker's insert fails rather than charging the
// card twice; the attempt id is the Stripe idempotency key. A warning and a
// card's spend outlive the teams they touched, so those tables carry no team.
const cardBillingTablesSQL = `CREATE TABLE IF NOT EXISTS card_charges (
    id              TEXT PRIMARY KEY,
    team            TEXT NOT NULL,
    amount_micro    INTEGER NOT NULL,
    cause           TEXT NOT NULL,
    state           TEXT NOT NULL,
    failures        INTEGER NOT NULL DEFAULT 0,
    decline_code    TEXT NOT NULL DEFAULT '',
    next_attempt_at INTEGER NOT NULL,
    payment_intent  TEXT NOT NULL DEFAULT '',
    opened_at       INTEGER NOT NULL,
    closed_at       INTEGER
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_card_charges_one_open ON card_charges(team) WHERE state = 'open';
CREATE INDEX IF NOT EXISTS idx_card_charges_due ON card_charges(state, next_attempt_at);
CREATE TABLE IF NOT EXISTS card_attempts (
    id             TEXT PRIMARY KEY,
    team           TEXT NOT NULL,
    charge_id      TEXT NOT NULL,
    kind           TEXT NOT NULL,
    status         TEXT NOT NULL,
    payment_intent TEXT NOT NULL DEFAULT '',
    decline_code   TEXT NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_card_attempts_one_live ON card_attempts(charge_id) WHERE status = 'live';
CREATE INDEX IF NOT EXISTS idx_card_attempts_team ON card_attempts(team, status, updated_at);
CREATE INDEX IF NOT EXISTS idx_card_attempts_intent ON card_attempts(payment_intent);
CREATE TABLE IF NOT EXISTS card_refunds (
    team            TEXT NOT NULL,
    payment_intent  TEXT NOT NULL,
    reason          TEXT NOT NULL,
    refund_id       TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'due',
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    PRIMARY KEY (team, payment_intent)
);
CREATE TABLE IF NOT EXISTS team_spend_days (
    team         TEXT NOT NULL,
    day          INTEGER NOT NULL,
    amount_micro INTEGER NOT NULL,
    PRIMARY KEY (team, day)
);
CREATE TABLE IF NOT EXISTS card_spend_days (
    fingerprint  TEXT NOT NULL,
    day          INTEGER NOT NULL,
    amount_micro INTEGER NOT NULL,
    PRIMARY KEY (fingerprint, day)
);
CREATE TABLE IF NOT EXISTS payment_warnings (
    warning_id     TEXT PRIMARY KEY,
    payment_intent TEXT NOT NULL,
    fingerprint    TEXT NOT NULL DEFAULT '',
    actionable     INTEGER NOT NULL DEFAULT 1,
    created_at     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_payment_warnings_intent ON payment_warnings(payment_intent);
CREATE INDEX IF NOT EXISTS idx_payment_warnings_card ON payment_warnings(fingerprint);`

// safety: a refund lands on the day of its reservation (the node's latest at or before it, not its first), as
// new writes do, so an old refund cannot free room on the day it was written.
// perf: one pass over the ledger; a refund's reservation is found through idx_credit_charges_node, and writes
// are frozen for the upgrade.
const spendBackfillSQL = `INSERT INTO team_spend_days (team, day, amount_micro)
SELECT c.team, COALESCE(CASE WHEN c.kind = 'refund' THEN (
         SELECT MAX(r.charged_at) FROM credit_charges r
          WHERE r.team = c.team AND r.run_id = c.run_id AND r.node_id = c.node_id AND r.kind = 'reservation'
            AND r.charged_at <= c.charged_at)
       END, c.charged_at) / 86400000000000, SUM(c.amount_micro)
  FROM credit_charges c
 GROUP BY 1, 2
ON CONFLICT (team, day) DO NOTHING`

// safety: v87 lets a trusted team's balance go negative and counts every
// charge into the spend buckets; a binary predating it would neither honor
// the card billing nor count spend, so it refuses the store.
const cardBillingRequirement = "card-billing-v1"

func applyCardBillingMigration(ctx context.Context, tx *storeTx, postgres bool) error {
	teamCols, freezeCols, script := cardBillingTeamCols, freezeCauseCols, cardBillingTablesSQL
	if postgres {
		teamCols = bigintCols(cardBillingTeamCols)
		script = strings.ReplaceAll(script, "INTEGER", "BIGINT")
		if err := addColumnsTx(ctx, tx, "teams", teamCols); err != nil {
			return err
		}
		if err := addColumnsTx(ctx, tx, "credit_freezes", freezeCols); err != nil {
			return err
		}
		if err := addColumnsTx(ctx, tx, "credit_charges", chargeCardCols); err != nil {
			return err
		}
	} else {
		if err := ensureColumnsSQLite(ctx, tx, "teams", teamCols); err != nil {
			return err
		}
		if err := ensureColumnsSQLite(ctx, tx, "credit_freezes", freezeCols); err != nil {
			return err
		}
		if err := ensureColumnsSQLite(ctx, tx, "credit_charges", chargeCardCols); err != nil {
			return err
		}
	}
	if err := execStatements(ctx, tx, script); err != nil {
		return err
	}
	// safety: the per-principal runner cap the team-wide count replaces is
	// gone, so its settings are dropped rather than left to mislead.
	if _, err := tx.ExecContext(ctx, `DELETE FROM sparkwing_meta WHERE key LIKE ?`,
		computeLimitKey("runner_scale_")+"%"); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, spendBackfillSQL)
	return err
}

func bigintCols(cols map[string]string) map[string]string {
	out := make(map[string]string, len(cols))
	for k, v := range cols {
		out[k] = strings.ReplaceAll(v, "INTEGER", "BIGINT")
	}
	return out
}

// safety: a refund lands on the day and the card of the charge it refunds, so
// refunding yesterday's reservation cannot free room under today's cap, nor
// credit a card that paid nothing. The card is counted beside the team, so
// one card cannot spend the limits again through a second team.
func addSpendTx(ctx context.Context, tx *storeTx, team Team, fingerprint string, atNS, amount int64) error {
	if amount == 0 {
		return nil
	}
	day := atNS / dayNS
	if _, err := tx.ExecContext(ctx, `INSERT INTO team_spend_days (team, day, amount_micro) VALUES (?, ?, ?)
		ON CONFLICT (team, day) DO UPDATE SET amount_micro = team_spend_days.amount_micro + excluded.amount_micro`,
		string(team), day, amount); err != nil {
		return err
	}
	if fingerprint == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO card_spend_days (fingerprint, day, amount_micro) VALUES (?, ?, ?)
		ON CONFLICT (fingerprint, day) DO UPDATE SET amount_micro = card_spend_days.amount_micro + excluded.amount_micro`,
		fingerprint, day, amount)
	return err
}

func teamCardTx(ctx context.Context, tx *storeTx, team Team) (string, error) {
	var fingerprint string
	err := tx.QueryRowContext(ctx, `SELECT card_fingerprint FROM teams WHERE name = ?`, string(team)).Scan(&fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return fingerprint, err
}

// Card is the card a team's usage is billed to.
type Card struct {
	Customer      string
	PaymentMethod string
	Fingerprint   string
	Brand         string
	Last4         string
	AddedAt       time.Time
}

// CardCharge is one obligation billed to a team's card.
type CardCharge struct {
	ID            string
	Team          Team
	AmountMicro   int64
	Cause         string
	State         string
	Failures      int64
	DeclineCode   string
	NextAttemptAt time.Time
	PaymentIntent string
	OpenedAt      time.Time
}

// SpendStanding is what a team may still spend and why: its balance, the
// credit its card extends, and its spend, and its card's, against each limit.
type SpendStanding struct {
	Billing             BillingStanding
	Card                Card
	OpenCharge          *CardCharge
	BalanceMicro        int64
	CreditLimitMicro    int64
	RungMicro           int64
	CeilingMicro        int64
	DailyCapMicro       int64
	BudgetMicro         int64
	Spent30dMicro       int64
	SpentTodayMicro     int64
	CardSpent30dMicro   int64
	CardSpentTodayMicro int64
}

// CardBilled reports that the team's usage is charged to its card: it is
// trusted and has one on file.
func (s SpendStanding) CardBilled() bool {
	return s.Billing.Trusted && s.Card.PaymentMethod != ""
}

// ChargeFailed reports an open charge whose last attempt failed.
func (s SpendStanding) ChargeFailed() bool {
	return s.OpenCharge != nil && s.OpenCharge.Failures > 0
}

// Headroom is what the team may still spend and the limit that binds first.
func (s SpendStanding) Headroom() (int64, string) {
	room, limit := s.BalanceMicro+s.CreditLimitMicro, SpendLimitBalance
	if s.ChargeFailed() && room <= 0 {
		limit = SpendLimitChargeFailed
	}
	checks := []struct {
		room  int64
		limit string
	}{
		{s.CeilingMicro - s.Spent30dMicro, SpendLimitCeiling},
		{s.DailyCapMicro - s.SpentTodayMicro, SpendLimitDailyCap},
		{s.BudgetMicro - s.Spent30dMicro, SpendLimitBudget},
	}
	if s.Card.Fingerprint != "" {
		checks = append(checks, []struct {
			room  int64
			limit string
		}{
			{s.CeilingMicro - s.CardSpent30dMicro, SpendLimitCardCeiling},
			{s.DailyCapMicro - s.CardSpentTodayMicro, SpendLimitCardDailyCap},
		}...)
	}
	for _, c := range checks {
		if c.room < room {
			room, limit = c.room, c.limit
		}
	}
	return room, limit
}

// SpendStanding reports what t may still spend as of now.
func (t *Tenant) SpendStanding(ctx context.Context, now time.Time) (SpendStanding, error) {
	return spendStandingTx(ctx, storeRowQuerier{t.s}, t.team, now)
}

const spendWindowSQL = `SELECT COALESCE(SUM(amount_micro), 0),
    COALESCE(SUM(CASE WHEN day = ? THEN amount_micro ELSE 0 END), 0)
  FROM %s WHERE %s = ? AND day >= ?`

func spendStandingTx(ctx context.Context, q rowQuerier, team Team, now time.Time) (SpendStanding, error) {
	var s SpendStanding
	var err error
	if s.Billing, err = trustStandingTx(ctx, q, team, now); err != nil {
		return s, err
	}
	var addedNS, rungCents, dailyCents, budgetCents int64
	if err := q.QueryRowContext(ctx, `SELECT card_customer, card_payment_method, card_fingerprint, card_brand,
	    card_last4, card_added_at, billing_rung_cents, billing_daily_cap_cents, billing_budget_cents
	    FROM teams WHERE name = ?`, string(team)).Scan(&s.Card.Customer, &s.Card.PaymentMethod,
		&s.Card.Fingerprint, &s.Card.Brand, &s.Card.Last4, &addedNS, &rungCents, &dailyCents, &budgetCents); err != nil {
		return s, err
	}
	if addedNS > 0 {
		s.Card.AddedAt = time.Unix(0, addedNS)
	}
	if s.OpenCharge, err = openCardChargeTx(ctx, q, team); err != nil {
		return s, err
	}
	today := now.UnixNano() / dayNS
	if err := q.QueryRowContext(ctx, fmt.Sprintf(spendWindowSQL, "team_spend_days", "team"),
		today, string(team), today-SpendWindowDays).Scan(&s.Spent30dMicro, &s.SpentTodayMicro); err != nil {
		return s, err
	}
	if s.Card.Fingerprint != "" {
		if err := q.QueryRowContext(ctx, fmt.Sprintf(spendWindowSQL, "card_spend_days", "fingerprint"),
			today, s.Card.Fingerprint, today-SpendWindowDays).Scan(&s.CardSpent30dMicro, &s.CardSpentTodayMicro); err != nil {
			return s, err
		}
	}
	if s.BalanceMicro, err = balanceMicroTx(ctx, q, team); err != nil {
		return s, err
	}
	ceiling, daily, rung := int64(NewSpendCeilingCents), int64(NewDailyCapCents), int64(0)
	if s.Billing.Trusted {
		ceiling, daily, rung = TrustedSpendCeilingCents, TrustedDailyCapCents, TrustedRungCents
	}
	if s.Billing.Trust == BillingTrustGranted && s.Billing.Trusted {
		if s.Billing.LimitOverrideCents > 0 {
			ceiling = s.Billing.LimitOverrideCents
		}
		if dailyCents > 0 {
			daily = dailyCents
		}
		if rungCents > 0 {
			rung = rungCents
		}
	}
	budget := ceiling
	if budgetCents > 0 && budgetCents < ceiling {
		budget = budgetCents
	}
	s.CeilingMicro = ceiling * MicroCreditsPerCent
	s.DailyCapMicro = daily * MicroCreditsPerCent
	s.BudgetMicro = budget * MicroCreditsPerCent
	s.RungMicro = rung * MicroCreditsPerCent
	// safety: a failed charge withdraws the card's credit until it is paid,
	// so the team runs on what it has paid for while the owner fixes the card.
	if s.CardBilled() && !s.ChargeFailed() {
		s.CreditLimitMicro = 2 * s.RungMicro
	}
	return s, nil
}

func balanceMicroTx(ctx context.Context, q rowQuerier, team Team) (int64, error) {
	var granted, charged sql.NullInt64
	if err := q.QueryRowContext(ctx, creditBalanceSQL, string(team), string(team)).Scan(&granted, &charged); err != nil {
		return 0, err
	}
	return granted.Int64 - charged.Int64, nil
}

func openCardChargeTx(ctx context.Context, q rowQuerier, team Team) (*CardCharge, error) {
	c := CardCharge{Team: team}
	var next, opened int64
	err := q.QueryRowContext(ctx, `SELECT id, amount_micro, cause, state, failures, decline_code, next_attempt_at,
	    payment_intent, opened_at FROM card_charges WHERE team = ? AND state = ?`, string(team), CardChargeOpen).
		Scan(&c.ID, &c.AmountMicro, &c.Cause, &c.State, &c.Failures, &c.DeclineCode, &next, &c.PaymentIntent, &opened)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.NextAttemptAt, c.OpenedAt = time.Unix(0, next), time.Unix(0, opened)
	return &c, nil
}

// safety: every claim, and every charge of running work, asks this under the
// ledger lock, so two claims racing for the last room cannot both pass it.
func refuseAboveHeadroomTx(
	ctx context.Context, tx *storeTx, team Team, required int64, now time.Time, runID, nodeID string,
) error {
	s, err := spendStandingTx(ctx, tx, team, now)
	if err != nil {
		return err
	}
	room, limit := s.Headroom()
	if room >= required {
		return nil
	}
	return &InsufficientCreditsError{
		BalanceMicro: s.BalanceMicro, RequiredMicro: required, RunID: runID, NodeID: nodeID, Limit: limit,
	}
}

func headroomTx(ctx context.Context, tx *storeTx, team Team, now time.Time) (int64, error) {
	s, err := spendStandingTx(ctx, tx, team, now)
	if err != nil {
		return 0, err
	}
	room, _ := s.Headroom()
	return room, nil
}

// SaveCard records the card an owner added through Stripe. A charge that
// failed on the old card is tried again at once.
func (t *Tenant) SaveCard(ctx context.Context, c Card, actor string, now time.Time) (err error) {
	if c.Customer == "" || c.PaymentMethod == "" || c.Fingerprint == "" {
		return fmt.Errorf("%w: a card names its customer, payment method and fingerprint", ErrInvalidInput)
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return err
	}
	if err := t.lockTeamTx(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE teams SET card_customer = ?, card_payment_method = ?,
	    card_fingerprint = ?, card_brand = ?, card_last4 = ?, card_added_at = ? WHERE name = ?`,
		c.Customer, c.PaymentMethod, c.Fingerprint, c.Brand, c.Last4, now.UnixNano(), string(t.team)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE card_charges SET next_attempt_at = ? WHERE team = ? AND state = ?`,
		now.UnixNano(), string(t.team), CardChargeOpen); err != nil {
		return err
	}
	if err := RecordBusinessEvent(tx, BusinessEvent{
		Team: t.team, Kind: BusinessEventCardAdded, SubjectID: c.PaymentMethod, Actor: actor, At: now,
		Attrs: map[string]any{"brand": c.Brand, "last4": c.Last4},
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// SetSpendBudget sets the owner's budget, which stops Cloud usage when the
// team's 30-day spend reaches it. Zero returns it to the ceiling.
func (t *Tenant) SetSpendBudget(ctx context.Context, cents int64) error {
	if cents < 0 || cents > MaxPurchaseLimitCents {
		return fmt.Errorf("%w: a budget is between $0 and $%d", ErrInvalidInput, MaxPurchaseLimitCents/100)
	}
	_, err := t.s.exec(ctx, `UPDATE teams SET billing_budget_cents = ? WHERE name = ?`, cents, string(t.team))
	return err
}

// CardChargeWork is one Stripe call the payment worker owes for an open
// charge, under the attempt's id. PaymentIntent is empty until the worker has
// created the payment and recorded it with [Store.RecordAttemptIntent]; from
// then on the payment is confirmed and read by that id.
type CardChargeWork struct {
	Team          Team
	ChargeID      string
	AttemptID     string
	AmountCents   int64
	Customer      string
	PaymentMethod string
	PaymentIntent string
}

// BudgetAlert is a budget threshold a team crossed this month, reported once.
type BudgetAlert struct {
	Team          Team
	Percent       int64
	Spent30dMicro int64
	BudgetMicro   int64
}

// DueCardCharges opens a charge for each card-billed team whose debt reached
// its trigger, and starts an attempt for every open charge that is due. The
// caller makes each Stripe call outside the ledger lock and reports it with
// [Store.SettleCardPayment] or [Store.FailCardAttempt].
func (s *Store) DueCardCharges(ctx context.Context, now time.Time) (_ []CardChargeWork, _ []BudgetAlert, err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return nil, nil, err
	}
	nowNS := now.UnixNano()
	if _, err := tx.ExecContext(ctx, `UPDATE card_attempts SET status = ?, decline_code = 'expired', updated_at = ?
	    WHERE status = ? AND kind = ? AND created_at < ?`, CardAttemptFailed, nowNS, CardAttemptLive,
		CardAttemptRecovery, now.Add(-recoveryAttemptLife).UnixNano()); err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE card_attempts SET status = ?, decline_code = 'key_expired', updated_at = ?
	    WHERE status = ? AND kind = ? AND payment_intent = '' AND created_at < ?`, CardAttemptFailed, nowNS,
		CardAttemptLive, CardAttemptOffSession, now.Add(-cardKeyLife).UnixNano()); err != nil {
		return nil, nil, err
	}
	teams, err := billedTeamsTx(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	var alerts []BudgetAlert
	for _, team := range teams {
		if team.carded {
			if err := decideCardChargeTx(ctx, tx, team.name, now); err != nil {
				return nil, nil, err
			}
		}
		alert, err := budgetAlertTx(ctx, tx, team.name, now)
		if err != nil {
			return nil, nil, err
		}
		if alert != nil {
			alerts = append(alerts, *alert)
		}
	}
	work, err := startDueAttemptsTx(ctx, tx, now)
	if err != nil {
		return nil, nil, err
	}
	return work, alerts, tx.Commit()
}

type billedTeam struct {
	name   Team
	carded bool
}

// safety: only a team with a card gets a charge opened, since a charge with
// no card to try would stay open and block the team's deletion; a team with
// only a budget is visited for its alerts.
func billedTeamsTx(ctx context.Context, tx *storeTx) (_ []billedTeam, err error) {
	rows, err := tx.QueryContext(ctx, `SELECT name, card_payment_method != '' FROM teams
	    WHERE card_payment_method != '' OR billing_budget_cents > 0 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []billedTeam
	for rows.Next() {
		var t billedTeam
		if err := rows.Scan(&t.name, &t.carded); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// safety: a dispute, fraud warning or operator hold stops the card being
// charged; a decline's own hold does not, since the retries are what lift it.
const blockingHoldSQL = `SELECT 1 FROM credit_freezes f WHERE f.team = ? AND f.released_at IS NULL AND f.cause != ?`

// safety: a team with a card owes whatever its balance reads below zero; the
// charge opens at the rung, on the first of the month for a debt carried
// over it, and at once for a team that is no longer card-billed, so a
// revocation or a hold settles what is owed.
func decideCardChargeTx(ctx context.Context, tx *storeTx, team Team, now time.Time) error {
	st, err := spendStandingTx(ctx, tx, team, now)
	if err != nil {
		return err
	}
	nowNS := now.UnixNano()
	var owedSince int64
	if err := tx.QueryRowContext(ctx, `SELECT card_owed_since FROM teams WHERE name = ?`, string(team)).
		Scan(&owedSince); err != nil {
		return err
	}
	switch {
	case st.BalanceMicro >= 0 && owedSince != 0:
		owedSince = 0
	case st.BalanceMicro < 0 && owedSince == 0:
		owedSince = nowNS
	}
	if _, err := tx.ExecContext(ctx, `UPDATE teams SET card_owed_since = ? WHERE name = ?`,
		owedSince, string(team)); err != nil {
		return err
	}
	owedCents := -st.BalanceMicro / MicroCreditsPerCent
	if st.OpenCharge != nil || owedCents < MinCardChargeCents {
		return nil
	}
	monthStart := time.Date(now.UTC().Year(), now.UTC().Month(), 1, 0, 0, 0, 0, time.UTC).UnixNano()
	cause := ""
	switch {
	case !st.CardBilled():
		cause = "settle"
	case -st.BalanceMicro >= st.RungMicro:
		cause = "rung"
	case owedSince < monthStart:
		cause = "month_end"
	default:
		return nil
	}
	if held, err := rowPresentTx(ctx, tx, blockingHoldSQL, string(team), FreezeCauseDecline); err != nil || held {
		return err
	}
	id, err := newCreditID("cardcharge")
	if err != nil {
		return err
	}
	amount := owedCents * MicroCreditsPerCent
	if _, err := tx.ExecContext(ctx, `INSERT INTO card_charges (id, team, amount_micro, cause, state,
	    next_attempt_at, opened_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, string(team), amount, cause, CardChargeOpen, nowNS, nowNS); err != nil {
		return fmt.Errorf("card billing: open charge: %w", err)
	}
	return RecordBusinessEvent(tx, BusinessEvent{
		Team: team, Kind: BusinessEventCardChargeOpen, SubjectID: id, Actor: "system", At: now,
		Attrs: map[string]any{"amount_micro": amount, "cause": cause},
	})
}

func dueChargesTx(ctx context.Context, tx *storeTx, now time.Time) (_ []CardChargeWork, err error) {
	nowNS := now.UnixNano()
	rows, err := tx.QueryContext(ctx, `SELECT c.id, c.team, c.amount_micro, t.card_customer, t.card_payment_method,
	    COALESCE(a.id, ''), COALESCE(a.payment_intent, '')
	  FROM card_charges c JOIN teams t ON t.name = c.team
	  LEFT JOIN card_attempts a ON a.charge_id = c.id AND a.status = ?
	  WHERE c.state = ? AND t.card_payment_method != ''
	    AND ((a.id IS NULL AND c.next_attempt_at <= ?)
	      OR (a.kind = ? AND a.updated_at < ?))
	    AND NOT EXISTS (SELECT 1 FROM credit_freezes f
	                     WHERE f.team = c.team AND f.released_at IS NULL AND f.cause != ?)
	  ORDER BY c.opened_at`,
		CardAttemptLive, CardChargeOpen, nowNS, CardAttemptOffSession, now.Add(-stalledAttemptAge).UnixNano(),
		FreezeCauseDecline)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var work []CardChargeWork
	for rows.Next() {
		var w CardChargeWork
		var amount int64
		if err := rows.Scan(&w.ChargeID, &w.Team, &amount, &w.Customer, &w.PaymentMethod, &w.AttemptID,
			&w.PaymentIntent); err != nil {
			return nil, err
		}
		w.AmountCents = amount / MicroCreditsPerCent
		work = append(work, w)
	}
	return work, rows.Err()
}

func startDueAttemptsTx(ctx context.Context, tx *storeTx, now time.Time) (_ []CardChargeWork, err error) {
	nowNS := now.UnixNano()
	work, err := dueChargesTx(ctx, tx, now)
	if err != nil {
		return nil, err
	}
	for i := range work {
		if work[i].AttemptID != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE card_attempts SET updated_at = ? WHERE id = ?`,
				nowNS, work[i].AttemptID); err != nil {
				return nil, err
			}
			continue
		}
		if work[i].AttemptID, err = insertCardAttemptTx(ctx, tx, work[i].Team, work[i].ChargeID,
			CardAttemptOffSession, nowNS); err != nil {
			return nil, err
		}
	}
	return work, nil
}

func insertCardAttemptTx(ctx context.Context, tx *storeTx, team Team, chargeID, kind string, nowNS int64) (string, error) {
	id, err := newCreditID("attempt")
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO card_attempts (id, team, charge_id, kind, status, created_at,
	    updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, string(team), chargeID, kind, CardAttemptLive, nowNS, nowNS); err != nil {
		return "", fmt.Errorf("card billing: start attempt: %w", err)
	}
	return id, nil
}

// RecordAttemptIntent binds the payment intent Stripe made for a live
// attempt, so every later pass confirms and reads that payment by its id.
// Binding the same intent twice is a no-op; another intent is refused.
func (s *Store) RecordAttemptIntent(ctx context.Context, attemptID, paymentIntent string, now time.Time) error {
	if attemptID == "" || paymentIntent == "" {
		return fmt.Errorf("%w: an attempt's intent names both", ErrInvalidInput)
	}
	res, err := s.exec(ctx, `UPDATE card_attempts SET payment_intent = ?, updated_at = ?
	    WHERE id = ? AND status = ? AND (payment_intent = '' OR payment_intent = ?)`,
		paymentIntent, now.UnixNano(), attemptID, CardAttemptLive, paymentIntent)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return cmp.Or(err, fmt.Errorf("%w: attempt %q is not live or has another payment", ErrInvalidInput, attemptID))
	}
	return nil
}

// ErrNoPayableCharge is returned when an owner asks to pay a charge that is
// not open, or one with an attempt already in flight.
var ErrNoPayableCharge = errors.New("card billing: no charge is waiting for payment")

// StartRecoveryAttempt opens a "pay now" attempt on t's open charge, for a
// Checkout the owner pays by hand. It refuses while another attempt is live.
func (t *Tenant) StartRecoveryAttempt(ctx context.Context, now time.Time) (_ CardChargeWork, err error) {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return CardChargeWork{}, err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return CardChargeWork{}, err
	}
	charge, err := openCardChargeTx(ctx, tx, t.team)
	if err != nil {
		return CardChargeWork{}, err
	}
	if charge == nil {
		return CardChargeWork{}, ErrNoPayableCharge
	}
	w := CardChargeWork{Team: t.team, ChargeID: charge.ID, AmountCents: charge.AmountMicro / MicroCreditsPerCent}
	if err := tx.QueryRowContext(ctx, `SELECT card_customer FROM teams WHERE name = ?`, string(t.team)).
		Scan(&w.Customer); err != nil {
		return CardChargeWork{}, err
	}
	live, err := rowPresentTx(ctx, tx, `SELECT 1 FROM card_attempts WHERE team = ? AND charge_id = ? AND status = ?`,
		string(t.team), charge.ID, CardAttemptLive)
	if err != nil {
		return CardChargeWork{}, err
	}
	if live {
		return CardChargeWork{}, ErrNoPayableCharge
	}
	if w.AttemptID, err = insertCardAttemptTx(ctx, tx, t.team, charge.ID, CardAttemptRecovery, now.UnixNano()); err != nil {
		return CardChargeWork{}, err
	}
	return w, tx.Commit()
}

// CardPayment is a succeeded Stripe payment of a card charge.
type CardPayment struct {
	Team          Team
	ChargeID      string
	AttemptID     string
	PaymentIntent string
	AmountCents   int64
	// Fingerprint is the card that paid, which a pay-now Checkout may take
	// from another card than the one on file.
	Fingerprint string
}

type cardAttemptRow struct {
	team, chargeID, kind, status, paymentIntent string
}

// SettleCardPayment is the one path a card payment reaches the ledger by: the
// worker's synchronous answer, the webhook and a recovery Checkout all land
// here, and a repeat of a settled payment is a no-op. The live attempt of an
// open charge paying exactly the charge's frozen amount pays it off and lifts
// a decline's hold. Any other money that arrived, a second payment or the
// wrong amount, is granted and queued for a refund, whose reversal takes the
// grant back. A payment that drew an early fraud warning, or was made on a
// card that did, grants nothing and holds the team, except that a pay-now
// payment by a card warned only on another payment still repays exactly the
// debt it was opened for, and holds the team. warning names the actionable
// early fraud warning that matched the payment or its card, for the
// operator's alert.
func (s *Store) SettleCardPayment(ctx context.Context, p CardPayment, now time.Time) (created bool, warning string, err error) {
	if p.PaymentIntent == "" || p.AmountCents <= 0 || p.ChargeID == "" || p.AttemptID == "" {
		return false, "", fmt.Errorf("%w: a card payment names its charge, attempt, payment intent and amount", ErrInvalidInput)
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return false, "", err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return false, "", err
	}
	var a cardAttemptRow
	err = tx.QueryRowContext(ctx, `SELECT team, charge_id, kind, status, payment_intent FROM card_attempts WHERE id = ?`,
		p.AttemptID).Scan(&a.team, &a.chargeID, &a.kind, &a.status, &a.paymentIntent)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", fmt.Errorf("%w: card attempt %q", ErrNotFound, p.AttemptID)
	}
	if err != nil {
		return false, "", err
	}
	if a.chargeID != p.ChargeID || (p.Team != "" && Team(a.team) != p.Team) {
		return false, "", fmt.Errorf("%w: card attempt %q is not for charge %q of this team", ErrInvalidInput, p.AttemptID, p.ChargeID)
	}
	team := Team(a.team)
	if done, err := cardPaymentSeenTx(ctx, tx, team, p.PaymentIntent); err != nil || done {
		return false, "", err
	}
	var state string
	var frozenAmount int64
	if err := tx.QueryRowContext(ctx, `SELECT state, amount_micro FROM card_charges WHERE id = ?`, p.ChargeID).
		Scan(&state, &frozenAmount); err != nil {
		return false, "", err
	}
	nowNS := now.UnixNano()
	amount := p.AmountCents * MicroCreditsPerCent
	payable := state == CardChargeOpen && a.status == CardAttemptLive && amount == frozenAmount &&
		(a.paymentIntent == "" || a.paymentIntent == p.PaymentIntent)
	warning, err = paymentWarningTx(ctx, tx, p.PaymentIntent, p.Fingerprint)
	if err != nil {
		return false, "", err
	}
	if warning != "" {
		repays, err := warnedCardRepaysTx(ctx, tx, a.kind, p.PaymentIntent, payable)
		if err != nil {
			return false, "", err
		}
		if err := settleWarnedTx(ctx, tx, team, p, warning, repays, now); err != nil {
			return false, "", err
		}
		if !repays {
			return false, warning, tx.Commit()
		}
	}
	if err := insertCardGrantTx(ctx, tx, team, amount, p.PaymentIntent, nowNS); err != nil {
		return false, "", err
	}
	if !payable {
		reason := "duplicate"
		if amount != frozenAmount {
			reason = "amount_mismatch"
		}
		if err := queueCardRefundTx(ctx, tx, team, p, reason, now); err != nil {
			return false, "", err
		}
		return true, "", tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE card_attempts SET status = ?, payment_intent = ?, updated_at = ?
	    WHERE id = ?`, CardAttemptPaid, p.PaymentIntent, nowNS, p.AttemptID); err != nil {
		return false, "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE card_charges SET state = ?, payment_intent = ?, closed_at = ?
	    WHERE id = ?`, CardChargePaid, p.PaymentIntent, nowNS, p.ChargeID); err != nil {
		return false, "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE credit_freezes SET released_at = ?
	    WHERE team = ? AND cause = ? AND released_at IS NULL`, nowNS, string(team), FreezeCauseDecline); err != nil {
		return false, "", err
	}
	if err := RecordBusinessEvent(tx, BusinessEvent{
		Team: team, Kind: BusinessEventCardCharged, SubjectID: p.PaymentIntent, Actor: "card", At: now,
		Attrs: map[string]any{"charge_id": p.ChargeID, "attempt_id": p.AttemptID, "amount_micro": amount},
	}); err != nil {
		return false, "", err
	}
	balance, err := creditBalanceTx(ctx, tx, team)
	if err != nil {
		return false, "", err
	}
	if balance > 0 {
		if err := clearTeamCreditExhaustedTx(ctx, tx, team, nowNS); err != nil {
			return false, "", err
		}
	}
	// safety: what is still owed opens the next charge in this transaction,
	// so a payment that settles less than the debt never leaves it unbilled.
	if err := decideCardChargeTx(ctx, tx, team, now); err != nil {
		return false, "", err
	}
	return true, warning, tx.Commit()
}

// safety: a payment already granted, queued for refund or refused over a
// warning has been settled; its redelivery changes nothing.
func cardPaymentSeenTx(ctx context.Context, tx *storeTx, team Team, paymentIntent string) (bool, error) {
	grant, found, err := creditGrantByReferenceTx(ctx, tx, "", CreditGrantPaid, paymentIntent)
	if err == nil && found && grant.Team != team {
		return false, ErrCreditGrantConflict
	}
	if err != nil || found {
		return found, err
	}
	return rowPresentTx(ctx, tx, `SELECT 1 FROM card_attempts WHERE team = ? AND payment_intent = ? AND status = ?`,
		string(team), paymentIntent, CardAttemptWarned)
}

func insertCardGrantTx(ctx context.Context, tx *storeTx, team Team, amount int64, paymentIntent string, nowNS int64) error {
	id, err := newCreditID("grant")
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO credit_grants (team, id, kind, amount_micro, reference, reverses,
	    created_by, created_at) VALUES (?, ?, ?, ?, ?, '', 'card', ?)`,
		string(team), id, CreditGrantPaid, amount, paymentIntent, nowNS); err != nil {
		return fmt.Errorf("card billing: grant payment: %w", err)
	}
	return nil
}

func queueCardRefundTx(ctx context.Context, tx *storeTx, team Team, p CardPayment, reason string, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO card_refunds (payment_intent, team, reason, created_at)
	    VALUES (?, ?, ?, ?) ON CONFLICT (team, payment_intent) DO NOTHING`,
		p.PaymentIntent, string(team), reason, now.UnixNano()); err != nil {
		return err
	}
	return RecordBusinessEvent(tx, BusinessEvent{
		Team: team, Kind: BusinessEventCardRefundDue, SubjectID: p.PaymentIntent, Actor: "card", At: now,
		Attrs: map[string]any{
			"charge_id": p.ChargeID, "attempt_id": p.AttemptID, "reason": reason,
			"amount_micro": p.AmountCents * MicroCreditsPerCent,
		},
	})
}

// safety: matched by payment and by the card that paid, so a warning that came
// first, or one on the same card, stops the grant while another card's payment
// is granted; a non-actionable warning is never refunded, so it stops nothing.
func paymentWarningTx(ctx context.Context, tx *storeTx, paymentIntent, fingerprint string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT warning_id FROM payment_warnings
	  WHERE actionable = 1 AND (payment_intent = ? OR (fingerprint != '' AND fingerprint = ?))
	  ORDER BY created_at LIMIT 1`, paymentIntent, fingerprint).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// safety: debt is usage already consumed, so any card may repay it; a pay-now
// payment of exactly the open debt by a card warned only elsewhere settles it
// and grants nothing beyond it, while a warning on this payment refuses it.
func warnedCardRepaysTx(ctx context.Context, tx *storeTx, kind, paymentIntent string, payable bool) (bool, error) {
	if kind != CardAttemptRecovery || !payable {
		return false, nil
	}
	own, err := rowPresentTx(ctx, tx, `SELECT 1 FROM payment_warnings WHERE actionable = 1 AND payment_intent = ?`,
		paymentIntent)
	return !own, err
}

func settleWarnedTx(ctx context.Context, tx *storeTx, team Team, p CardPayment, warning string, repays bool, now time.Time) error {
	if !repays {
		if _, err := tx.ExecContext(ctx, `UPDATE card_attempts SET status = ?, payment_intent = ?, updated_at = ?
		    WHERE team = ? AND id = ?`, CardAttemptWarned, p.PaymentIntent, now.UnixNano(), string(team), p.AttemptID); err != nil {
			return err
		}
	}
	if err := holdForWarningTx(ctx, tx, team, warning, p.PaymentIntent, now); err != nil {
		return err
	}
	return RecordBusinessEvent(tx, BusinessEvent{
		Team: team, Kind: BusinessEventCardWarned, SubjectID: p.PaymentIntent, Actor: "card", At: now,
		Attrs: map[string]any{"charge_id": p.ChargeID, "attempt_id": p.AttemptID, "warning_id": warning, "repaid_debt": repays},
	})
}

// safety: callers hold the ledger lock, so the check and the insert cannot
// interleave with another hold of the same warning.
func holdForWarningTx(ctx context.Context, tx *storeTx, team Team, warning, paymentIntent string, now time.Time) error {
	// safety: a length prefix prevents colons in team or provider IDs from colliding.
	freezeID := fmt.Sprintf("early_fraud_warning:%d:%s:%s", len(team), team, warning)
	// safety: legacy IDs, including released holds, must stay idempotent on replay.
	if held, err := rowPresentTx(ctx, tx, `SELECT 1 FROM credit_freezes
	    WHERE team = ? AND cause = ? AND dispute_id IN (?, ?)`,
		string(team), FreezeCauseWarning, freezeID, warning); err != nil || held {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO credit_freezes (dispute_id, team, payment_id, reason, cause, created_at)
	    VALUES (?, ?, ?, ?, ?, ?)`,
		freezeID, string(team), paymentIntent, "early fraud warning "+warning, FreezeCauseWarning, now.UnixNano()); err != nil {
		return err
	}
	return RecordBusinessEvent(tx, BusinessEvent{
		At: now, Team: team, Kind: BusinessEventTeamFrozen, SubjectID: freezeID,
		Attrs: map[string]any{"payment_id": paymentIntent, "cause": FreezeCauseWarning, "warning_id": warning},
	})
}

// PaymentWarning is an early fraud warning on a payment.
type PaymentWarning struct {
	WarningID     string
	PaymentIntent string
	Fingerprint   string
	// Actionable is Stripe's word that the payment can still be refunded to
	// head off a dispute; only such a warning stops a grant or holds a team.
	Actionable bool
}

// RecordPaymentWarning stores an early fraud warning whether or not the
// ledger has seen its payment yet, and holds the team the payment funded when
// it knows it. A warned payment, or a later one by the same card, is never
// granted. It reports the team held, empty when the payment is not known yet.
func (s *Store) RecordPaymentWarning(ctx context.Context, w PaymentWarning, now time.Time) (_ Team, err error) {
	if w.WarningID == "" || w.PaymentIntent == "" {
		return "", fmt.Errorf("%w: a warning names itself and its payment", ErrInvalidInput)
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return "", err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return "", err
	}
	actionable := 0
	if w.Actionable {
		actionable = 1
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO payment_warnings (warning_id, payment_intent, fingerprint, actionable,
	    created_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT (warning_id) DO NOTHING`,
		w.WarningID, w.PaymentIntent, w.Fingerprint, actionable, now.UnixNano()); err != nil {
		return "", err
	}
	if !w.Actionable {
		return "", tx.Commit()
	}
	var team string
	err = tx.QueryRowContext(ctx, `SELECT team FROM credit_grants WHERE kind = ? AND reference = ?
	  UNION SELECT team FROM card_attempts WHERE payment_intent = ? LIMIT 1`,
		CreditGrantPaid, w.PaymentIntent, w.PaymentIntent).Scan(&team)
	if errors.Is(err, sql.ErrNoRows) {
		return "", tx.Commit()
	}
	if err != nil {
		return "", err
	}
	if err := holdForWarningTx(ctx, tx, Team(team), w.WarningID, w.PaymentIntent, now); err != nil {
		return "", err
	}
	return Team(team), tx.Commit()
}

// ErrPaymentWarned refuses granting a payment that drew an early fraud
// warning; the warning's refund returns the money.
var ErrPaymentWarned = errors.New("credits: the payment drew an early fraud warning and is refunded, not granted")

func refuseWarnedPaymentTx(ctx context.Context, tx *storeTx, paymentIntent string) error {
	if paymentIntent == "" {
		return nil
	}
	warned, err := rowPresentTx(ctx, tx, `SELECT 1 FROM payment_warnings WHERE actionable = 1 AND payment_intent = ?`,
		paymentIntent)
	if err != nil || !warned {
		return err
	}
	return ErrPaymentWarned
}

// Queued refund states. A refund is done only once Stripe reports it
// succeeded; one that failed or was canceled is due again after a backoff.
const (
	CardRefundDue       = "due"
	CardRefundPending   = "pending"
	CardRefundSucceeded = "succeeded"
)

// safety: a failed refund is retried on a widening schedule, and never
// dropped, because until it succeeds the payer holds neither the money nor
// credits for it.
var cardRefundRetryAfter = []time.Duration{time.Hour, 6 * time.Hour, 24 * time.Hour}

// CardRefundWork is a payment the worker must refund in full. Key is its
// Stripe idempotency key, new for each retry after a failed refund.
type CardRefundWork struct {
	Team          Team
	PaymentIntent string
	Reason        string
	Key           string
}

// DueCardRefunds lists the queued refunds due to be made, first or again.
func (s *Store) DueCardRefunds(ctx context.Context, now time.Time) (_ []CardRefundWork, err error) {
	rows, err := s.query(ctx, `SELECT team, payment_intent, reason, attempts FROM card_refunds
	    WHERE status = ? AND next_attempt_at <= ? ORDER BY created_at`, CardRefundDue, now.UnixNano())
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []CardRefundWork
	for rows.Next() {
		var w CardRefundWork
		var attempts int64
		if err := rows.Scan(&w.Team, &w.PaymentIntent, &w.Reason, &attempts); err != nil {
			return nil, err
		}
		w.Key = fmt.Sprintf("%s-%d", w.PaymentIntent, attempts)
		out = append(out, w)
	}
	return out, rows.Err()
}

// MarkCardRefundMade binds the refund Stripe made for the queued attempt key
// names and the status it answered with. A refund whose outcome its webhook
// already reported is left as that report set it.
func (s *Store) MarkCardRefundMade(
	ctx context.Context, team Team, key, refundID, status string, now time.Time,
) error {
	paymentIntent, attempt, ok := parseRefundKey(key)
	if !ok {
		return fmt.Errorf("%w: refund key %q", ErrInvalidInput, key)
	}
	_, err := s.applyCardRefund(ctx, team, paymentIntent, refundID, status,
		`attempts = ? AND status = 'due'`, []any{attempt}, now)
	return err
}

// ReportCardRefund applies a refund's status as Stripe announces it, for the
// refund made under key, and reports whether it moved the queue; a failed or
// canceled one is due again.
func (s *Store) ReportCardRefund(ctx context.Context, key, refundID, status string, now time.Time) (bool, error) {
	paymentIntent, attempt, ok := parseRefundKey(key)
	if !ok {
		return false, nil
	}
	var team string
	err := s.queryRow(ctx, `SELECT team FROM card_refunds WHERE payment_intent = ?`, paymentIntent).Scan(&team)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// safety: only the current attempt's key moves the queue, even before the
	// worker binds the refund; a failure moves it to the next attempt, so a
	// repeat or a stale attempt matches nothing.
	switch status {
	case CardRefundSucceeded, "failed", "canceled":
		return s.applyCardRefund(ctx, Team(team), paymentIntent, refundID, status, `attempts = ?`, []any{attempt}, now)
	}
	return false, nil
}

func parseRefundKey(key string) (string, int64, bool) {
	i := strings.LastIndexByte(key, '-')
	if i <= 0 {
		return "", 0, false
	}
	attempt, err := strconv.ParseInt(key[i+1:], 10, 64)
	return key[:i], attempt, err == nil && attempt >= 0
}

func (s *Store) applyCardRefund(
	ctx context.Context, team Team, paymentIntent, refundID, status, when string, whenArgs []any, now time.Time,
) (bool, error) {
	set, args := `refund_id = ?, status = ?`, []any{refundID, CardRefundPending}
	switch status {
	case CardRefundSucceeded:
		args[1] = CardRefundSucceeded
	case "failed", "canceled":
		// safety: a refund can fail after it succeeded; its reversal is capped
		// at what the payment still holds, so retrying cannot take back twice.
		set = `refund_id = ?, status = ?, attempts = attempts + 1, next_attempt_at = ?`
		args = []any{refundID, CardRefundDue, now.Add(cardRefundBackoff(ctx, s, team, paymentIntent)).UnixNano()}
	}
	args = append(append(args, string(team), paymentIntent), whenArgs...)
	res, err := s.exec(ctx, `UPDATE card_refunds SET `+set+` WHERE team = ? AND payment_intent = ? AND `+when, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func cardRefundBackoff(ctx context.Context, s *Store, team Team, paymentIntent string) time.Duration {
	var attempts int
	if err := s.queryRow(ctx, `SELECT attempts FROM card_refunds WHERE team = ? AND payment_intent = ?`,
		string(team), paymentIntent).Scan(&attempts); err != nil {
		return cardRefundRetryAfter[0]
	}
	return cardRefundRetryAfter[min(attempts, len(cardRefundRetryAfter)-1)]
}

// FailCardAttempt records a declined or unauthenticated attempt. The charge
// stays open with its retry scheduled, and the team is held, back at New,
// until the charge is paid. first reports the charge's first failure, which
// is when the owners are mailed.
func (s *Store) FailCardAttempt(
	ctx context.Context, attemptID, paymentIntent, declineCode string, now time.Time,
) (team Team, first bool, err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return "", false, err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return "", false, err
	}
	var chargeID, status, name string
	err = tx.QueryRowContext(ctx, `SELECT charge_id, status, team FROM card_attempts WHERE id = ?`, attemptID).
		Scan(&chargeID, &status, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("%w: card attempt %q", ErrNotFound, attemptID)
	}
	if err != nil || status != CardAttemptLive {
		return Team(name), false, err
	}
	nowNS := now.UnixNano()
	if _, err := tx.ExecContext(ctx, `UPDATE card_attempts SET status = ?, payment_intent = ?, decline_code = ?,
	    updated_at = ? WHERE id = ?`, CardAttemptFailed, paymentIntent, declineCode, nowNS, attemptID); err != nil {
		return "", false, err
	}
	var failures int64
	if err := tx.QueryRowContext(ctx, `SELECT failures FROM card_charges WHERE id = ? AND state = ?`,
		chargeID, CardChargeOpen).Scan(&failures); errors.Is(err, sql.ErrNoRows) {
		return Team(name), false, tx.Commit()
	} else if err != nil {
		return "", false, err
	}
	next := int64(1<<63 - 1)
	if int(failures) < len(cardRetryAfter) {
		next = now.Add(cardRetryAfter[failures]).UnixNano()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE card_charges SET failures = failures + 1, decline_code = ?,
	    next_attempt_at = ? WHERE id = ?`, declineCode, next, chargeID); err != nil {
		return "", false, err
	}
	// safety: the hold is keyed by the charge, so every retry of it shares
	// one hold, and paying the charge releases it.
	if _, err := tx.ExecContext(ctx, `INSERT INTO credit_freezes (dispute_id, team, payment_id, reason, cause, created_at)
	    VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (dispute_id) DO NOTHING`, "decline-"+chargeID, name, paymentIntent,
		"card charge declined: "+declineCode, FreezeCauseDecline, nowNS); err != nil {
		return "", false, err
	}
	if err := RecordBusinessEvent(tx, BusinessEvent{
		Team: Team(name), Kind: BusinessEventCardFailed, SubjectID: attemptID, Actor: "card", At: now,
		Attrs: map[string]any{"charge_id": chargeID, "decline_code": declineCode, "failures": failures + 1},
	}); err != nil {
		return "", false, err
	}
	return Team(name), failures == 0, tx.Commit()
}

// safety: the business event's unique subject is what reports a threshold
// once per team per month, whichever controller's worker gets there first.
func budgetAlertTx(ctx context.Context, tx *storeTx, team Team, now time.Time) (*BudgetAlert, error) {
	st, err := spendStandingTx(ctx, tx, team, now)
	if err != nil || st.BudgetMicro <= 0 {
		return nil, err
	}
	pct := int64(0)
	for _, p := range []int64{100, 80, 50} {
		if st.Spent30dMicro*100 >= st.BudgetMicro*p {
			pct = p
			break
		}
	}
	if pct == 0 {
		return nil, nil
	}
	subject := fmt.Sprintf("%s:%d", now.UTC().Format("2006-01"), pct)
	if seen, err := rowPresentTx(ctx, tx, `SELECT 1 FROM business_events WHERE team = ? AND kind = ? AND subject_id = ?`,
		string(team), BusinessEventBudgetAlert, subject); err != nil || seen {
		return nil, err
	}
	if err := RecordBusinessEvent(tx, BusinessEvent{
		Team: team, Kind: BusinessEventBudgetAlert, SubjectID: subject, Actor: "system", At: now,
		Attrs: map[string]any{"percent": pct, "spent_micro": st.Spent30dMicro, "budget_micro": st.BudgetMicro},
	}); err != nil {
		return nil, err
	}
	return &BudgetAlert{Team: team, Percent: pct, Spent30dMicro: st.Spent30dMicro, BudgetMicro: st.BudgetMicro}, nil
}

// TeamOwnerEmails returns the verified email addresses of t's owners.
func (t *Tenant) TeamOwnerEmails(ctx context.Context) (_ []string, err error) {
	rows, err := t.s.query(ctx, `SELECT a.email FROM memberships m JOIN accounts a ON a.id = m.account_id
	  WHERE m.team = ? AND m.role = 'owner' AND a.email_verified = 1 ORDER BY a.email`, string(t.team))
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []string
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, err
		}
		out = append(out, email)
	}
	return out, rows.Err()
}

// AccountEmail returns the email address of an account.
func (s *Store) AccountEmail(ctx context.Context, accountID string) (string, error) {
	var email string
	err := s.queryRow(ctx, `SELECT email FROM accounts WHERE id = ?`, accountID).Scan(&email)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return email, err
}

func teamHasCardTx(ctx context.Context, tx *storeTx, team Team) (bool, error) {
	return rowPresentTx(ctx, tx, `SELECT 1 FROM teams WHERE name = ? AND card_payment_method != ''`, string(team))
}

// DropCardAttempt closes a live attempt that never reached Stripe, without
// counting it as a failed charge.
func (s *Store) DropCardAttempt(ctx context.Context, attemptID string, now time.Time) error {
	_, err := s.exec(ctx, `UPDATE card_attempts SET status = ?, decline_code = 'not_opened', updated_at = ?
	    WHERE id = ? AND status = ?`, CardAttemptFailed, now.UnixNano(), attemptID, CardAttemptLive)
	return err
}
