package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// Credit ledger arithmetic. One credit is one hundredth of a dollar, so a
// ten dollar top-up is a thousand credits. Grants and charges are stored in
// micro-credits because a cloud runner second costs well under one credit
// and an integer ledger must charge it exactly.
//
// At the default rate a cloud runner second costs 0.02 credits, so a minute
// costs 1.2 credits and an hour costs 72 credits ($0.72). Ten dollars is a
// thousand credits, which buys 50,000 cloud runner seconds, just under
// fourteen hours.
const (
	CreditsPerDollar       = 100
	MicroCreditsPerCredit  = 1_000_000
	DefaultCreditRateMicro = 20_000

	// DefaultCreditGraceSeconds is how long a node keeps running after the
	// balance reaches zero before the controller cancels it.
	DefaultCreditGraceSeconds = 60

	// CreditClaimFloorSeconds is the runway a claim reserves up front. The
	// reservation is taken inside the claim transaction and refunded at
	// finish, so it both guarantees a claimed node a minute of execution and
	// bounds how far concurrent runners can drive the balance below zero.
	CreditClaimFloorSeconds = 60

	// DefaultCreditMaxChargeSeconds caps the seconds one charge may bill.
	// Heartbeats arrive every three seconds by default, so the cap engages
	// only when the controller was unreachable or the heartbeat loop stalled,
	// and the customer is not billed for the gap.
	DefaultCreditMaxChargeSeconds = 30

	// MinCreditMaxChargeSeconds is the lowest charge cap an operator may set.
	// Heartbeats arrive every three seconds, so a cap under that forgives part
	// of every ordinary interval and the ledger undercharges steady work
	// instead of only a stall.
	MinCreditMaxChargeSeconds = 3
)

// Credit grant kinds.
const (
	CreditGrantFree = "free"
	CreditGrantPaid = "paid"
)

// Credit charge kinds. A reservation is taken at claim time, usage rows bill
// the intervals a node actually ran, and a refund returns the unused tail of
// a reservation when the node finishes early.
const (
	CreditChargeReservation = "reservation"
	CreditChargeUsage       = "usage"
	CreditChargeRefund      = "refund"
)

// Run event kinds the credit ledger writes.
const (
	// EventKindCreditsBlocked records a claim refused for an empty balance,
	// so a run waiting on a metered runner says why.
	EventKindCreditsBlocked = "credits_blocked"
	// EventKindCreditsExhausted records a node cancelled after the balance
	// reached zero and the grace period elapsed.
	EventKindCreditsExhausted = "credits_exhausted"
)

// ErrInsufficientCredits is returned when a metered runner's controller has
// no balance left to pay for the work it asked for. The claim path returns
// an [InsufficientCreditsError], which wraps it and names the shortfall.
var ErrInsufficientCredits = errors.New("insufficient credits")

// InsufficientCreditsError refuses a claim the balance cannot pay for and
// names both sides of the comparison, so the refusal a runner receives says
// how much is missing.
type InsufficientCreditsError struct {
	BalanceMicro  int64
	RequiredMicro int64
}

func (e *InsufficientCreditsError) Error() string {
	return fmt.Sprintf("insufficient credits: balance %s, need %s",
		FormatCredits(e.BalanceMicro), FormatCredits(e.RequiredMicro))
}

// Unwrap reports [ErrInsufficientCredits], so a caller matches the condition
// with errors.Is without knowing this type.
func (e *InsufficientCreditsError) Unwrap() error { return ErrInsufficientCredits }

const (
	metaKeyCreditRateMicro   = "credit_rate_micro_per_second"
	metaKeyCreditGraceSecs   = "credit_grace_seconds"
	metaKeyCreditMaxCharge   = "credit_max_charge_seconds"
	metaKeyCreditExhaustedAt = "credit_exhausted_at"
)

// CreditHistoryMaxLimit is the most rows of each kind one history read
// returns.
const CreditHistoryMaxLimit = 1000

const creditHistoryDefaultLimit = 200

const creditGrantsTableSQLite = `CREATE TABLE IF NOT EXISTS credit_grants (
    id           TEXT PRIMARY KEY,
    -- free | paid
    kind         TEXT NOT NULL,
    amount_micro INTEGER NOT NULL,
    -- payment id or operator note; empty for an unreferenced grant
    reference    TEXT NOT NULL DEFAULT '',
    created_by   TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_credit_grants_created ON credit_grants(created_at);`

const creditChargesTableSQLite = `CREATE TABLE IF NOT EXISTS credit_charges (
    id           TEXT PRIMARY KEY,
    run_id       TEXT NOT NULL,
    node_id      TEXT NOT NULL,
    token_prefix TEXT NOT NULL,
    -- reservation | usage | refund; a refund carries negative seconds and amount
    kind         TEXT NOT NULL DEFAULT 'usage',
    seconds      INTEGER NOT NULL,
    amount_micro INTEGER NOT NULL,
    -- cpu_class: whole cores of the class billed; 0 for a row written before
    -- the rate table, which was billed at credit_rate_micro_per_second.
    cpu_class    INTEGER NOT NULL DEFAULT 0,
    rate_micro_per_second INTEGER NOT NULL DEFAULT 0,
    charged_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_credit_charges_charged ON credit_charges(charged_at);
CREATE INDEX IF NOT EXISTS idx_credit_charges_node ON credit_charges(run_id, node_id);`

var creditsTablesPostgres = func() string {
	r := strings.NewReplacer("INTEGER", "BIGINT")
	return r.Replace(creditGrantsTableSQLite) + "\n" + r.Replace(creditChargesTableSQLite)
}()

// safety: v35 gained the charge kind while it was still unreleased, so a store
// an earlier commit on this lineage stamped 35 is short of it.
var creditChargeKindCols = map[string]string{
	"kind": "TEXT NOT NULL DEFAULT 'usage'",
}

// safety: a row billed before the rate table carries class 0, which reads as
// the single rate it was charged at rather than a class the table prices.
var creditChargeClassCols = map[string]string{
	"cpu_class":             "INTEGER NOT NULL DEFAULT 0",
	"rate_micro_per_second": "INTEGER NOT NULL DEFAULT 0",
}

// safety: a node claimed before the rate table carries class 0 and keeps
// billing at the single rate, so an upgrade does not reprice work in flight.
var nodesCreditClassCols = map[string]string{
	"credit_cpu_class": "INTEGER NOT NULL DEFAULT 0",
}

// safety: metering trusts this operator-set marker alone, never a runner's
// self-asserted labels.
var tokensMeteredCols = map[string]string{
	"metered": "INTEGER NOT NULL DEFAULT 0",
}

// safety: a claim reserves through this instant, so a charge bills only the
// seconds past it and a requeue that clears it cannot bill the idle gap.
var nodesCreditCols = map[string]string{
	"credit_charged_through": "INTEGER NOT NULL DEFAULT 0",
}

func applyCreditsMigrationSQLite(ctx context.Context, tx *storeTx) error {
	if err := ensureColumnsSQLite(ctx, tx, "tokens", tokensMeteredCols); err != nil {
		return err
	}
	if err := ensureColumnsSQLite(ctx, tx, "nodes", nodesCreditCols); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, creditGrantsTableSQLite); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, creditChargesTableSQLite); err != nil {
		return err
	}
	return ensureColumnsSQLite(ctx, tx, "credit_charges", creditChargeKindCols)
}

func applyCreditClassMigrationSQLite(ctx context.Context, tx *storeTx) error {
	if err := ensureColumnsSQLite(ctx, tx, "credit_charges", creditChargeClassCols); err != nil {
		return err
	}
	return ensureColumnsSQLite(ctx, tx, "nodes", nodesCreditClassCols)
}

func applyCreditClassMigrationPostgres(ctx context.Context, tx *storeTx) error {
	if err := addColumnsTx(ctx, tx, "credit_charges", creditChargeClassCols); err != nil {
		return err
	}
	return addColumnsTx(ctx, tx, "nodes", nodesCreditClassCols)
}

func applyCreditsMigrationPostgres(ctx context.Context, tx *storeTx) error {
	if err := addColumnsTx(ctx, tx, "tokens", tokensMeteredCols); err != nil {
		return err
	}
	if err := addColumnsTx(ctx, tx, "nodes", nodesCreditCols); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, creditsTablesPostgres); err != nil {
		return err
	}
	return addColumnsTx(ctx, tx, "credit_charges", creditChargeKindCols)
}

// CreditGrant is one row of credit_grants: credits the operator added.
type CreditGrant struct {
	ID          string
	Kind        string
	AmountMicro int64
	Reference   string
	CreatedBy   string
	CreatedAt   time.Time
}

// CreditCharge is one row of credit_charges: a claim's reservation, one
// interval of a node's execution, or the refund of a reservation the node
// did not use.
type CreditCharge struct {
	ID          string
	RunID       string
	NodeID      string
	TokenPrefix string
	Kind        string
	Seconds     int64
	AmountMicro int64
	// CPUClassCores is the cpu class the row was billed at, in whole cores,
	// and zero for a row written before the rate table.
	CPUClassCores int64
	// RateMicroPerSecond is the price the row was billed at, which a later
	// change to the rate table leaves alone.
	RateMicroPerSecond int64
	ChargedAt          time.Time
}

// CreditState is the ledger as an operator reads it: what was granted, what
// was spent, what is left, the price of a cloud runner second, and the burn
// over a recent window.
type CreditState struct {
	GrantedMicro       int64
	ChargedMicro       int64
	BalanceMicro       int64
	RateMicroPerSecond int64
	RateTable          CreditRateTable
	GraceSeconds       int64
	MaxChargeSeconds   int64
	BurnWindow         time.Duration
	BurnMicro          int64
	ExhaustedAt        *time.Time
}

// ValidCreditGrantKind reports whether kind is one this ledger stores.
func ValidCreditGrantKind(kind string) bool {
	return kind == CreditGrantFree || kind == CreditGrantPaid
}

// GrantCredits adds credits to the ledger and returns the row it wrote.
// A grant that lifts the balance above zero clears the exhaustion stamp, so
// a node cancelled for an empty balance is the last one cancelled.
func (s *Store) GrantCredits(
	ctx context.Context, kind string, amountMicro int64, reference, createdBy string,
) (_ *CreditGrant, err error) {
	if !ValidCreditGrantKind(kind) {
		return nil, fmt.Errorf("credits: unknown grant kind %q", kind)
	}
	if amountMicro <= 0 {
		return nil, errors.New("credits: grant amount must be positive")
	}
	id, err := newCreditID("grant")
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	grant := &CreditGrant{
		ID: id, Kind: kind, AmountMicro: amountMicro,
		Reference: reference, CreatedBy: createdBy, CreatedAt: now,
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
        INSERT INTO credit_grants (id, kind, amount_micro, reference, created_by, created_at)
        VALUES (?, ?, ?, ?, ?, ?)`,
		id, kind, amountMicro, reference, createdBy, now.UnixNano()); err != nil {
		return nil, fmt.Errorf("credits: insert grant: %w", err)
	}
	balance, err := creditBalanceTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	if balance > 0 {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM sparkwing_meta WHERE key = ?`, metaKeyCreditExhaustedAt); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return grant, nil
}

// CreditBalanceMicro returns grants minus charges, in micro-credits. A
// controller that was never granted anything reads zero.
func (s *Store) CreditBalanceMicro(ctx context.Context) (int64, error) {
	var granted, charged sql.NullInt64
	if err := s.queryRow(ctx, creditBalanceSQL).Scan(&granted, &charged); err != nil {
		return 0, err
	}
	return granted.Int64 - charged.Int64, nil
}

const creditBalanceSQL = `SELECT (SELECT SUM(amount_micro) FROM credit_grants),
        (SELECT SUM(amount_micro) FROM credit_charges)`

func creditBalanceTx(ctx context.Context, tx *storeTx) (int64, error) {
	var granted, charged sql.NullInt64
	if err := tx.QueryRowContext(ctx, creditBalanceSQL).Scan(&granted, &charged); err != nil {
		return 0, err
	}
	return granted.Int64 - charged.Int64, nil
}

// safety: Postgres runs concurrent claims in their own transactions, so the
// reservation that bounds the balance has to serialize on something; SQLite
// allows one connection and is already serial.
func lockCreditLedgerTx(ctx context.Context, tx *storeTx) error {
	if tx.dialect != DialectPostgres {
		return nil
	}
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext(?))`, "sparkwing/credit-ledger")
	return err
}

// CreditState reports the ledger together with the rate, the grace period,
// and the credits burned over window.
func (s *Store) CreditState(ctx context.Context, window time.Duration) (CreditState, error) {
	out := CreditState{BurnWindow: window}
	var granted, charged sql.NullInt64
	if err := s.queryRow(ctx, creditBalanceSQL).Scan(&granted, &charged); err != nil {
		return out, err
	}
	out.GrantedMicro = granted.Int64
	out.ChargedMicro = charged.Int64
	out.BalanceMicro = granted.Int64 - charged.Int64

	if window > 0 {
		var burn sql.NullInt64
		if err := s.queryRow(ctx,
			`SELECT SUM(amount_micro) FROM credit_charges WHERE charged_at >= ?`,
			time.Now().Add(-window).UnixNano()).Scan(&burn); err != nil {
			return out, err
		}
		out.BurnMicro = burn.Int64
	}

	rate, err := s.CreditRateMicroPerSecond(ctx)
	if err != nil {
		return out, err
	}
	out.RateMicroPerSecond = rate
	table, err := s.CreditRateTable(ctx)
	if err != nil {
		return out, err
	}
	out.RateTable = table
	grace, err := s.CreditGraceSeconds(ctx)
	if err != nil {
		return out, err
	}
	out.GraceSeconds = grace
	maxCharge, err := s.CreditMaxChargeSeconds(ctx)
	if err != nil {
		return out, err
	}
	out.MaxChargeSeconds = maxCharge
	exhausted, err := s.creditExhaustedAt(ctx)
	if err != nil {
		return out, err
	}
	out.ExhaustedAt = exhausted
	return out, nil
}

// CreditLedgerTotals is the ledger summed the way a meter reads it: what was
// granted, split by whether the operator paid for it, and what the charge rows
// did with it. Every charge belongs to a metered credential by construction,
// so the charge figures carry no principal split.
type CreditLedgerTotals struct {
	// BalanceMicro is what is left to spend.
	BalanceMicro int64
	// GrantedFreeMicro and GrantedPaidMicro sum the grants of each kind.
	GrantedFreeMicro int64
	GrantedPaidMicro int64
	// ReservedMicro is the runway claims took up front, refunds included, so
	// it is the gross reservation rather than the part never returned.
	ReservedMicro int64
	// ChargedMicro is what execution billed.
	ChargedMicro int64
	// RefundedMicro is the unused reservation tail returned at finish,
	// reported as a positive amount.
	RefundedMicro int64
	// SettledSeconds is the runner seconds the ledger has finished charging
	// for: every charge row less the part of an open reservation a finish
	// would still refund. A reservation therefore enters this total as the
	// node consumes it rather than all at once, so the figure only ever
	// grows and a caller may export it as a counter.
	SettledSeconds int64
}

// CreditLedgerTotals reports the ledger's lifetime sums under one repeatable
// read, so the grant, charge and reservation figures agree with each other
// even while claims land. A controller exports them as metrics, where they
// survive a restart the way an in-process counter does not.
//
// Both sums scan a covering index over a table that is never pruned, so a
// caller samples them on a slow timer rather than on every sweep.
func (s *Store) CreditLedgerTotals(ctx context.Context) (_ CreditLedgerTotals, err error) {
	var out CreditLedgerTotals
	tx, err := s.beginSnapshotReadTx(ctx)
	if err != nil {
		return out, err
	}
	defer rollbackUnlessDone(tx, &err)

	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(CASE WHEN kind = ? THEN amount_micro ELSE 0 END), 0),
                        COALESCE(SUM(CASE WHEN kind = ? THEN amount_micro ELSE 0 END), 0)
                   FROM credit_grants`,
		CreditGrantFree, CreditGrantPaid,
	).Scan(&out.GrantedFreeMicro, &out.GrantedPaidMicro); err != nil {
		return out, err
	}
	var settled, charged int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(CASE WHEN kind = ? THEN amount_micro ELSE 0 END), 0),
                        COALESCE(SUM(CASE WHEN kind = ? THEN amount_micro ELSE 0 END), 0),
                        COALESCE(SUM(CASE WHEN kind = ? THEN -amount_micro ELSE 0 END), 0),
                        COALESCE(SUM(amount_micro), 0),
                        COALESCE(SUM(seconds), 0)
                   FROM credit_charges`,
		CreditChargeReservation, CreditChargeUsage, CreditChargeRefund,
	).Scan(&out.ReservedMicro, &out.ChargedMicro, &out.RefundedMicro,
		&settled, &charged); err != nil {
		return out, err
	}

	// safety: counting a reservation whole would make the seconds total fall
	// by its refund, so the part a finish right now would still return is held
	// back. The database's clock measures that part, because a sampler's own
	// clock lets a second controller or an NTP step pull the figure backwards.
	var refundable int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(CASE WHEN credit_charged_through / 1000000000 > `+s.nowSeconds()+`
                                         THEN credit_charged_through / 1000000000 - `+s.nowSeconds()+`
                                         ELSE 0 END), 0)
                   FROM nodes WHERE credit_charged_through != 0`,
	).Scan(&refundable); err != nil {
		return out, err
	}
	out.SettledSeconds = charged - refundable

	out.BalanceMicro = out.GrantedFreeMicro + out.GrantedPaidMicro - settled
	return out, tx.Commit()
}

// CreditRateMicroPerSecond returns the price of one cloud runner second in
// micro-credits. A controller that never set one reads the default.
func (s *Store) CreditRateMicroPerSecond(ctx context.Context) (int64, error) {
	return s.creditSetting(ctx, metaKeyCreditRateMicro, DefaultCreditRateMicro)
}

// SetCreditRateMicroPerSecond prices a cloud runner second. Charges already
// written keep the rate they were charged at.
func (s *Store) SetCreditRateMicroPerSecond(ctx context.Context, micro int64) error {
	if micro < 0 {
		return errors.New("credits: rate must not be negative")
	}
	return s.setCreditSetting(ctx, metaKeyCreditRateMicro, micro)
}

// CreditGraceSeconds returns how long a running node survives an empty
// balance before the controller cancels it.
func (s *Store) CreditGraceSeconds(ctx context.Context) (int64, error) {
	return s.creditSetting(ctx, metaKeyCreditGraceSecs, DefaultCreditGraceSeconds)
}

// SetCreditGraceSeconds sets the window between an empty balance and the
// cancellation of the nodes still running on it.
func (s *Store) SetCreditGraceSeconds(ctx context.Context, secs int64) error {
	if secs < 0 {
		return errors.New("credits: grace must not be negative")
	}
	return s.setCreditSetting(ctx, metaKeyCreditGraceSecs, secs)
}

// CreditMaxChargeSeconds returns the most seconds one charge may bill, which
// is what keeps a controller outage or a stalled heartbeat loop from billing
// the gap it left behind.
func (s *Store) CreditMaxChargeSeconds(ctx context.Context) (int64, error) {
	return s.creditSetting(ctx, metaKeyCreditMaxCharge, DefaultCreditMaxChargeSeconds)
}

// SetCreditMaxChargeSeconds caps the seconds one charge may bill.
func (s *Store) SetCreditMaxChargeSeconds(ctx context.Context, secs int64) error {
	if secs <= 0 {
		return errors.New("credits: the charge cap must be positive")
	}
	return s.setCreditSetting(ctx, metaKeyCreditMaxCharge, secs)
}

func (s *Store) creditSetting(ctx context.Context, key string, fallback int64) (int64, error) {
	var raw string
	err := s.queryRow(ctx, `SELECT value FROM sparkwing_meta WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return fallback, nil
	}
	if err != nil {
		return 0, err
	}
	return parseCreditSetting(raw, fallback), nil
}

func creditSettingTx(ctx context.Context, tx *storeTx, key string, fallback int64) (int64, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT value FROM sparkwing_meta WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return fallback, nil
	}
	if err != nil {
		return 0, err
	}
	return parseCreditSetting(raw, fallback), nil
}

// safety: a settings row nothing can parse must not break reading a balance,
// so the default stands and the unusable value is named in the log.
func parseCreditSetting(raw string, fallback int64) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		slog.Warn("credits: a stored setting is not an integer; using the default",
			"value", raw, "default", fallback, "err", err)
		return fallback
	}
	return v
}

func (s *Store) setCreditSetting(ctx context.Context, key string, v int64) error {
	_, err := s.exec(ctx, upsertCreditSettingSQL, key, formatCreditSetting(v), time.Now().UnixNano())
	return err
}

func setCreditSettingTx(ctx context.Context, tx *storeTx, key, value string) error {
	_, err := tx.ExecContext(ctx, upsertCreditSettingSQL, key, value, time.Now().UnixNano())
	return err
}

const upsertCreditSettingSQL = `
        INSERT INTO sparkwing_meta (key, value, updated_at) VALUES (?, ?, ?)
        ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`

func formatCreditSetting(v int64) string { return strconv.FormatInt(v, 10) }

// safety: a setting the store has never held reads as the empty string, which
// is what lets a caller tell "never set" from a value it cannot parse.
func (s *Store) creditSettingRaw(ctx context.Context, key string) (string, error) {
	var raw string
	err := s.queryRow(ctx, selectCreditSettingSQL, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return raw, err
}

func creditSettingRawTx(ctx context.Context, tx *storeTx, key string) (string, error) {
	var raw string
	err := tx.QueryRowContext(ctx, selectCreditSettingSQL, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return raw, err
}

const selectCreditSettingSQL = `SELECT value FROM sparkwing_meta WHERE key = ?`

func (s *Store) creditExhaustedAt(ctx context.Context) (*time.Time, error) {
	var raw string
	err := s.queryRow(ctx, `SELECT value FROM sparkwing_meta WHERE key = ?`, metaKeyCreditExhaustedAt).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ns := parseCreditSetting(raw, 0)
	if ns == 0 {
		return nil, nil
	}
	at := time.Unix(0, ns).UTC()
	return &at, nil
}

// ListCreditGrants returns grants newest first, at most limit rows and never
// more than [CreditHistoryMaxLimit].
func (s *Store) ListCreditGrants(ctx context.Context, limit int) (_ []CreditGrant, err error) {
	rows, err := s.query(ctx, `SELECT id, kind, amount_micro, reference, created_by, created_at
	  FROM credit_grants ORDER BY created_at DESC, id DESC LIMIT ?`, creditLimit(limit))
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []CreditGrant
	for rows.Next() {
		var g CreditGrant
		var created int64
		if err := rows.Scan(&g.ID, &g.Kind, &g.AmountMicro, &g.Reference, &g.CreatedBy, &created); err != nil {
			return nil, err
		}
		g.CreatedAt = time.Unix(0, created).UTC()
		out = append(out, g)
	}
	return out, rows.Err()
}

// ListCreditCharges returns charges newest first, at most limit rows and
// never more than [CreditHistoryMaxLimit].
func (s *Store) ListCreditCharges(ctx context.Context, limit int) (_ []CreditCharge, err error) {
	rows, err := s.query(ctx, `SELECT id, run_id, node_id, token_prefix, kind, seconds, amount_micro,
	         cpu_class, rate_micro_per_second, charged_at
	  FROM credit_charges ORDER BY charged_at DESC, id DESC LIMIT ?`, creditLimit(limit))
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []CreditCharge
	for rows.Next() {
		var c CreditCharge
		var charged int64
		if err := rows.Scan(&c.ID, &c.RunID, &c.NodeID, &c.TokenPrefix, &c.Kind,
			&c.Seconds, &c.AmountMicro, &c.CPUClassCores, &c.RateMicroPerSecond, &charged); err != nil {
			return nil, err
		}
		c.ChargedAt = time.Unix(0, charged).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

func creditLimit(limit int) int {
	if limit <= 0 {
		return creditHistoryDefaultLimit
	}
	if limit > CreditHistoryMaxLimit {
		return CreditHistoryMaxLimit
	}
	return limit
}

// CreditClaimFloorMicro is one minute of cloud runner time at the four-core
// rate. A claim reserves this minute at the class of the node it takes, so a
// claim on another class reserves the same minute at that class's price.
func (s *Store) CreditClaimFloorMicro(ctx context.Context) (int64, error) {
	rate, err := s.CreditRateMicroPerSecond(ctx)
	if err != nil {
		return 0, err
	}
	return rate * CreditClaimFloorSeconds, nil
}

// safety: reserving inside the claim's own transaction is what keeps concurrent
// runners from each reading the same balance and claiming against it.
func reserveNodeCreditsTx(
	ctx context.Context, tx *storeTx, claimant ClaimIdentity, runID, nodeID string, now time.Time,
) error {
	metered, err := tokenMeteredTx(ctx, tx, claimant.TokenPrefix)
	if err != nil || !metered {
		return err
	}
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return err
	}
	table, err := creditRateTableTx(ctx, tx)
	if err != nil {
		return err
	}
	class, err := nodeCreditClassTx(ctx, tx, table, runID, nodeID)
	if err != nil {
		return err
	}
	required := class.MicroPerSecond * CreditClaimFloorSeconds
	balance, err := creditBalanceTx(ctx, tx)
	if err != nil {
		return err
	}
	// safety: an empty balance is the refusal a runner already understands, so
	// it is reported before a guard that would mask it with a different code.
	if balance < required {
		return &InsufficientCreditsError{BalanceMicro: balance, RequiredMicro: required}
	}
	limits, err := computeLimitsTx(ctx, tx)
	if err != nil {
		return err
	}
	if limits.Any() {
		if err := enforceClaimComputeLimitsTx(ctx, tx, limits, claimant, runID, now); err != nil {
			return err
		}
	}
	id, err := newCreditID("charge")
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, insertCreditChargeSQL,
		id, runID, nodeID, claimant.TokenPrefix, CreditChargeReservation,
		int64(CreditClaimFloorSeconds), required, class.Cores, class.MicroPerSecond,
		now.UnixNano()); err != nil {
		return fmt.Errorf("credits: reserve: %w", err)
	}
	through := now.Add(CreditClaimFloorSeconds * time.Second).UnixNano()
	_, err = tx.ExecContext(ctx,
		`UPDATE nodes SET credit_charged_through = ?, credit_cpu_class = ?
		  WHERE run_id = ? AND node_id = ?`,
		through, class.Cores, runID, nodeID)
	return err
}

const insertCreditChargeSQL = `
        INSERT INTO credit_charges (id, run_id, node_id, token_prefix, kind, seconds, amount_micro,
                cpu_class, rate_micro_per_second, charged_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// CreditChargeResult is what one charge did to the ledger.
type CreditChargeResult struct {
	// Charge is the row written, or nil when the charge window had not
	// advanced by a whole second.
	Charge *CreditCharge
	// BalanceMicro is the balance after the charge.
	BalanceMicro int64
	// ExhaustedFor is how long the balance has been empty, zero while it
	// holds credit.
	ExhaustedFor time.Duration
	// Cancel reports that the balance has been empty for longer than the
	// grace period, so the caller must stop the node.
	Cancel bool
	// ForgivenSeconds is the gap the charge cap refused to bill, which is
	// non-zero only after the controller or the heartbeat loop stalled.
	ForgivenSeconds int64
}

// ChargeNodeCredits bills the seconds this node has run since its previous
// charge and reports whether the balance can still pay for it. Charging is
// idempotent within a second: a second call in the same second advances
// nothing and writes no row. A node still inside its claim reservation is
// charged nothing, because the reservation already paid for that minute.
func (s *Store) ChargeNodeCredits(ctx context.Context, runID, nodeID, tokenPrefix string, now time.Time) (CreditChargeResult, error) {
	return s.chargeNode(ctx, runID, nodeID, tokenPrefix, now, false)
}

// FinalizeNodeCredits settles a metered node when it stops running: it bills
// the tail since the last charge, refunds whatever is left of the claim
// reservation, and releases the node's charge window so a later attempt
// starts its own. It is a no-op for a node that was never metered and for one
// already settled.
func (s *Store) FinalizeNodeCredits(ctx context.Context, runID, nodeID, tokenPrefix string, now time.Time) (CreditChargeResult, error) {
	return s.chargeNode(ctx, runID, nodeID, tokenPrefix, now, true)
}

func (s *Store) chargeNode(
	ctx context.Context, runID, nodeID, tokenPrefix string, now time.Time, final bool,
) (_ CreditChargeResult, err error) {
	var out CreditChargeResult
	tx, err := s.beginTx(ctx)
	if err != nil {
		return out, err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return out, err
	}
	table, err := creditRateTableTx(ctx, tx)
	if err != nil {
		return out, err
	}
	grace, err := creditSettingTx(ctx, tx, metaKeyCreditGraceSecs, DefaultCreditGraceSeconds)
	if err != nil {
		return out, err
	}
	maxCharge, err := creditSettingTx(ctx, tx, metaKeyCreditMaxCharge, DefaultCreditMaxChargeSeconds)
	if err != nil {
		return out, err
	}

	var anchor, class int64
	err = tx.QueryRowContext(ctx,
		`SELECT credit_charged_through, credit_cpu_class FROM nodes
		  WHERE run_id = ? AND node_id = ?`+tx.forUpdate(),
		runID, nodeID).Scan(&anchor, &class)
	if errors.Is(err, sql.ErrNoRows) {
		return out, notFound("node", runID+"/"+nodeID)
	}
	if err != nil {
		return out, err
	}

	nowNS := now.UnixNano()
	rate := chargeRate(table, class)
	refundRate, err := refundRateTx(ctx, tx, runID, nodeID, rate, final && nowNS <= anchor)
	if err != nil {
		return out, err
	}
	charge, forgiven, through, err := settleChargeWindow(
		ctx, tx, chargeWindow{
			RunID: runID, NodeID: nodeID, TokenPrefix: tokenPrefix,
			Anchor: anchor, NowNS: nowNS, Rate: rate, RefundRate: refundRate, Class: class,
			MaxCharge: maxCharge, Final: final,
		})
	if err != nil {
		return out, err
	}
	out.Charge = charge
	out.ForgivenSeconds = forgiven
	if through != anchor {
		if _, err := tx.ExecContext(ctx,
			`UPDATE nodes SET credit_charged_through = ? WHERE run_id = ? AND node_id = ?`,
			through, runID, nodeID); err != nil {
			return out, err
		}
	}
	if final && anchor == 0 {
		return out, tx.Commit()
	}

	balance, err := creditBalanceTx(ctx, tx)
	if err != nil {
		return out, err
	}
	out.BalanceMicro = balance
	exhaustedFor, cancel, err := settleCreditExhaustionTx(ctx, tx, balance, nowNS, grace)
	if err != nil {
		return out, err
	}
	if err := tx.Commit(); err != nil {
		return out, err
	}
	out.ExhaustedFor = exhaustedFor
	out.Cancel = cancel
	return out, nil
}

type chargeWindow struct {
	RunID, NodeID, TokenPrefix         string
	Anchor, NowNS                      int64
	Rate, RefundRate, Class, MaxCharge int64
	Final                              bool
}

// safety: the tail of a reservation is returned at the price it was taken at,
// because a table raised between the claim and the finish would otherwise
// refund more than the reservation took out.
func refundRateTx(
	ctx context.Context, tx *storeTx, runID, nodeID string, fallback int64, refunding bool,
) (int64, error) {
	if !refunding {
		return fallback, nil
	}
	var reserved int64
	err := tx.QueryRowContext(ctx,
		`SELECT rate_micro_per_second FROM credit_charges
		  WHERE run_id = ? AND node_id = ? AND kind = ?
		  ORDER BY charged_at DESC, id DESC LIMIT 1`,
		runID, nodeID, CreditChargeReservation).Scan(&reserved)
	if errors.Is(err, sql.ErrNoRows) {
		return fallback, nil
	}
	if err != nil {
		return 0, err
	}
	if reserved <= 0 {
		return fallback, nil
	}
	return reserved, nil
}

// safety: a node claimed before the rate table carries no class, so it keeps
// billing at the four-core price the single rate setting holds and an upgrade
// leaves work in flight at the price it started on.
func chargeRate(table CreditRateTable, class int64) int64 {
	if class <= 0 {
		return table.BaseRate()
	}
	return table.RateFor(class)
}

// safety: the returned instant is zero once a final settle has released the
// window, so a later attempt on the same node anchors its own.
func settleChargeWindow(ctx context.Context, tx *storeTx, w chargeWindow) (*CreditCharge, int64, int64, error) {
	// safety: a node claimed before this schema carries no anchor, so charging
	// starts at this call rather than billing every second before it.
	if w.Anchor == 0 {
		if w.Final {
			return nil, 0, 0, nil
		}
		return nil, 0, w.NowNS, nil
	}

	if w.NowNS <= w.Anchor {
		if !w.Final {
			return nil, 0, w.Anchor, nil
		}
		refund := (w.Anchor - w.NowNS) / int64(time.Second)
		if refund <= 0 {
			return nil, 0, 0, nil
		}
		charge, err := insertCreditChargeTx(ctx, tx, w.refunding(), CreditChargeRefund, -refund,
			-w.RefundRate*refund)
		return charge, 0, 0, err
	}

	seconds := (w.NowNS - w.Anchor) / int64(time.Second)
	forgiven := int64(0)
	through := w.Anchor + seconds*int64(time.Second)
	if w.MaxCharge > 0 && seconds > w.MaxCharge {
		forgiven = seconds - w.MaxCharge
		seconds = w.MaxCharge
		// safety: the gap is forgiven rather than deferred, so the anchor
		// catches up to now instead of lagging by it on every later charge.
		through = w.NowNS
	}
	settled := through
	if w.Final {
		settled = 0
	}
	if seconds <= 0 {
		return nil, forgiven, settled, nil
	}
	charge, err := insertCreditChargeTx(ctx, tx, w, CreditChargeUsage, seconds, w.Rate*seconds)
	return charge, forgiven, settled, err
}

// safety: the row records the price it moved credits at, which for a refund is
// the reservation's price rather than the one in force now.
func (w chargeWindow) refunding() chargeWindow {
	w.Rate = w.RefundRate
	return w
}

func insertCreditChargeTx(
	ctx context.Context, tx *storeTx, w chargeWindow, kind string, seconds, amount int64,
) (*CreditCharge, error) {
	id, err := newCreditID("charge")
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, insertCreditChargeSQL,
		id, w.RunID, w.NodeID, w.TokenPrefix, kind, seconds, amount,
		w.Class, w.Rate, w.NowNS); err != nil {
		return nil, fmt.Errorf("credits: insert charge: %w", err)
	}
	return &CreditCharge{
		ID: id, RunID: w.RunID, NodeID: w.NodeID, TokenPrefix: w.TokenPrefix,
		Kind: kind, Seconds: seconds, AmountMicro: amount,
		CPUClassCores: w.Class, RateMicroPerSecond: w.Rate,
		ChargedAt: time.Unix(0, w.NowNS).UTC(),
	}, nil
}

func settleCreditExhaustionTx(
	ctx context.Context, tx *storeTx, balance, nowNS, grace int64,
) (time.Duration, bool, error) {
	if balance > 0 {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM sparkwing_meta WHERE key = ?`, metaKeyCreditExhaustedAt); err != nil {
			return 0, false, err
		}
		return 0, false, nil
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO sparkwing_meta (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT (key) DO NOTHING`,
		metaKeyCreditExhaustedAt, strconv.FormatInt(nowNS, 10), nowNS); err != nil {
		return 0, false, err
	}
	var raw string
	if err := tx.QueryRowContext(ctx,
		`SELECT value FROM sparkwing_meta WHERE key = ?`, metaKeyCreditExhaustedAt).Scan(&raw); err != nil {
		return 0, false, err
	}
	stamped := parseCreditSetting(raw, nowNS)
	exhaustedFor := time.Duration(nowNS - stamped)
	if exhaustedFor < 0 {
		exhaustedFor = 0
	}
	return exhaustedFor, exhaustedFor >= time.Duration(grace)*time.Second, nil
}

// TokenMetered reports whether the operator marked this token's holder as a
// runner credits pay for. An unknown prefix is not metered.
func (s *Store) TokenMetered(ctx context.Context, prefix string) (bool, error) {
	if prefix == "" {
		return false, nil
	}
	var metered int64
	err := s.queryRow(ctx, `SELECT metered FROM tokens WHERE prefix = ?`, prefix).Scan(&metered)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return metered != 0, nil
}

func tokenMeteredTx(ctx context.Context, tx *storeTx, prefix string) (bool, error) {
	if prefix == "" {
		return false, nil
	}
	var metered int64
	err := tx.QueryRowContext(ctx, `SELECT metered FROM tokens WHERE prefix = ?`, prefix).Scan(&metered)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return metered != 0, nil
}

// SetTokenMetered marks or unmarks a minted token as one credits pay for,
// which is how a warm pool already running gets its marker.
func (s *Store) SetTokenMetered(ctx context.Context, prefix string, metered bool) error {
	value := 0
	if metered {
		value = 1
	}
	res, err := s.exec(ctx, `UPDATE tokens SET metered = ? WHERE prefix = ?`, value, prefix)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound("token", prefix)
	}
	return nil
}

// AppendEventOnce writes an event unless the run or node already carries one
// of that kind, which keeps a condition a poller re-observes every half
// second to one row. It reports whether it wrote.
func (s *Store) AppendEventOnce(ctx context.Context, runID, nodeID, kind string, payload []byte) (_ bool, err error) {
	// safety: the common call finds the event already there, so the read comes
	// before the transaction rather than inside one opened twice a second.
	present, err := s.eventKindPresent(ctx, runID, nodeID, kind)
	if err != nil || present {
		return false, err
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackUnlessDone(tx, &err)
	var existing int
	err = tx.QueryRowContext(ctx,
		`SELECT 1 FROM events WHERE run_id = ? AND node_id = ? AND kind = ? LIMIT 1`,
		runID, nodeID, kind).Scan(&existing)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if _, err := appendEventTx(ctx, tx, runID, nodeID, kind, payload, time.Now()); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) eventKindPresent(ctx context.Context, runID, nodeID, kind string) (bool, error) {
	var existing int
	err := s.queryRow(ctx,
		`SELECT 1 FROM events WHERE run_id = ? AND node_id = ? AND kind = ? LIMIT 1`,
		runID, nodeID, kind).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// CancelNodeForExhaustedCredits fails a running node whose controller ran out
// of credit, releases its claim, and records the reason on the run. It
// settles the node's credits first, so the ledger stops at the instant the
// node did.
func (s *Store) CancelNodeForExhaustedCredits(
	ctx context.Context, runID, nodeID, tokenPrefix string, now time.Time,
) error {
	return s.cancelMeteredNode(ctx, runID, nodeID, tokenPrefix,
		FailureCreditsExhausted, "credit balance exhausted", now)
}

// CancelNodeForComputeLimit fails a running node a compute guard stopped,
// releases its claim, and records the reason on the run. It settles the
// node's credits first, so a metered node is billed to the instant it
// stopped.
func (s *Store) CancelNodeForComputeLimit(
	ctx context.Context, runID, nodeID, tokenPrefix, limit string, now time.Time,
) error {
	return s.cancelMeteredNode(ctx, runID, nodeID, tokenPrefix,
		FailureComputeLimit, "compute limit "+limit+" reached", now)
}

func (s *Store) cancelMeteredNode(
	ctx context.Context, runID, nodeID, tokenPrefix, reason, message string, now time.Time,
) (err error) {
	if _, err := s.FinalizeNodeCredits(ctx, runID, nodeID, tokenPrefix, now); err != nil {
		return err
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := lockExecutorEligibilityTx(ctx, tx, false); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE nodes
   SET `+nodeFailSet+`, error = ?, failure_reason = ?, finished_at = ?,
       claimed_by = NULL, claim_principal = '', claim_token_prefix = '',
       claim_executor = '', claim_cores = 0, claim_memory_bytes = 0,
       claim_reservation = '', claim_slot = -1, lease_expires_at = NULL,
       ready_at = NULL, offer_started_at = NULL, reservation_id = '',
       credit_charged_through = 0
 WHERE run_id = ? AND node_id = ? AND `+nodeNotDone,
		message, reason, now.UnixNano(), runID, nodeID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM node_claim_offers WHERE run_id = ? AND node_id = ?`, runID, nodeID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE node_execution_attempts
   SET finished_at = COALESCE(finished_at, ?), outcome = CASE WHEN finished_at IS NULL THEN 'failed' ELSE outcome END,
       failure_reason = CASE WHEN finished_at IS NULL THEN ? ELSE failure_reason END
 WHERE run_id = ? AND node_id = ? AND finished_at IS NULL`,
		now.UnixNano(), reason, runID, nodeID); err != nil {
		return err
	}
	return tx.Commit()
}

// OldestWaitingReadyNode names the ready node a claim would have been given,
// so a refusal can be recorded against the run that is waiting for it. It
// returns empty strings when nothing is waiting.
func (s *Store) OldestWaitingReadyNode(ctx context.Context) (runID, nodeID string, err error) {
	err = s.queryRow(ctx, `SELECT run_id, node_id FROM nodes
	  WHERE ready_at IS NOT NULL AND claimed_by IS NULL AND `+nodeNotDone+`
	  ORDER BY ready_at ASC LIMIT 1`).Scan(&runID, &nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	return runID, nodeID, nil
}

func newCreditID(prefix string) (string, error) {
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(suffix[:]), nil
}

// FormatCredits renders micro-credits as credits with two decimal places,
// which is the resolution an operator reads a balance at.
func FormatCredits(micro int64) string {
	neg := ""
	if micro < 0 {
		neg = "-"
		micro = -micro
	}
	whole := micro / MicroCreditsPerCredit
	cents := (micro % MicroCreditsPerCredit) * 100 / MicroCreditsPerCredit
	return fmt.Sprintf("%s%d.%02d", neg, whole, cents)
}
