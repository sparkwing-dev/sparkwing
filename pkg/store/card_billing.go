package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Spend limits per level, in cents. A team may spend its 30-day ceiling over
// [SpendWindowDays] and its daily cap in one UTC day, whether prepaid or billed
// to its card; a trusted team with a card on file may also run its balance
// down to twice its rung before new work waits for the charge.
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
	SpendLimitBudget       = "budget"
	SpendLimitChargeFailed = "charge_failed"
)

// Business event kinds for card billing.
const (
	BusinessEventCardAdded      = "card.added"
	BusinessEventCardCharged    = "card.charge_paid"
	BusinessEventCardFailed     = "card.charge_failed"
	BusinessEventCardDuplicate  = "card.payment_duplicate"
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

	CardAttemptOffSession = "off_session"
	CardAttemptRecovery   = "recovery"
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
}

// safety: one open charge per team and one live attempt per charge are
// unique indexes, so a second worker's insert fails rather than charging the
// card twice; the attempt id is the Stripe idempotency key.
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
CREATE TABLE IF NOT EXISTS team_spend_days (
    team         TEXT NOT NULL,
    day          INTEGER NOT NULL,
    amount_micro INTEGER NOT NULL,
    PRIMARY KEY (team, day)
);`

// safety: the backfill carries all past spend into the buckets, so a team
// that spent up to a limit before the upgrade is not handed it again.
// perf: the automatic trust rule reads lifetime spend on every claim and
// heartbeat, so it sums these day rows instead of every charge.
const spendBackfillSQL = `INSERT INTO team_spend_days (team, day, amount_micro)
SELECT team, charged_at / 86400000000000, SUM(amount_micro) FROM credit_charges
 GROUP BY team, charged_at / 86400000000000
ON CONFLICT (team, day) DO NOTHING`

// safety: v87 lets a trusted team's balance go negative and counts every
// charge into the spend buckets; a binary predating it would neither honor
// the card billing nor count spend, so it refuses the store.
const cardBillingRequirement = "card-billing-v1"

func applyCardBillingMigration(ctx context.Context, tx *storeTx, postgres bool) error {
	cols, script := cardBillingTeamCols, cardBillingTablesSQL
	if postgres {
		cols = make(map[string]string, len(cardBillingTeamCols))
		for k, v := range cardBillingTeamCols {
			cols[k] = strings.ReplaceAll(v, "INTEGER", "BIGINT")
		}
		script = strings.ReplaceAll(script, "INTEGER", "BIGINT")
		if err := addColumnsTx(ctx, tx, "teams", cols); err != nil {
			return err
		}
	} else if err := ensureColumnsSQLite(ctx, tx, "teams", cols); err != nil {
		return err
	}
	if err := execStatements(ctx, tx, script); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, spendBackfillSQL)
	return err
}

// safety: a refund lands on the day of the charge it refunds, so refunding
// yesterday's reservation cannot free room under today's cap.
func addSpendTx(ctx context.Context, tx *storeTx, team Team, atNS, amount int64) error {
	if amount == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO team_spend_days (team, day, amount_micro) VALUES (?, ?, ?)
		ON CONFLICT (team, day) DO UPDATE SET amount_micro = team_spend_days.amount_micro + excluded.amount_micro`,
		string(team), atNS/dayNS, amount)
	return err
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
// credit its card extends, and its spend against each limit.
type SpendStanding struct {
	Billing          BillingStanding
	Card             Card
	OpenCharge       *CardCharge
	BalanceMicro     int64
	CreditLimitMicro int64
	RungMicro        int64
	CeilingMicro     int64
	DailyCapMicro    int64
	BudgetMicro      int64
	Spent30dMicro    int64
	SpentTodayMicro  int64
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
	for _, c := range []struct {
		room  int64
		limit string
	}{
		{s.CeilingMicro - s.Spent30dMicro, SpendLimitCeiling},
		{s.DailyCapMicro - s.SpentTodayMicro, SpendLimitDailyCap},
		{s.BudgetMicro - s.Spent30dMicro, SpendLimitBudget},
	} {
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
	if err := q.QueryRowContext(ctx, `SELECT
	    COALESCE(SUM(amount_micro), 0),
	    COALESCE(SUM(CASE WHEN day = ? THEN amount_micro ELSE 0 END), 0)
	  FROM team_spend_days WHERE team = ? AND day >= ?`,
		today, string(team), today-SpendWindowDays).Scan(&s.Spent30dMicro, &s.SpentTodayMicro); err != nil {
		return s, err
	}
	if s.BalanceMicro, err = balanceMicroTx(ctx, q, team); err != nil {
		return s, err
	}
	ceiling, daily, rung := int64(NewSpendCeilingCents), int64(NewDailyCapCents), int64(0)
	if s.Billing.Trusted {
		ceiling, daily, rung = TrustedSpendCeilingCents, TrustedDailyCapCents, TrustedRungCents
	}
	if s.Billing.Trust == BillingTrustGranted {
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

// CardChargeWork is one Stripe call the payment worker owes: an off-session
// charge of a team's card for an open charge, under the attempt's id.
type CardChargeWork struct {
	Team          Team
	ChargeID      string
	AttemptID     string
	AmountCents   int64
	Customer      string
	PaymentMethod string
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
	freeze, err := teamCreditFreezeTx(ctx, tx, team)
	if err != nil || freeze.Frozen {
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

// safety: a disputed or fraud-warned team is held, and its card is not
// charged again while the hold stands.
func dueChargesTx(ctx context.Context, tx *storeTx, now time.Time) (_ []CardChargeWork, err error) {
	nowNS := now.UnixNano()
	rows, err := tx.QueryContext(ctx, `SELECT c.id, c.team, c.amount_micro, t.card_customer, t.card_payment_method,
	    COALESCE(a.id, '')
	  FROM card_charges c JOIN teams t ON t.name = c.team
	  LEFT JOIN card_attempts a ON a.charge_id = c.id AND a.status = ?
	  WHERE c.state = ? AND t.card_payment_method != ''
	    AND ((a.id IS NULL AND c.next_attempt_at <= ?)
	      OR (a.kind = ? AND a.updated_at < ?))
	    AND NOT EXISTS (SELECT 1 FROM credit_freezes f WHERE f.team = c.team AND f.released_at IS NULL)
	  ORDER BY c.opened_at`,
		CardAttemptLive, CardChargeOpen, nowNS, CardAttemptOffSession, now.Add(-stalledAttemptAge).UnixNano())
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var work []CardChargeWork
	for rows.Next() {
		var w CardChargeWork
		var amount int64
		if err := rows.Scan(&w.ChargeID, &w.Team, &amount, &w.Customer, &w.PaymentMethod, &w.AttemptID); err != nil {
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
}

// SettleCardPayment is the one path a card payment reaches the ledger by: the
// worker's synchronous answer, the webhook and a recovery Checkout all land
// here. It writes the paid grant under the payment intent, so a repeat is a
// no-op, and closes the attempt and the charge. A payment for a charge
// already paid is still granted, because the money arrived, and is recorded
// as a duplicate for the operator to refund.
func (s *Store) SettleCardPayment(ctx context.Context, p CardPayment, now time.Time) (created bool, err error) {
	if p.PaymentIntent == "" || p.AmountCents <= 0 || p.ChargeID == "" {
		return false, fmt.Errorf("%w: a card payment names its charge, payment intent and amount", ErrInvalidInput)
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return false, err
	}
	var team string
	var state string
	err = tx.QueryRowContext(ctx, `SELECT team, state FROM card_charges WHERE id = ?`, p.ChargeID).Scan(&team, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("%w: card charge %q", ErrNotFound, p.ChargeID)
	}
	if err != nil {
		return false, err
	}
	if p.Team != "" && Team(team) != p.Team {
		return false, fmt.Errorf("%w: card charge %q belongs to another team", ErrInvalidInput, p.ChargeID)
	}
	if _, found, err := creditGrantByReferenceTx(ctx, tx, "", CreditGrantPaid, p.PaymentIntent); err != nil || found {
		return false, err
	}
	nowNS := now.UnixNano()
	amount := p.AmountCents * MicroCreditsPerCent
	id, err := newCreditID("grant")
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO credit_grants (team, id, kind, amount_micro, reference, reverses,
	    created_by, created_at) VALUES (?, ?, ?, ?, ?, '', 'card', ?)`,
		team, id, CreditGrantPaid, amount, p.PaymentIntent, nowNS); err != nil {
		return false, fmt.Errorf("card billing: grant payment: %w", err)
	}
	if p.AttemptID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE card_attempts SET status = ?, payment_intent = ?, updated_at = ?
		    WHERE id = ? AND charge_id = ?`, CardAttemptPaid, p.PaymentIntent, nowNS, p.AttemptID, p.ChargeID); err != nil {
			return false, err
		}
	}
	kind := BusinessEventCardCharged
	if state == CardChargeOpen {
		if _, err := tx.ExecContext(ctx, `UPDATE card_charges SET state = ?, payment_intent = ?, closed_at = ?
		    WHERE id = ?`, CardChargePaid, p.PaymentIntent, nowNS, p.ChargeID); err != nil {
			return false, err
		}
	} else {
		kind = BusinessEventCardDuplicate
	}
	if err := RecordBusinessEvent(tx, BusinessEvent{
		Team: Team(team), Kind: kind, SubjectID: p.PaymentIntent, Actor: "card", At: now,
		Attrs: map[string]any{"charge_id": p.ChargeID, "attempt_id": p.AttemptID, "amount_micro": amount},
	}); err != nil {
		return false, err
	}
	balance, err := creditBalanceTx(ctx, tx, Team(team))
	if err != nil {
		return false, err
	}
	if balance > 0 {
		if err := clearTeamCreditExhaustedTx(ctx, tx, Team(team), nowNS); err != nil {
			return false, err
		}
	}
	// safety: what is still owed opens the next charge in this transaction,
	// so a payment that settles less than the debt never leaves it unbilled.
	if err := decideCardChargeTx(ctx, tx, Team(team), now); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// FailCardAttempt records a declined or unauthenticated attempt. The charge
// stays open with its retry scheduled, and its failure withdraws the card's
// credit until it is paid. first reports the charge's first failure, which
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
