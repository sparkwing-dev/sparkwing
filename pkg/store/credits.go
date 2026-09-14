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

	// MaxCreditGrantMicro caps the size of one grant at a billion credits,
	// ten million dollars. A ledger sums grants in SQL, so an amount near the
	// integer limit turns a later balance read into an overflow rather than a
	// number, and no real payment reaches this ceiling.
	MaxCreditGrantMicro = 1_000_000_000_000_000

	// DefaultCreditMaxChargeSeconds caps the seconds one charge may bill.
	// Heartbeats arrive every three seconds by default, so the cap engages
	// only when the controller was unreachable or the heartbeat loop stalled,
	// and the customer is not billed for the gap.
	DefaultCreditMaxChargeSeconds = 30

	// MaxCreditRateMicro is the highest price an operator may put on a cloud
	// runner second: a million credits, which is ten thousand dollars. The
	// ceiling is what keeps the arithmetic the ledger does with the rate inside
	// int64: the largest reservation is the rate times CreditClaimFloorSeconds
	// and the largest single charge is the rate times MaxCreditMaxChargeSeconds,
	// and both stay far below the int64 maximum. Without it a rate near that
	// maximum wraps a reservation negative, and a claim the ledger must refuse
	// succeeds.
	MaxCreditRateMicro = 1_000_000_000_000

	// MaxCreditMaxChargeSeconds is the highest charge cap an operator may set.
	// A day is longer than any heartbeat gap worth billing, and the ceiling is
	// the other half of what keeps a charge inside int64.
	MaxCreditMaxChargeSeconds = 86_400
)

// Node heartbeat cadences. A metered node's charge cap has to clear the
// longest of them, so they are declared here, beside the cap that is judged
// against them, and the runners read them from here.
const (
	// PoolHeartbeatInterval is how often a pooled runner renews its claim.
	PoolHeartbeatInterval = 3 * time.Second

	// DispatchedHeartbeatInterval is how often a dispatched node and a warm
	// pool runner renew theirs.
	DispatchedHeartbeatInterval = 5 * time.Second

	// MaxNodeHeartbeatInterval is the longest cadence any metered node
	// renews on.
	MaxNodeHeartbeatInterval = DispatchedHeartbeatInterval
)

// MinCreditMaxChargeSeconds is the lowest charge cap an operator may set: one
// second past the longest heartbeat cadence. A cap at the cadence itself
// truncates every tick that arrives a little late and forgives the remainder,
// so the ledger would undercharge ordinary work rather than only a stall.
const MinCreditMaxChargeSeconds = int64(MaxNodeHeartbeatInterval/time.Second) + 1

// Credit grant kinds. A reversal carries a negative amount and names the
// paid grant it takes back, which is how a refunded card payment leaves the
// ledger.
const (
	CreditGrantFree     = "free"
	CreditGrantPaid     = "paid"
	CreditGrantReversal = "reversal"
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

// ErrCreditGrantConflict is returned when a grant names a kind and reference
// another grant already carries but asks for different terms. A replay of the
// identical request returns the stored grant instead.
var ErrCreditGrantConflict = errors.New("credits: a different grant already carries this reference")

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
    -- free | paid | reversal; a reversal carries a negative amount
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
    charged_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_credit_charges_charged ON credit_charges(charged_at);
CREATE INDEX IF NOT EXISTS idx_credit_charges_node ON credit_charges(run_id, node_id);`

// safety: a payment webhook redelivers until it sees a 2xx, so the reference a
// grant carries is the key that keeps the retry from granting twice. An empty
// reference is an operator note rather than a payment, so it repeats freely.
const creditGrantReferenceIndex = `CREATE UNIQUE INDEX IF NOT EXISTS idx_credit_grants_reference
    ON credit_grants(kind, reference) WHERE reference != ''`

// safety: the runner cap sums one kind of grant over a date range on every
// cache miss, so that pair is an index rather than a scan of the ledger.
const creditGrantKindCreatedIndex = `CREATE INDEX IF NOT EXISTS idx_credit_grants_kind_created
    ON credit_grants(kind, created_at)`

func applyRunnerCapIndexMigration(ctx context.Context, tx *storeTx) error {
	_, err := tx.ExecContext(ctx, creditGrantKindCreatedIndex)
	return err
}

// safety: a reversal names the paid grant's reference here, and its own
// reference is the refund id, so partial refunds of one payment each land.
var creditGrantReversesCols = map[string]string{
	"reverses": "TEXT NOT NULL DEFAULT ''",
}

var creditsTablesPostgres = func() string {
	r := strings.NewReplacer("INTEGER", "BIGINT")
	return r.Replace(creditGrantsTableSQLite) + "\n" + r.Replace(creditChargesTableSQLite)
}()

// safety: v35 gained the charge kind while it was still unreleased, so a store
// an earlier commit on this lineage stamped 35 is short of it.
var creditChargeKindCols = map[string]string{
	"kind": "TEXT NOT NULL DEFAULT 'usage'",
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

// safety: a node's grace clock starts where its paid runway ended, which is a
// per-node instant; a ledger-wide stamp records only when some charge first
// noticed the balance was empty, which lags by a heartbeat or by an idle hour.
var nodesCreditExhaustionCols = map[string]string{
	"credit_exhausted_anchor": "INTEGER NOT NULL DEFAULT 0",
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

func applyCreditReferenceMigrationSQLite(ctx context.Context, tx *storeTx) error {
	return ensureColumnsSQLite(ctx, tx, "credit_grants", creditGrantReversesCols)
}

func applyCreditReferenceMigrationPostgres(ctx context.Context, tx *storeTx) error {
	return addColumnsTx(ctx, tx, "credit_grants", creditGrantReversesCols)
}

// safety: grants written before the reference became a key may already repeat
// one, and refusing to open a ledger over an operator note costs more than the
// index buys; the grant path enforces the rule inside the ledger lock anyway.
// The attempt repeats on every open, so deleting the duplicates enforces the key.
func (s *Store) ensureCreditGrantReferenceIndex(ctx context.Context) error {
	present, err := s.CreditGrantReferenceIndexPresent(ctx)
	if err != nil || present {
		return err
	}
	dupes, err := duplicateGrantReferences(ctx, storeExecer{s: s})
	if err != nil {
		return err
	}
	if len(dupes) > 0 {
		slog.Warn("credits: grants repeat a reference, so the database does not enforce the grant key; "+
			"delete the duplicate rows and restart to enforce it",
			"references", strings.Join(dupes, ", "))
		return nil
	}
	_, err = s.exec(ctx, creditGrantReferenceIndex)
	return err
}

// CreditGrantReferenceIndexPresent reports whether the database enforces one
// grant per kind and non-empty reference. It is false only while grants
// written before the key existed still repeat a reference.
func (s *Store) CreditGrantReferenceIndexPresent(ctx context.Context) (bool, error) {
	q := `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_credit_grants_reference'`
	if s.dialect == DialectPostgres {
		q = `SELECT COUNT(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		     WHERE c.relname = 'idx_credit_grants_reference' AND c.relkind = 'i'
		       AND n.nspname = ANY (current_schemas(true))`
	}
	var count int
	if err := s.queryRow(ctx, q).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func duplicateGrantReferences(ctx context.Context, q migrationQueryExecer) (_ []string, err error) {
	rows, err := q.QueryContext(ctx, `SELECT kind, reference FROM credit_grants
	  WHERE reference != '' GROUP BY kind, reference HAVING COUNT(*) > 1`)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []string
	for rows.Next() {
		var kind, reference string
		if err := rows.Scan(&kind, &reference); err != nil {
			return nil, err
		}
		out = append(out, kind+"/"+reference)
	}
	return out, rows.Err()
}

// CreditGrant is one row of credit_grants: credits the operator added.
type CreditGrant struct {
	ID          string
	Kind        string
	AmountMicro int64
	Reference   string
	// Reverses names the reference of the paid grant a reversal takes back,
	// and is empty on every other kind.
	Reverses  string
	CreatedBy string
	CreatedAt time.Time
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
	ChargedAt   time.Time
}

// CreditState is the ledger as an operator reads it: what was granted, what
// was spent, what is left, the price of a cloud runner second, and the burn
// over a recent window.
type CreditState struct {
	GrantedMicro int64
	// ReversedMicro is what reversals took back, reported as a positive
	// amount and already subtracted from the balance.
	ReversedMicro      int64
	ChargedMicro       int64
	BalanceMicro       int64
	RateMicroPerSecond int64
	GraceSeconds       int64
	MaxChargeSeconds   int64
	BurnWindow         time.Duration
	BurnMicro          int64
	ExhaustedAt        *time.Time
}

// ValidCreditGrantKind reports whether kind is one this ledger stores.
func ValidCreditGrantKind(kind string) bool {
	return kind == CreditGrantFree || kind == CreditGrantPaid || kind == CreditGrantReversal
}

// CreditGrantRequest is one movement of the grant side of the ledger: credits
// added, or, for [CreditGrantReversal], credits taken back.
//
// Reference is the payment id the grant came from, or an operator note. A
// non-empty one makes the grant idempotent: a second request naming the same
// kind and reference returns the row already written, which is what lets a
// payment webhook retry. An empty reference writes a new row every time.
//
// A reversal carries a negative AmountMicro, its own Reference (the refund id,
// so partial refunds of one payment each land), and Reverses naming the
// reference of the paid grant it takes back.
type CreditGrantRequest struct {
	Kind        string
	AmountMicro int64
	Reference   string
	Reverses    string
	CreatedBy   string
}

// CreditGrantResult is the row the ledger holds for a request together with
// whether this call wrote it. Created is false when a non-empty reference
// matched a grant already recorded, in which case Grant is that earlier row.
type CreditGrantResult struct {
	Grant   CreditGrant
	Created bool
}

func (req CreditGrantRequest) validate() error {
	if !ValidCreditGrantKind(req.Kind) {
		return fmt.Errorf("credits: unknown grant kind %q", req.Kind)
	}
	if req.Kind != CreditGrantReversal {
		if req.AmountMicro <= 0 {
			return errors.New("credits: grant amount must be positive")
		}
		if req.AmountMicro > MaxCreditGrantMicro {
			return fmt.Errorf("credits: a grant may not exceed %d micro-credits", MaxCreditGrantMicro)
		}
		if req.Reverses != "" {
			return fmt.Errorf("credits: only a %s grant reverses another grant", CreditGrantReversal)
		}
		return nil
	}
	if req.AmountMicro >= 0 {
		return errors.New("credits: a reversal amount must be negative")
	}
	if req.AmountMicro < -MaxCreditGrantMicro {
		return fmt.Errorf("credits: a reversal may not exceed %d micro-credits", MaxCreditGrantMicro)
	}
	if req.Reference == "" {
		return errors.New("credits: a reversal needs its own reference, the refund id")
	}
	if req.Reverses == "" {
		return fmt.Errorf("credits: a reversal must name the %s grant's reference it reverses",
			CreditGrantPaid)
	}
	return nil
}

// GrantCredits adds credits to the ledger and returns the row it holds for
// them. A grant that lifts the balance above zero clears the exhaustion stamp,
// so a node cancelled for an empty balance is the last one cancelled. A
// non-empty reference is idempotent: the row already written under that kind
// and reference is returned instead of a second one.
func (s *Store) GrantCredits(
	ctx context.Context, kind string, amountMicro int64, reference, createdBy string,
) (*CreditGrant, error) {
	res, err := s.RecordCreditGrant(ctx, CreditGrantRequest{
		Kind: kind, AmountMicro: amountMicro, Reference: reference, CreatedBy: createdBy,
	})
	if err != nil {
		return nil, err
	}
	grant := res.Grant
	return &grant, nil
}

// RecordCreditGrant writes req and reports whether it wrote a new row. It
// refuses a reversal whose Reverses names no paid grant, so a refund can only
// take back a payment the ledger recorded. A reversal may take the balance
// below zero; the claim path then refuses new metered work.
func (s *Store) RecordCreditGrant(
	ctx context.Context, req CreditGrantRequest,
) (_ CreditGrantResult, err error) {
	if err := req.validate(); err != nil {
		return CreditGrantResult{}, err
	}
	id, err := newCreditID("grant")
	if err != nil {
		return CreditGrantResult{}, err
	}
	now := time.Now().UTC()
	grant := CreditGrant{
		ID: id, Kind: req.Kind, AmountMicro: req.AmountMicro,
		Reference: req.Reference, Reverses: req.Reverses,
		CreatedBy: req.CreatedBy, CreatedAt: now,
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return CreditGrantResult{}, err
	}
	defer rollbackUnlessDone(tx, &err)
	// safety: the lock is what makes the reference check and the insert one
	// step, so two deliveries of the same payment cannot both find nothing.
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return CreditGrantResult{}, err
	}
	if req.Reference != "" {
		existing, found, err := creditGrantByReferenceTx(ctx, tx, req.Kind, req.Reference)
		if err != nil {
			return CreditGrantResult{}, err
		}
		if found {
			if err := sameGrantTerms(existing, req); err != nil {
				return CreditGrantResult{}, err
			}
			return CreditGrantResult{Grant: existing}, tx.Commit()
		}
	}
	if req.Kind == CreditGrantReversal {
		_, found, err := creditGrantByReferenceTx(ctx, tx, CreditGrantPaid, req.Reverses)
		if err != nil {
			return CreditGrantResult{}, err
		}
		if !found {
			return CreditGrantResult{}, fmt.Errorf(
				"credits: no %s grant carries the reference %q", CreditGrantPaid, req.Reverses)
		}
	}
	if _, err := tx.ExecContext(ctx, `
        INSERT INTO credit_grants (id, kind, amount_micro, reference, reverses, created_by, created_at)
        VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, req.Kind, req.AmountMicro, req.Reference, req.Reverses,
		req.CreatedBy, now.UnixNano()); err != nil {
		return CreditGrantResult{}, fmt.Errorf("credits: insert grant: %w", err)
	}
	balance, err := creditBalanceTx(ctx, tx)
	if err != nil {
		return CreditGrantResult{}, err
	}
	if balance > 0 {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM sparkwing_meta WHERE key = ?`, metaKeyCreditExhaustedAt); err != nil {
			return CreditGrantResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return CreditGrantResult{}, err
	}
	s.invalidateRunnerCaps()
	return CreditGrantResult{Grant: grant, Created: true}, nil
}

// safety: a webhook replay carries the terms it carried the first time, so
// different terms under one reference are a caller mistake rather than a
// retry, and answering with the stored row would swallow the difference.
func sameGrantTerms(stored CreditGrant, req CreditGrantRequest) error {
	if stored.AmountMicro != req.AmountMicro {
		return fmt.Errorf("%w: %q holds %d micro-credits, not %d",
			ErrCreditGrantConflict, req.Reference, stored.AmountMicro, req.AmountMicro)
	}
	if stored.Reverses != req.Reverses {
		return fmt.Errorf("%w: %q reverses %q, not %q",
			ErrCreditGrantConflict, req.Reference, stored.Reverses, req.Reverses)
	}
	return nil
}

func creditGrantByReferenceTx(
	ctx context.Context, tx *storeTx, kind, reference string,
) (CreditGrant, bool, error) {
	var g CreditGrant
	var created int64
	err := tx.QueryRowContext(ctx, `SELECT id, kind, amount_micro, reference, reverses, created_by, created_at
	  FROM credit_grants WHERE kind = ? AND reference = ?
	  ORDER BY created_at ASC, id ASC LIMIT 1`, kind, reference).
		Scan(&g.ID, &g.Kind, &g.AmountMicro, &g.Reference, &g.Reverses, &g.CreatedBy, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return CreditGrant{}, false, nil
	}
	if err != nil {
		return CreditGrant{}, false, err
	}
	g.CreatedAt = time.Unix(0, created).UTC()
	return g, true, nil
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

// safety: a reversal is a grant row with a negative amount, so a reader that
// wants the two apart asks for them apart; the balance is the same either way.
const creditGrantSplitSQL = `SELECT
        (SELECT SUM(amount_micro) FROM credit_grants WHERE kind != ?),
        (SELECT -SUM(amount_micro) FROM credit_grants WHERE kind = ?),
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
	var granted, reversed, charged sql.NullInt64
	if err := s.queryRow(ctx, creditGrantSplitSQL,
		CreditGrantReversal, CreditGrantReversal).Scan(&granted, &reversed, &charged); err != nil {
		return out, err
	}
	out.GrantedMicro = granted.Int64
	out.ReversedMicro = reversed.Int64
	out.ChargedMicro = charged.Int64
	out.BalanceMicro = granted.Int64 - reversed.Int64 - charged.Int64

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
	// ReversedMicro is what refunds took back out of the paid grants,
	// reported as a positive amount.
	ReversedMicro int64
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
                        COALESCE(SUM(CASE WHEN kind = ? THEN amount_micro ELSE 0 END), 0),
                        -COALESCE(SUM(CASE WHEN kind = ? THEN amount_micro ELSE 0 END), 0)
                   FROM credit_grants`,
		CreditGrantFree, CreditGrantPaid, CreditGrantReversal,
	).Scan(&out.GrantedFreeMicro, &out.GrantedPaidMicro, &out.ReversedMicro); err != nil {
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

	out.BalanceMicro = out.GrantedFreeMicro + out.GrantedPaidMicro - out.ReversedMicro - settled
	return out, tx.Commit()
}

// CreditRateMicroPerSecond returns the price of one cloud runner second in
// micro-credits. A controller that never set one reads the default.
func (s *Store) CreditRateMicroPerSecond(ctx context.Context) (int64, error) {
	return s.creditSetting(ctx, metaKeyCreditRateMicro, DefaultCreditRateMicro)
}

// SetCreditRateMicroPerSecond prices a cloud runner second, between one
// micro-credit and MaxCreditRateMicro. Charges already written keep the rate
// they were charged at.
func (s *Store) SetCreditRateMicroPerSecond(ctx context.Context, micro int64) error {
	if err := validCreditRate(micro); err != nil {
		return err
	}
	return s.setCreditSetting(ctx, metaKeyCreditRateMicro, micro)
}

// CreditGraceSeconds returns how long a node keeps running once it has
// consumed the reservation its claim paid for with the balance at zero.
func (s *Store) CreditGraceSeconds(ctx context.Context) (int64, error) {
	return s.creditSetting(ctx, metaKeyCreditGraceSecs, DefaultCreditGraceSeconds)
}

// SetCreditGraceSeconds sets the window between a node consuming the
// reservation its claim paid for on an empty balance and its cancellation.
// Zero cancels it at the first heartbeat past that reservation.
func (s *Store) SetCreditGraceSeconds(ctx context.Context, secs int64) error {
	if err := validCreditGrace(secs); err != nil {
		return err
	}
	return s.setCreditSetting(ctx, metaKeyCreditGraceSecs, secs)
}

// CreditMaxChargeSeconds returns the most seconds one charge may bill, which
// is what keeps a controller outage or a stalled heartbeat loop from billing
// the gap it left behind.
func (s *Store) CreditMaxChargeSeconds(ctx context.Context) (int64, error) {
	return s.creditSetting(ctx, metaKeyCreditMaxCharge, DefaultCreditMaxChargeSeconds)
}

// SetCreditMaxChargeSeconds caps the seconds one charge may bill, between
// MinCreditMaxChargeSeconds and MaxCreditMaxChargeSeconds.
func (s *Store) SetCreditMaxChargeSeconds(ctx context.Context, secs int64) error {
	if err := validCreditMaxCharge(secs); err != nil {
		return err
	}
	return s.setCreditSetting(ctx, metaKeyCreditMaxCharge, secs)
}

// CreditSettings are the three values the ledger prices work with.
type CreditSettings struct {
	// RateMicroPerSecond is the price of one cloud runner second.
	RateMicroPerSecond int64
	// GraceSeconds is how long a node that has consumed its claim reservation
	// on an empty balance keeps running before the controller cancels it.
	GraceSeconds int64
	// MaxChargeSeconds is the most seconds any one charge may bill.
	MaxChargeSeconds int64
}

// CreditSettingsUpdate names the settings to change. A nil field leaves that
// setting as it stands, so a caller changing one value sends one field and a
// field added later joins without disturbing these three.
type CreditSettingsUpdate struct {
	RateMicroPerSecond *int64
	GraceSeconds       *int64
	MaxChargeSeconds   *int64
}

// CreditSettings reads all three settings together. A controller that set none
// of them reads the defaults.
func (s *Store) CreditSettings(ctx context.Context) (_ CreditSettings, err error) {
	var out CreditSettings
	tx, err := s.beginSnapshotReadTx(ctx)
	if err != nil {
		return out, err
	}
	defer rollbackUnlessDone(tx, &err)
	out, err = creditSettingsTx(ctx, tx)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

// SetCreditSettings validates every value the update names and writes them in
// one transaction, so a refused field leaves the ledger exactly as it was. It
// answers the settings in force after the write. An update naming nothing is
// refused, as is a rate outside one micro-credit to MaxCreditRateMicro, a
// negative grace period, and a charge cap outside MinCreditMaxChargeSeconds to
// MaxCreditMaxChargeSeconds. Every refusal is an ErrInvalidCreditSetting.
func (s *Store) SetCreditSettings(ctx context.Context, up CreditSettingsUpdate) (_ CreditSettings, err error) {
	var out CreditSettings
	if err := up.validate(); err != nil {
		return out, err
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return out, err
	}
	defer rollbackUnlessDone(tx, &err)
	for key, value := range up.byKey() {
		if _, err := tx.ExecContext(ctx, upsertCreditSettingSQL,
			key, strconv.FormatInt(value, 10), time.Now().UnixNano()); err != nil {
			return out, err
		}
	}
	out, err = creditSettingsTx(ctx, tx)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

// ErrInvalidCreditSetting is the class of every refusal SetCreditSettings and
// the individual setters return, so a caller answers a bad value differently
// from a database failure.
var ErrInvalidCreditSetting = errors.New("credits: invalid setting")

func (u CreditSettingsUpdate) byKey() map[string]int64 {
	out := make(map[string]int64, 3)
	if u.RateMicroPerSecond != nil {
		out[metaKeyCreditRateMicro] = *u.RateMicroPerSecond
	}
	if u.GraceSeconds != nil {
		out[metaKeyCreditGraceSecs] = *u.GraceSeconds
	}
	if u.MaxChargeSeconds != nil {
		out[metaKeyCreditMaxCharge] = *u.MaxChargeSeconds
	}
	return out
}

// safety: every value is judged before any of them is written, so a body that
// names one good setting and one bad one moves neither.
func (u CreditSettingsUpdate) validate() error {
	if u.RateMicroPerSecond == nil && u.GraceSeconds == nil && u.MaxChargeSeconds == nil {
		return fmt.Errorf("%w: name at least one of the rate, the grace period, or the charge cap",
			ErrInvalidCreditSetting)
	}
	if u.RateMicroPerSecond != nil {
		if err := validCreditRate(*u.RateMicroPerSecond); err != nil {
			return err
		}
	}
	if u.GraceSeconds != nil {
		if err := validCreditGrace(*u.GraceSeconds); err != nil {
			return err
		}
	}
	if u.MaxChargeSeconds != nil {
		if err := validCreditMaxCharge(*u.MaxChargeSeconds); err != nil {
			return err
		}
	}
	return nil
}

func validCreditRate(micro int64) error {
	if micro <= 0 || micro > MaxCreditRateMicro {
		return fmt.Errorf("%w: the rate must be between 1 and %d micro-credits per second, got %d",
			ErrInvalidCreditSetting, int64(MaxCreditRateMicro), micro)
	}
	return nil
}

func validCreditGrace(secs int64) error {
	if secs < 0 {
		return fmt.Errorf("%w: the grace period must not be negative, got %d",
			ErrInvalidCreditSetting, secs)
	}
	return nil
}

func validCreditMaxCharge(secs int64) error {
	if secs < MinCreditMaxChargeSeconds || secs > MaxCreditMaxChargeSeconds {
		return fmt.Errorf("%w: the charge cap must be between %d and %d seconds, got %d",
			ErrInvalidCreditSetting, MinCreditMaxChargeSeconds, int64(MaxCreditMaxChargeSeconds), secs)
	}
	return nil
}

func creditSettingsTx(ctx context.Context, tx *storeTx) (CreditSettings, error) {
	var out CreditSettings
	rate, err := creditSettingTx(ctx, tx, metaKeyCreditRateMicro, DefaultCreditRateMicro)
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
	out.RateMicroPerSecond = rate
	out.GraceSeconds = grace
	out.MaxChargeSeconds = maxCharge
	return out, nil
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
	_, err := s.exec(ctx, upsertCreditSettingSQL, key, strconv.FormatInt(v, 10), time.Now().UnixNano())
	return err
}

const upsertCreditSettingSQL = `
        INSERT INTO sparkwing_meta (key, value, updated_at) VALUES (?, ?, ?)
        ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`

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
	rows, err := s.query(ctx, `SELECT id, kind, amount_micro, reference, reverses, created_by, created_at
	  FROM credit_grants ORDER BY created_at DESC, id DESC LIMIT ?`, creditLimit(limit))
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []CreditGrant
	for rows.Next() {
		var g CreditGrant
		var created int64
		if err := rows.Scan(&g.ID, &g.Kind, &g.AmountMicro, &g.Reference, &g.Reverses,
			&g.CreatedBy, &created); err != nil {
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
	rows, err := s.query(ctx, `SELECT id, run_id, node_id, token_prefix, kind, seconds, amount_micro, charged_at
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
			&c.Seconds, &c.AmountMicro, &charged); err != nil {
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

// CreditClaimFloorMicro is what a metered claim reserves: one minute of
// cloud runner time at the current rate.
func (s *Store) CreditClaimFloorMicro(ctx context.Context) (int64, error) {
	rate, err := s.CreditRateMicroPerSecond(ctx)
	if err != nil {
		return 0, err
	}
	return rate * CreditClaimFloorSeconds, nil
}

// safety: reserving inside the claim's own transaction is what keeps concurrent
// runners from each reading the same balance and claiming against it.
func (s *Store) reserveNodeCreditsTx(
	ctx context.Context, tx *storeTx, claimant ClaimIdentity, runID, nodeID string, now time.Time,
) error {
	metered, err := tokenMeteredTx(ctx, tx, claimant.TokenPrefix)
	if err != nil || !metered {
		return err
	}
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return err
	}
	rate, err := creditSettingTx(ctx, tx, metaKeyCreditRateMicro, DefaultCreditRateMicro)
	if err != nil {
		return err
	}
	required := rate * CreditClaimFloorSeconds
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
		if err := s.enforceClaimComputeLimitsTx(ctx, tx, limits, claimant, runID, now); err != nil {
			return err
		}
	}
	id, err := newCreditID("charge")
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, insertCreditChargeSQL,
		id, runID, nodeID, claimant.TokenPrefix, CreditChargeReservation,
		int64(CreditClaimFloorSeconds), required, now.UnixNano()); err != nil {
		return fmt.Errorf("credits: reserve: %w", err)
	}
	through := now.Add(CreditClaimFloorSeconds * time.Second).UnixNano()
	_, err = tx.ExecContext(ctx,
		`UPDATE nodes SET credit_charged_through = ? WHERE run_id = ? AND node_id = ?`,
		through, runID, nodeID)
	return err
}

const insertCreditChargeSQL = `
        INSERT INTO credit_charges (id, run_id, node_id, token_prefix, kind, seconds, amount_micro, charged_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

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
	rate, err := creditSettingTx(ctx, tx, metaKeyCreditRateMicro, DefaultCreditRateMicro)
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

	var anchor int64
	err = tx.QueryRowContext(ctx,
		`SELECT credit_charged_through FROM nodes WHERE run_id = ? AND node_id = ?`+tx.forUpdate(),
		runID, nodeID).Scan(&anchor)
	if errors.Is(err, sql.ErrNoRows) {
		return out, notFound("node", runID+"/"+nodeID)
	}
	if err != nil {
		return out, err
	}

	nowNS := now.UnixNano()
	charge, forgiven, through, err := settleChargeWindow(
		ctx, tx, chargeWindow{
			RunID: runID, NodeID: nodeID, TokenPrefix: tokenPrefix,
			Anchor: anchor, NowNS: nowNS, Rate: rate, MaxCharge: maxCharge, Final: final,
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
	exhaustedFor, cancel, err := settleCreditExhaustionTx(ctx, tx, creditExhaustion{
		Balance: balance, NowNS: nowNS, Grace: grace,
		RunID: runID, NodeID: nodeID, Anchor: anchor,
	})
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
	RunID, NodeID, TokenPrefix string
	Anchor, NowNS              int64
	Rate, MaxCharge            int64
	Final                      bool
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
		charge, err := insertCreditChargeTx(ctx, tx, w, CreditChargeRefund, -refund, -w.Rate*refund)
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

func insertCreditChargeTx(
	ctx context.Context, tx *storeTx, w chargeWindow, kind string, seconds, amount int64,
) (*CreditCharge, error) {
	id, err := newCreditID("charge")
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, insertCreditChargeSQL,
		id, w.RunID, w.NodeID, w.TokenPrefix, kind, seconds, amount, w.NowNS); err != nil {
		return nil, fmt.Errorf("credits: insert charge: %w", err)
	}
	return &CreditCharge{
		ID: id, RunID: w.RunID, NodeID: w.NodeID, TokenPrefix: w.TokenPrefix,
		Kind: kind, Seconds: seconds, AmountMicro: amount,
		ChargedAt: time.Unix(0, w.NowNS).UTC(),
	}, nil
}

// safety: Anchor is the instant the node is paid through, read before this
// charge advanced it. Zero means no charge has anchored the node yet, which is
// a node still inside the claim that will set one.
type creditExhaustion struct {
	Balance, NowNS, Grace int64
	RunID, NodeID         string
	Anchor                int64
}

func settleCreditExhaustionTx(
	ctx context.Context, tx *storeTx, e creditExhaustion,
) (time.Duration, bool, error) {
	if e.Balance > 0 {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM sparkwing_meta WHERE key = ?`, metaKeyCreditExhaustedAt); err != nil {
			return 0, false, err
		}
		// safety: credit bought the node fresh runway, so the clock it was
		// running down is gone rather than paused. The predicate keeps a
		// healthy balance from writing the row on every heartbeat.
		if err := clearCreditExhaustionAnchorTx(ctx, tx, e); err != nil {
			return 0, false, err
		}
		return 0, false, nil
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO sparkwing_meta (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT (key) DO NOTHING`,
		metaKeyCreditExhaustedAt, strconv.FormatInt(e.NowNS, 10), e.NowNS); err != nil {
		return 0, false, err
	}
	var raw string
	if err := tx.QueryRowContext(ctx,
		`SELECT value FROM sparkwing_meta WHERE key = ?`, metaKeyCreditExhaustedAt).Scan(&raw); err != nil {
		return 0, false, err
	}
	stamped := parseCreditSetting(raw, e.NowNS)
	exhaustedFor := time.Duration(e.NowNS - stamped)
	if exhaustedFor < 0 {
		exhaustedFor = 0
	}
	// safety: a node with no anchor has never been charged, so it is still
	// inside the claim that will set one and cannot have outrun its runway.
	if e.Anchor == 0 {
		return exhaustedFor, false, nil
	}
	from, err := creditExhaustionAnchorTx(ctx, tx, e)
	if err != nil {
		return exhaustedFor, false, err
	}
	deadline := from + e.Grace*int64(time.Second)
	return exhaustedFor, e.NowNS > e.Anchor && e.NowNS > deadline, nil
}

// safety: the grace clock is per node and starts where that node's paid runway
// ended, which is the instant it was charged through when the balance first
// read empty. Recording it once is what keeps the deadline still while later
// heartbeats push the charge anchor forward.
func creditExhaustionAnchorTx(ctx context.Context, tx *storeTx, e creditExhaustion) (int64, error) {
	var stamped int64
	if err := tx.QueryRowContext(ctx,
		`SELECT credit_exhausted_anchor FROM nodes WHERE run_id = ? AND node_id = ?`,
		e.RunID, e.NodeID).Scan(&stamped); err != nil {
		return 0, err
	}
	if stamped != 0 {
		return stamped, nil
	}
	return e.Anchor, stampCreditExhaustionAnchorTx(ctx, tx, e, e.Anchor)
}

func stampCreditExhaustionAnchorTx(
	ctx context.Context, tx *storeTx, e creditExhaustion, at int64,
) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE nodes SET credit_exhausted_anchor = ? WHERE run_id = ? AND node_id = ?`,
		at, e.RunID, e.NodeID)
	return err
}

func clearCreditExhaustionAnchorTx(ctx context.Context, tx *storeTx, e creditExhaustion) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE nodes SET credit_exhausted_anchor = 0
		  WHERE run_id = ? AND node_id = ? AND credit_exhausted_anchor != 0`,
		e.RunID, e.NodeID)
	return err
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
