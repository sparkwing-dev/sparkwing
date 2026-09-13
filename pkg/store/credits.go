package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
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

	// CreditClaimFloorSeconds is the runway a claim must be able to pay for.
	// A node handed to a metered runner is guaranteed a minute of execution
	// rather than a cancellation on its first heartbeat.
	CreditClaimFloorSeconds = 60
)

// Credit grant kinds.
const (
	CreditGrantFree = "free"
	CreditGrantPaid = "paid"
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
// no balance left to pay for the work it asked for.
var ErrInsufficientCredits = errors.New("insufficient credits")

const (
	metaKeyCreditRateMicro   = "credit_rate_micro_per_second"
	metaKeyCreditGraceSecs   = "credit_grace_seconds"
	metaKeyCreditExhaustedAt = "credit_exhausted_at"
)

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
    seconds      INTEGER NOT NULL,
    amount_micro INTEGER NOT NULL,
    charged_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_credit_charges_charged ON credit_charges(charged_at);
CREATE INDEX IF NOT EXISTS idx_credit_charges_node ON credit_charges(run_id, node_id);`

var creditsTablesPostgres = func() string {
	r := strings.NewReplacer("INTEGER", "BIGINT")
	return r.Replace(creditGrantsTableSQLite) + "\n" + r.Replace(creditChargesTableSQLite)
}()

// safety: metering trusts this operator-set marker alone, never a runner's
// self-asserted labels.
var tokensMeteredCols = map[string]string{
	"metered": "INTEGER NOT NULL DEFAULT 0",
}

// safety: the anchor makes each heartbeat charge exactly the seconds since the
// previous charge for that node.
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
	_, err := tx.ExecContext(ctx, creditChargesTableSQLite)
	return err
}

func applyCreditsMigrationPostgres(ctx context.Context, tx *storeTx) error {
	if err := addColumnsTx(ctx, tx, "tokens", tokensMeteredCols); err != nil {
		return err
	}
	if err := addColumnsTx(ctx, tx, "nodes", nodesCreditCols); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, creditsTablesPostgres)
	return err
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

// CreditCharge is one row of credit_charges: the cost of one interval of
// one node's execution on a metered runner.
type CreditCharge struct {
	ID          string
	RunID       string
	NodeID      string
	TokenPrefix string
	Seconds     int64
	AmountMicro int64
	ChargedAt   time.Time
}

// CreditState is the ledger as an operator reads it: what was granted, what
// was spent, what is left, the price of a cloud runner second, and the burn
// over a recent window.
type CreditState struct {
	GrantedMicro       int64
	ChargedMicro       int64
	BalanceMicro       int64
	RateMicroPerSecond int64
	GraceSeconds       int64
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
func (s *Store) GrantCredits(ctx context.Context, kind string, amountMicro int64, reference, createdBy string) (*CreditGrant, error) {
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
	defer func() { _ = tx.Rollback() }()
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
	if err := s.queryRow(ctx,
		`SELECT (SELECT SUM(amount_micro) FROM credit_grants),
		        (SELECT SUM(amount_micro) FROM credit_charges)`).Scan(&granted, &charged); err != nil {
		return 0, err
	}
	return granted.Int64 - charged.Int64, nil
}

func creditBalanceTx(ctx context.Context, tx *storeTx) (int64, error) {
	var granted, charged sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT (SELECT SUM(amount_micro) FROM credit_grants),
		        (SELECT SUM(amount_micro) FROM credit_charges)`).Scan(&granted, &charged); err != nil {
		return 0, err
	}
	return granted.Int64 - charged.Int64, nil
}

// CreditState reports the ledger together with the rate, the grace period,
// and the credits burned over window.
func (s *Store) CreditState(ctx context.Context, window time.Duration) (CreditState, error) {
	out := CreditState{BurnWindow: window}
	var granted, charged sql.NullInt64
	if err := s.queryRow(ctx,
		`SELECT (SELECT SUM(amount_micro) FROM credit_grants),
		        (SELECT SUM(amount_micro) FROM credit_charges)`).Scan(&granted, &charged); err != nil {
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
	grace, err := s.CreditGraceSeconds(ctx)
	if err != nil {
		return out, err
	}
	out.GraceSeconds = grace
	exhausted, err := s.creditExhaustedAt(ctx)
	if err != nil {
		return out, err
	}
	out.ExhaustedAt = exhausted
	return out, nil
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

func (s *Store) creditSetting(ctx context.Context, key string, fallback int64) (int64, error) {
	var raw string
	err := s.queryRow(ctx, `SELECT value FROM sparkwing_meta WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return fallback, nil
	}
	if err != nil {
		return 0, err
	}
	v, perr := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if perr != nil {
		return fallback, nil
	}
	return v, nil
}

func (s *Store) setCreditSetting(ctx context.Context, key string, v int64) error {
	_, err := s.exec(ctx,
		`INSERT INTO sparkwing_meta (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, strconv.FormatInt(v, 10), time.Now().UnixNano())
	return err
}

func (s *Store) creditExhaustedAt(ctx context.Context) (*time.Time, error) {
	var raw string
	err := s.queryRow(ctx, `SELECT value FROM sparkwing_meta WHERE key = ?`, metaKeyCreditExhaustedAt).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ns, perr := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if perr != nil {
		return nil, nil
	}
	at := time.Unix(0, ns).UTC()
	return &at, nil
}

// ListCreditGrants returns grants newest first, at most limit rows.
func (s *Store) ListCreditGrants(ctx context.Context, limit int) ([]CreditGrant, error) {
	rows, err := s.query(ctx, `SELECT id, kind, amount_micro, reference, created_by, created_at
	  FROM credit_grants ORDER BY created_at DESC, id DESC LIMIT ?`, creditLimit(limit))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
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

// ListCreditCharges returns charges newest first, at most limit rows.
func (s *Store) ListCreditCharges(ctx context.Context, limit int) ([]CreditCharge, error) {
	rows, err := s.query(ctx, `SELECT id, run_id, node_id, token_prefix, seconds, amount_micro, charged_at
	  FROM credit_charges ORDER BY charged_at DESC, id DESC LIMIT ?`, creditLimit(limit))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []CreditCharge
	for rows.Next() {
		var c CreditCharge
		var charged int64
		if err := rows.Scan(&c.ID, &c.RunID, &c.NodeID, &c.TokenPrefix, &c.Seconds, &c.AmountMicro, &charged); err != nil {
			return nil, err
		}
		c.ChargedAt = time.Unix(0, charged).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

func creditLimit(limit int) int {
	if limit <= 0 || limit > 1000 {
		return 200
	}
	return limit
}

// CreditClaimFloorMicro is what a metered claim must be able to pay for:
// one minute of cloud runner time at the current rate.
func (s *Store) CreditClaimFloorMicro(ctx context.Context) (int64, error) {
	rate, err := s.CreditRateMicroPerSecond(ctx)
	if err != nil {
		return 0, err
	}
	return rate * CreditClaimFloorSeconds, nil
}

// StartNodeMetering anchors a metered node's charge window at the moment it
// was claimed, so the first heartbeat charges from the claim rather than
// from itself. It is a no-op once the node carries an anchor.
func (s *Store) StartNodeMetering(ctx context.Context, runID, nodeID string, at time.Time) error {
	_, err := s.exec(ctx,
		`UPDATE nodes SET credit_charged_through = ?
		  WHERE run_id = ? AND node_id = ? AND credit_charged_through = 0`,
		at.UnixNano(), runID, nodeID)
	return err
}

// CreditChargeResult is what one metered heartbeat did to the ledger.
type CreditChargeResult struct {
	// Charge is the row written, or nil when less than a second elapsed
	// since the previous charge for this node.
	Charge *CreditCharge
	// BalanceMicro is the balance after the charge.
	BalanceMicro int64
	// ExhaustedFor is how long the balance has been empty, zero while it
	// holds credit.
	ExhaustedFor time.Duration
	// Cancel reports that the balance has been empty for longer than the
	// grace period, so the caller must stop the node.
	Cancel bool
}

// ChargeNodeCredits bills the seconds this node has run since its previous
// charge and reports whether the balance can still pay for it. Charging is
// idempotent within a second: a second call in the same second advances
// nothing and writes no row.
func (s *Store) ChargeNodeCredits(ctx context.Context, runID, nodeID, tokenPrefix string, now time.Time) (CreditChargeResult, error) {
	var out CreditChargeResult
	rate, err := s.CreditRateMicroPerSecond(ctx)
	if err != nil {
		return out, err
	}
	grace, err := s.CreditGraceSeconds(ctx)
	if err != nil {
		return out, err
	}
	id, err := newCreditID("charge")
	if err != nil {
		return out, err
	}

	tx, err := s.beginTx(ctx)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()

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
	switch {
	case anchor == 0 || anchor > nowNS:
		if _, err := tx.ExecContext(ctx,
			`UPDATE nodes SET credit_charged_through = ? WHERE run_id = ? AND node_id = ?`,
			nowNS, runID, nodeID); err != nil {
			return out, err
		}
	default:
		seconds := (nowNS - anchor) / int64(time.Second)
		if seconds > 0 {
			amount := rate * seconds
			through := anchor + seconds*int64(time.Second)
			if _, err := tx.ExecContext(ctx, `
                INSERT INTO credit_charges (id, run_id, node_id, token_prefix, seconds, amount_micro, charged_at)
                VALUES (?, ?, ?, ?, ?, ?, ?)`,
				id, runID, nodeID, tokenPrefix, seconds, amount, nowNS); err != nil {
				return out, fmt.Errorf("credits: insert charge: %w", err)
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE nodes SET credit_charged_through = ? WHERE run_id = ? AND node_id = ?`,
				through, runID, nodeID); err != nil {
				return out, err
			}
			out.Charge = &CreditCharge{
				ID: id, RunID: runID, NodeID: nodeID, TokenPrefix: tokenPrefix,
				Seconds: seconds, AmountMicro: amount, ChargedAt: now.UTC(),
			}
		}
	}

	balance, err := creditBalanceTx(ctx, tx)
	if err != nil {
		return out, err
	}
	out.BalanceMicro = balance

	if balance > 0 {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM sparkwing_meta WHERE key = ?`, metaKeyCreditExhaustedAt); err != nil {
			return out, err
		}
		if err := tx.Commit(); err != nil {
			return out, err
		}
		return out, nil
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO sparkwing_meta (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT (key) DO NOTHING`,
		metaKeyCreditExhaustedAt, strconv.FormatInt(nowNS, 10), nowNS); err != nil {
		return out, err
	}
	var stampedRaw string
	if err := tx.QueryRowContext(ctx,
		`SELECT value FROM sparkwing_meta WHERE key = ?`, metaKeyCreditExhaustedAt).Scan(&stampedRaw); err != nil {
		return out, err
	}
	stamped, perr := strconv.ParseInt(strings.TrimSpace(stampedRaw), 10, 64)
	if perr != nil {
		stamped = nowNS
	}
	if err := tx.Commit(); err != nil {
		return out, err
	}
	out.ExhaustedFor = time.Duration(nowNS - stamped)
	if out.ExhaustedFor < 0 {
		out.ExhaustedFor = 0
	}
	out.Cancel = out.ExhaustedFor >= time.Duration(grace)*time.Second
	return out, nil
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
	// safety: an ambiguous prefix names no single holder, so metering it would
	// charge for work a stranger's token did.
	if n > 1 {
		return fmt.Errorf("tokens: prefix %q matched %d rows, aborting", prefix, n)
	}
	return nil
}

// AppendEventOnce writes an event unless the run or node already carries one
// of that kind, which keeps a condition a poller re-observes every half
// second to one row. It reports whether it wrote.
func (s *Store) AppendEventOnce(ctx context.Context, runID, nodeID, kind string, payload []byte) (bool, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
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
