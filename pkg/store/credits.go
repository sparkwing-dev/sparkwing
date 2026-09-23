package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Credit ledger arithmetic. One credit is one second of one vCPU, and a
// dollar buys 20,000 of them, which prices compute at 0.18 dollars a
// vCPU-hour. Grants and charges are stored in micro-credits, 5,000 to the
// credit, so a dollar is 100,000,000 micro-credits and a cent is 1,000,000.
//
// A class costs its core count in credits a second: the default four-core
// rate is 4 credits a second, 240 a minute, 0.72 dollars an hour. Ten dollars
// is 200,000 credits, which buys 50,000 four-core seconds, just under
// fourteen hours.
const (
	CreditsPerDollar       = 20_000
	MicroCreditsPerCredit  = 5_000
	DefaultCreditRateMicro = 4 * MicroCreditsPerCredit

	// MicroCreditsPerCent is what one cent of a payment buys, which a
	// checkout converts by. It is the figure that must not move when the
	// credit does, because every stored amount is priced by it.
	MicroCreditsPerCent = MicroCreditsPerCredit * CreditsPerDollar / 100

	// DefaultCreditGraceSeconds is how long a node keeps running after the
	// balance reaches zero before the controller cancels it.
	DefaultCreditGraceSeconds = 60

	// MinBillableSeconds is the least one metered node or trigger step pays
	// for, on every class. A cold start takes about half a minute of
	// provisioning nobody is billed for, and a run earns what it costs to
	// serve once it bills at least half of that, so the minimum is 20 seconds
	// with margin over the measured 31-second start.
	//
	// A claim reserves this many seconds at its class's rate inside the claim
	// transaction and refuses a balance that cannot cover them. Once billing
	// starts the reservation is consumed rather than refunded, so a node that
	// finishes sooner pays the minimum; only a claim that never started, or a
	// setup the platform failed, gets it back. A claimed node is therefore
	// guaranteed this long, plus the grace period, before an empty balance
	// cancels it.
	//
	// safety: the reservation also bounds how far concurrent claims can drive
	// one balance below zero, and a smaller one admits more of them at once;
	// the per-principal runner caps bound that depth, not this constant.
	MinBillableSeconds = 20

	// MaxCreditGrantMicro caps the size of one grant at ten billion dollars.
	// A ledger sums grants in SQL, so an amount near the integer limit turns a
	// later balance read into an overflow rather than a number, and no real
	// payment reaches this ceiling.
	MaxCreditGrantMicro = 1_000_000_000_000_000

	// DefaultCreditMaxChargeSeconds caps the seconds one charge may bill.
	// Heartbeats arrive every three seconds by default, so the cap engages
	// only when the controller was unreachable or the heartbeat loop stalled,
	// and the customer is not billed for the gap.
	DefaultCreditMaxChargeSeconds = 30

	// MaxCreditRateMicro is the highest price an operator may put on a cloud
	// runner second: ten thousand dollars. The ceiling is what keeps the
	// arithmetic the ledger does with the rate inside int64: the largest
	// reservation is the rate times MinBillableSeconds
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
	// pool runner renew theirs, which is the longest cadence any metered node
	// renews on.
	DispatchedHeartbeatInterval = 5 * time.Second
)

// MinCreditMaxChargeSeconds is the lowest charge cap an operator may set: one
// second past the longest heartbeat cadence. A cap at the cadence itself
// truncates every tick that arrives a little late and forgives the remainder,
// so the ledger would undercharge ordinary work rather than only a stall.
const MinCreditMaxChargeSeconds = int64(DispatchedHeartbeatInterval/time.Second) + 1

// Credit grant kinds. A reversal carries a negative amount and names the
// paid grant it takes back, which is how a refunded card payment leaves the
// ledger.
const (
	CreditGrantFree     = "free"
	CreditGrantPaid     = "paid"
	CreditGrantReversal = "reversal"
)

// Credit charge kinds. A reservation is taken at claim time, usage rows bill
// the intervals a node actually ran, a refund returns the unused tail of a
// reservation when the node finishes early, and a storage row bills the bytes
// a team kept for the interval it kept them.
const (
	CreditChargeReservation = "reservation"
	CreditChargeUsage       = "usage"
	CreditChargeRefund      = "refund"
	CreditChargeStorage     = "storage"
)

// Run event kinds the credit ledger writes.
const (
	// EventKindCreditsBlocked records a claim refused for an empty balance,
	// so a run waiting on a metered runner says why.
	EventKindCreditsBlocked = "credits_blocked"
	// EventKindCreditsExhausted records a node cancelled after the balance
	// reached zero and the grace period elapsed.
	EventKindCreditsExhausted = "credits_exhausted"
	// EventKindCreditsUnpriced records a node failed because its cpu request
	// is above the largest class the rate table prices.
	EventKindCreditsUnpriced = "credits_unpriced_class"
)

// ErrInsufficientCredits is returned when a metered runner's controller has
// no balance left to pay for the work it asked for. The claim path returns
// an [InsufficientCreditsError], which wraps it and names the shortfall.
var ErrInsufficientCredits = errors.New("insufficient credits")

// ErrReversalExceedsPayment is returned when a reversal would take back more
// than the payment it names paid, counting the reversals already written.
var ErrReversalExceedsPayment = errors.New("credits: the reversal exceeds what the payment paid")

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
	// RunID and NodeID name the node the claim was refused for, which is in
	// the claimant's own team; a record of the refusal belongs on that node
	// and on no other team's.
	RunID  string
	NodeID string
}

func (e *InsufficientCreditsError) Error() string {
	return fmt.Sprintf("insufficient credits: balance %s, need %s",
		FormatCredits(e.BalanceMicro), FormatCredits(e.RequiredMicro))
}

// Unwrap reports [ErrInsufficientCredits], so a caller matches the condition
// with errors.Is without knowing this type.
func (e *InsufficientCreditsError) Unwrap() error { return ErrInsufficientCredits }

// safety: each of these is one price or one policy the operator sets for the
// whole controller, so they stay in the deployment's bag; a per-team rate is a
// discount the product does not sell, and the charge cap is judged against the
// heartbeat cadence this binary ships rather than against any customer.
const (
	metaKeyCreditRateMicro = "credit_rate_micro_per_second"
	metaKeyCreditGraceSecs = "credit_grace_seconds"
	metaKeyCreditMaxCharge = "credit_max_charge_seconds"
	metaKeyWarmCPUClass    = "warm_cpu_class_cores"

	// safety: the marker moved to the team row, and this key is kept only
	// because the v50 migration reads it and deletes it.
	metaKeyCreditExhaustedAt = "credit_exhausted_at"
)

// safety: when a balance first read empty is one team's state and the registry
// holds exactly one row per team, so it lives there. It cannot stay in
// sparkwing_meta, because a team column on that bag would make the session
// CSRF key per team and break local sessions. Zero never ran out.
var teamsCreditExhaustedCols = map[string]string{
	"credit_exhausted_at": "INTEGER NOT NULL DEFAULT 0",
}

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
    -- principal: the team whose storage or runner work this row billed;
    -- empty on legacy and unowned work.
    principal    TEXT NOT NULL DEFAULT '',
    -- storage_bytes: retained bytes above the free allowance a storage row
    -- billed; 0 on every other kind.
    storage_bytes INTEGER NOT NULL DEFAULT 0,
    -- cpu_class: whole cores of the class billed; 0 for a row written before
    -- the rate table, which was billed at credit_rate_micro_per_second.
    cpu_class    INTEGER NOT NULL DEFAULT 0,
    rate_micro_per_second INTEGER NOT NULL DEFAULT 0,
    charged_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_credit_charges_charged ON credit_charges(charged_at);
CREATE INDEX IF NOT EXISTS idx_credit_charges_node ON credit_charges(run_id, node_id);`

// safety: a payment webhook redelivers until it sees a 2xx, so the reference a
// grant carries is the key that keeps the retry from granting twice. An empty
// reference is an operator note rather than a payment, so it repeats freely.
// v52 replaces this key with [creditGrantTeamReferenceIndex].
const creditGrantReferenceIndex = `CREATE UNIQUE INDEX IF NOT EXISTS idx_credit_grants_reference
    ON credit_grants(kind, reference) WHERE reference != ''`

// safety: the reference is unique within a team, because a reference an
// operator chose, such as a welcome grant, names a different grant in every
// team; the grant path keeps a payment id unique across teams itself.
const creditGrantTeamReferenceIndex = `CREATE UNIQUE INDEX IF NOT EXISTS idx_credit_grants_team_reference
    ON credit_grants(team, kind, reference) WHERE reference != ''`

// applyTeamGrantReferenceMigration moves the grant reference key from
// (kind, reference) to (team, kind, reference). It is a step of v52.
func applyTeamGrantReferenceMigration(ctx context.Context, tx *storeTx) error {
	if _, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS idx_credit_grants_reference`); err != nil {
		return err
	}
	dupes, err := duplicateTeamGrantReferences(ctx, tx)
	if err != nil {
		return err
	}
	if len(dupes) > 0 {
		slog.Warn("credits: grants repeat a reference within a team, so the database does not enforce the grant key; "+
			"delete the duplicate rows and recreate the index to enforce it",
			"references", strings.Join(dupes, ", "))
		return nil
	}
	_, err = tx.ExecContext(ctx, creditGrantTeamReferenceIndex)
	return err
}

// creditUnitScale is how many of today's credits one credit bought before a
// credit became a vCPU-second, when it was a hundredth of a dollar. Micro-credit
// amounts kept their dollar value across the change, so only a setting written
// in whole credits needs it.
const creditUnitScale = 1_000_000 / MicroCreditsPerCredit

// applyCreditUnitMigration restates runner_scale_step_credits, the one setting
// written in whole credits, in the vCPU-second credit, so a step an operator set
// keeps its dollar value. It is a step of v56.
func applyCreditUnitMigration(ctx context.Context, tx *storeTx) error {
	key := computeLimitKey(ComputeLimitRunnerScaleStepCredits)
	var raw string
	err := tx.QueryRowContext(ctx, selectCreditSettingSQL, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	// safety: a value the guard reader cannot parse reads as unset there, so
	// it is left for the operator rather than guessed at.
	step, parsed := wholeCreditSetting(raw)
	if !parsed || step <= 0 {
		return nil
	}
	return setCreditSettingTx(ctx, tx, key,
		formatCreditSetting(min(step*creditUnitScale, RunnerScaleMaxStepCredits)))
}

func wholeCreditSetting(raw string) (int64, bool) {
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return v, err == nil
}

func duplicateTeamGrantReferences(ctx context.Context, q migrationQueryExecer) (_ []string, err error) {
	rows, err := q.QueryContext(ctx, `SELECT team, kind, reference FROM credit_grants
	  WHERE reference != '' GROUP BY team, kind, reference HAVING COUNT(*) > 1`)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []string
	for rows.Next() {
		var team, kind, reference string
		if err := rows.Scan(&team, &kind, &reference); err != nil {
			return nil, err
		}
		out = append(out, team+"/"+kind+"/"+reference)
	}
	return out, rows.Err()
}

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

// safety: a storage row bills a team's retained bytes rather than a node's
// seconds, so the team and the bytes have columns of their own; every row a
// runner charge wrote carries the empty defaults.
var creditChargeStorageCols = map[string]string{
	"principal":     "TEXT NOT NULL DEFAULT ''",
	"storage_bytes": "INTEGER NOT NULL DEFAULT 0",
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

// safety: zero means the machine that runs the node has not started on it, so
// a claim a dispatcher took before its pod exists bills nothing until the pod
// renews the claim or starts execution, and a setup that never began is
// refunded whole.
var nodesCreditBillingCols = map[string]string{
	"credit_billing_from": "INTEGER NOT NULL DEFAULT 0",
}

// platformSetupFailures are the failure reasons that mean the platform, not
// the customer's pipeline, stopped a node before its execution started: the
// runner vanished or lost its lease, no machine of the class came free, or
// the log service refused or dropped the node's writes. A node that fails
// this way before execution gets back everything its claim billed; any other
// failure before execution, such as a compile error, keeps its setup billed.
var platformSetupFailures = map[string]bool{
	FailureAgentLost:          true,
	FailureRunnerLeaseExpired: true,
	FailureQueueTimeout:       true,
	FailureLogsAuth:           true,
	FailureLogsDropped:        true,
}

// safety: a metered trigger claim reserves from this instant, and zero means
// no reservation is open, so a settle that finds it zero bills nothing twice.
var triggersCreditCols = map[string]string{
	"credit_reserved_at": "INTEGER NOT NULL DEFAULT 0",
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

func applyCreditReferenceMigrationSQLite(ctx context.Context, tx *storeTx) error {
	if err := ensureColumnsSQLite(ctx, tx, "credit_grants", creditGrantReversesCols); err != nil {
		return err
	}
	return createCreditGrantReferenceIndex(ctx, tx)
}

func applyCreditReferenceMigrationPostgres(ctx context.Context, tx *storeTx) error {
	if err := addColumnsTx(ctx, tx, "credit_grants", creditGrantReversesCols); err != nil {
		return err
	}
	return createCreditGrantReferenceIndex(ctx, tx)
}

// safety: grants written before the reference became a key may already repeat
// one, and refusing to open a ledger over an operator note costs more than the
// index buys; the grant path enforces the rule inside the ledger lock anyway.
func createCreditGrantReferenceIndex(ctx context.Context, tx *storeTx) error {
	dupes, err := duplicateGrantReferences(ctx, tx)
	if err != nil {
		return err
	}
	if len(dupes) > 0 {
		slog.Warn("credits: grants repeat a reference, so the database does not enforce the grant key; "+
			"delete the duplicate rows and recreate the index to enforce it",
			"references", strings.Join(dupes, ", "))
		return nil
	}
	_, err = tx.ExecContext(ctx, creditGrantReferenceIndex)
	return err
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
	ID string
	// Team is whose balance the grant pays for. Only this team's charges
	// draw on it.
	Team        Team
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
	ID string
	// Team is whose balance the row drew on.
	Team        Team
	RunID       string
	NodeID      string
	TokenPrefix string
	// Principal names the team whose storage or runner work this row billed.
	// It is empty on legacy and unowned work.
	Principal   string
	Kind        string
	Seconds     int64
	AmountMicro int64
	// StorageBytes is the retained bytes above the free allowance a storage
	// row billed, and zero on every other kind.
	StorageBytes int64
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
	GrantedMicro int64
	// ReversedMicro is what reversals took back, reported as a positive
	// amount and already subtracted from the balance.
	ReversedMicro int64
	// ChargedMicro is every charge row, reservations, refunds and storage
	// included, so that granted less reversed less charged is the balance.
	// [CreditLedgerTotals.ChargedMicro] is the other figure: runner usage
	// alone, with storage beside it.
	ChargedMicro int64
	// StorageChargedMicro is the part of ChargedMicro that billed retained
	// bytes rather than runner time.
	StorageChargedMicro int64
	BalanceMicro        int64
	RateMicroPerSecond  int64
	RateTable           CreditRateTable
	// RateTableSet reports whether an operator wrote the table. A state that
	// reports false bills the default ladder.
	RateTableSet bool
	// WarmCPUClassCores is the largest class a warm runner pool serves.
	WarmCPUClassCores int64
	GraceSeconds      int64
	MaxChargeSeconds  int64
	// StorageRateMicroPerGBDay is what one gibibyte kept for one day costs,
	// and zero is an installation that bills no storage.
	StorageRateMicroPerGBDay int64
	// StorageFreeAllowanceBytes is how many retained bytes a team keeps
	// without being billed for them.
	StorageFreeAllowanceBytes int64
	BurnWindow                time.Duration
	BurnMicro                 int64
	ExhaustedAt               *time.Time
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
	// Checkout names the payment session a paid grant settles, so the
	// checkout it opened stops counting against the balance cap.
	Checkout string
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
		// safety: a paid grant is one checkout's payment, so it is bounded by
		// the largest purchase; that bounds what a leaked grant credential can
		// add, since the balance cap does not refuse a paid grant.
		if req.Kind == CreditGrantPaid && req.AmountMicro > CreditPurchaseMaxCents*MicroCreditsPerCent {
			return fmt.Errorf("credits: a paid grant is one purchase, at most %d micro-credits",
				int64(CreditPurchaseMaxCents*MicroCreditsPerCent))
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

// GrantCredits adds credits to the default team's balance and returns the row
// it holds for them. A grant that lifts that balance above zero clears the
// team's exhaustion stamp, so a node cancelled for an empty balance is the
// last one cancelled. A non-empty reference is idempotent: the row already
// written under that kind and reference is returned instead of a second one.
func (s *Store) GrantCredits(
	ctx context.Context, kind string, amountMicro int64, reference, createdBy string,
) (*CreditGrant, error) {
	return grantCredits(ctx, s.RecordCreditGrant, kind, amountMicro, reference, createdBy)
}

// GrantCredits adds credits to t's balance and returns the row it holds for
// them. It reads the same as [Store.GrantCredits] otherwise.
func (t *Tenant) GrantCredits(
	ctx context.Context, kind string, amountMicro int64, reference, createdBy string,
) (*CreditGrant, error) {
	return grantCredits(ctx, t.RecordCreditGrant, kind, amountMicro, reference, createdBy)
}

func grantCredits(
	ctx context.Context,
	record func(context.Context, CreditGrantRequest) (CreditGrantResult, error),
	kind string, amountMicro int64, reference, createdBy string,
) (*CreditGrant, error) {
	res, err := record(ctx, CreditGrantRequest{
		Kind: kind, AmountMicro: amountMicro, Reference: reference, CreatedBy: createdBy,
	})
	if err != nil {
		return nil, err
	}
	grant := res.Grant
	return &grant, nil
}

// RecordCreditGrant writes req against the default team's balance and reports
// whether it wrote a new row. It refuses a reversal whose Reverses names no
// paid grant of that team, so a refund can only take back a payment the
// team's ledger recorded, and a free grant that would lift the balance above
// [MaxTeamBalanceMicro] with a [CreditBalanceCapError]; a paid grant is never
// held to the cap, because its payment already went through. A reversal may take the balance below zero; the
// claim path then refuses that team's new metered work.
func (s *Store) RecordCreditGrant(
	ctx context.Context, req CreditGrantRequest,
) (CreditGrantResult, error) {
	return s.recordCreditGrant(ctx, DefaultTeam, req)
}

// RecordCreditGrant writes req against t's balance and reports whether it
// wrote a new row. It reads the same as [Store.RecordCreditGrant] otherwise.
func (t *Tenant) RecordCreditGrant(
	ctx context.Context, req CreditGrantRequest,
) (CreditGrantResult, error) {
	return t.s.recordCreditGrant(ctx, t.team, req)
}

func (s *Store) recordCreditGrant(
	ctx context.Context, team Team, req CreditGrantRequest,
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
		ID: id, Team: team, Kind: req.Kind, AmountMicro: req.AmountMicro,
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
		// safety: a payment id is the processor's, so one already paid to
		// another team is a caller mistake and must not pay this team too; any
		// other reference is the team's own name for its grant.
		lookup := team
		if req.Kind == CreditGrantPaid {
			lookup = ""
		}
		existing, found, err := creditGrantByReferenceTx(ctx, tx, lookup, req.Kind, req.Reference)
		if err != nil {
			return CreditGrantResult{}, err
		}
		if found {
			if err := sameGrantTerms(existing, team, req); err != nil {
				return CreditGrantResult{}, err
			}
			return CreditGrantResult{Grant: existing}, tx.Commit()
		}
	}
	if req.Kind == CreditGrantReversal {
		// safety: the reversal has to find the payment inside this team,
		// because reversing another team's grant would move credits between
		// balances that never traded.
		reversed, found, err := creditGrantByReferenceTx(ctx, tx, team, CreditGrantPaid, req.Reverses)
		if err != nil {
			return CreditGrantResult{}, err
		}
		if !found || reversed.Team != team {
			return CreditGrantResult{}, fmt.Errorf(
				"credits: no %s grant carries the reference %q", CreditGrantPaid, req.Reverses)
		}
		already, err := reversedMicroTx(ctx, tx, team, req.Reverses)
		if err != nil {
			return CreditGrantResult{}, err
		}
		if already-req.AmountMicro > reversed.AmountMicro {
			return CreditGrantResult{}, fmt.Errorf("%w: %q paid %d micro-credits, %d are already reversed, not %d more",
				ErrReversalExceedsPayment, req.Reverses, reversed.AmountMicro, already, -req.AmountMicro)
		}
	}
	// safety: a paid grant is money that already moved, so the cap was held
	// when its checkout opened and is not held again here; refusing it would
	// leave a payment with no credits. Only an operator's free grant meets the
	// cap at the ledger.
	if req.Kind == CreditGrantFree {
		before, err := creditBalanceTx(ctx, tx, team)
		if err != nil {
			return CreditGrantResult{}, err
		}
		if err := refuseAboveBalanceCap(before, req.AmountMicro); err != nil {
			return CreditGrantResult{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
        INSERT INTO credit_grants (team, id, kind, amount_micro, reference, reverses, created_by, created_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		string(team), id, req.Kind, req.AmountMicro, req.Reference, req.Reverses,
		req.CreatedBy, now.UnixNano()); err != nil {
		return CreditGrantResult{}, fmt.Errorf("credits: insert grant: %w", err)
	}
	if req.Kind == CreditGrantPaid {
		if err := markCreditCheckoutPaidTx(ctx, tx, team, req.Checkout, now.UnixNano()); err != nil {
			return CreditGrantResult{}, err
		}
	}
	balance, err := creditBalanceTx(ctx, tx, team)
	if err != nil {
		return CreditGrantResult{}, err
	}
	if balance > 0 {
		if err := clearTeamCreditExhaustedTx(ctx, tx, team, now.UnixNano()); err != nil {
			return CreditGrantResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return CreditGrantResult{}, err
	}
	s.invalidateRunnerCap()
	return CreditGrantResult{Grant: grant, Created: true}, nil
}

// safety: a webhook replay carries the terms it carried the first time, so
// different terms under one reference are a caller mistake rather than a
// retry, and answering with the stored row would swallow the difference.
func sameGrantTerms(stored CreditGrant, team Team, req CreditGrantRequest) error {
	// safety: the reference is the deployment's idempotency key, a payment id
	// rather than a name a team chose, so one seen under a second team is a
	// caller mistake and must not grant that team the first team's credits.
	if stored.Team != team {
		return fmt.Errorf("%w: %q belongs to team %s, not %s",
			ErrCreditGrantConflict, req.Reference, stored.Team, team)
	}
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

// reversedMicroTx is how much of a payment the team's reversals already
// took back, as a positive amount.
func reversedMicroTx(ctx context.Context, tx *storeTx, team Team, payment string) (int64, error) {
	var sum sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT SUM(amount_micro) FROM credit_grants
	  WHERE team = ? AND kind = ? AND reverses = ?`, string(team), CreditGrantReversal, payment).Scan(&sum)
	return -sum.Int64, err
}

// creditGrantByReferenceTx finds the grant of kind carrying reference in
// team, or in any team when team is empty.
func creditGrantByReferenceTx(
	ctx context.Context, tx *storeTx, team Team, kind, reference string,
) (CreditGrant, bool, error) {
	var g CreditGrant
	var created int64
	var owner string
	err := tx.QueryRowContext(ctx, `SELECT team, id, kind, amount_micro, reference, reverses, created_by, created_at
	  FROM credit_grants WHERE (? = '' OR team = ?) AND kind = ? AND reference = ?
	  ORDER BY created_at ASC, id ASC LIMIT 1`, string(team), string(team), kind, reference).
		Scan(&owner, &g.ID, &g.Kind, &g.AmountMicro, &g.Reference, &g.Reverses, &g.CreatedBy, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return CreditGrant{}, false, nil
	}
	if err != nil {
		return CreditGrant{}, false, err
	}
	g.Team = Team(owner)
	g.CreatedAt = time.Unix(0, created).UTC()
	return g, true, nil
}

// CreditBalanceMicro returns the default team's grants minus its charges, in
// micro-credits. A controller that was never granted anything reads zero.
//
// safety: the unscoped twin reads [DefaultTeam] because a local install holds
// exactly that one team; summing every team would let a funded team pay for
// an unfunded one's work.
func (s *Store) CreditBalanceMicro(ctx context.Context) (int64, error) {
	return s.creditBalanceMicro(ctx, DefaultTeam)
}

// CreditBalanceMicro returns t's grants minus t's charges, in micro-credits.
// Another team's grants are not in it, so a team that has spent its own
// grants reads an empty balance however well funded its neighbors are.
func (t *Tenant) CreditBalanceMicro(ctx context.Context) (int64, error) {
	return t.s.creditBalanceMicro(ctx, t.team)
}

func (s *Store) creditBalanceMicro(ctx context.Context, team Team) (int64, error) {
	var granted, charged sql.NullInt64
	if err := s.queryRow(ctx, creditBalanceSQL,
		string(team), string(team)).Scan(&granted, &charged); err != nil {
		return 0, err
	}
	return granted.Int64 - charged.Int64, nil
}

const creditBalanceSQL = `SELECT (SELECT SUM(amount_micro) FROM credit_grants WHERE team = ?),
        (SELECT SUM(amount_micro) FROM credit_charges WHERE team = ?)`

// safety: a reversal is a grant row with a negative amount, so a reader that
// wants the two apart asks for them apart; the balance is the same either way.
const creditGrantSplitSQL = `SELECT
        (SELECT SUM(amount_micro) FROM credit_grants WHERE team = ? AND kind != ?),
        (SELECT -SUM(amount_micro) FROM credit_grants WHERE team = ? AND kind = ?),
        (SELECT SUM(amount_micro) FROM credit_charges WHERE team = ?)`

func creditBalanceTx(ctx context.Context, tx *storeTx, team Team) (int64, error) {
	var granted, charged sql.NullInt64
	if err := tx.QueryRowContext(ctx, creditBalanceSQL,
		string(team), string(team)).Scan(&granted, &charged); err != nil {
		return 0, err
	}
	return granted.Int64 - charged.Int64, nil
}

// safety: an executor enrolls with the deployment and is offered work from
// every team on it, so the team a charge bills comes off the run row rather
// than off the caller. A run the store has lost reads as [DefaultTeam]
// because that is the team every row written before the tenant key lives in.
func creditTeamForRunTx(ctx context.Context, tx *storeTx, runID string) (Team, error) {
	team, found, err := runOwnerTx(ctx, tx, runID)
	if err != nil {
		return "", err
	}
	if !found || team == "" {
		return DefaultTeam, nil
	}
	return team, nil
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

// CreditState reports the default team's ledger together with the rate, the
// grace period, and the credits it burned over window.
//
// safety: the unscoped twin reads [DefaultTeam] for the same reason
// [Store.CreditBalanceMicro] does.
func (s *Store) CreditState(ctx context.Context, window time.Duration) (CreditState, error) {
	return s.creditState(ctx, DefaultTeam, window)
}

// CreditState reports t's ledger together with the rate, the grace period,
// and the credits t burned over window. The rate, the grace period and the
// charge cap are the deployment's and are the same for every team.
func (t *Tenant) CreditState(ctx context.Context, window time.Duration) (CreditState, error) {
	return t.s.creditState(ctx, t.team, window)
}

func (s *Store) creditState(ctx context.Context, team Team, window time.Duration) (CreditState, error) {
	out := CreditState{BurnWindow: window}
	var granted, reversed, charged sql.NullInt64
	if err := s.queryRow(ctx, creditGrantSplitSQL,
		string(team), CreditGrantReversal, string(team), CreditGrantReversal,
		string(team)).Scan(&granted, &reversed, &charged); err != nil {
		return out, err
	}
	out.GrantedMicro = granted.Int64
	out.ReversedMicro = reversed.Int64
	out.ChargedMicro = charged.Int64
	out.BalanceMicro = granted.Int64 - reversed.Int64 - charged.Int64

	var storageCharged sql.NullInt64
	if err := s.queryRow(ctx,
		`SELECT SUM(amount_micro) FROM credit_charges WHERE team = ? AND kind = ?`,
		string(team), CreditChargeStorage).Scan(&storageCharged); err != nil {
		return out, err
	}
	out.StorageChargedMicro = storageCharged.Int64

	if window > 0 {
		var burn sql.NullInt64
		if err := s.queryRow(ctx,
			`SELECT SUM(amount_micro) FROM credit_charges WHERE team = ? AND charged_at >= ?`,
			string(team), time.Now().Add(-window).UnixNano()).Scan(&burn); err != nil {
			return out, err
		}
		out.BurnMicro = burn.Int64
	}

	settings, err := s.CreditSettings(ctx)
	if err != nil {
		return out, err
	}
	out.RateMicroPerSecond = settings.RateMicroPerSecond
	out.RateTable = settings.RateTable
	out.RateTableSet = settings.RateTableSet
	out.WarmCPUClassCores = settings.WarmCPUClassCores
	out.GraceSeconds = settings.GraceSeconds
	out.MaxChargeSeconds = settings.MaxChargeSeconds
	out.StorageRateMicroPerGBDay = settings.StorageRateMicroPerGBDay
	out.StorageFreeAllowanceBytes = settings.StorageFreeAllowanceBytes
	exhausted, err := s.creditExhaustedAt(ctx, team)
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
	// ChargedMicro is what runner execution billed, and storage is not in it;
	// StorageMicro carries that. [CreditState.ChargedMicro] is the other
	// figure: every charge row, so that granted less charged is the balance.
	ChargedMicro int64
	// RefundedMicro is the unused reservation tail returned at finish,
	// reported as a positive amount.
	RefundedMicro int64
	// StorageMicro is what retained bytes billed, which is the part of the
	// ledger's spend that bought no runner time.
	StorageMicro int64
	// SettledSeconds is the runner seconds the ledger has finished charging
	// for: every charge row less the part of an open reservation a finish
	// would still refund. A reservation enters this total after execution
	// starts and as the node consumes it, so the figure only ever grows and a
	// caller may export it as a counter.
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
	// safety: a storage row bills bytes rather than runner time, so its
	// interval stays out of the seconds total the cloud placement series is.
	var settled, charged int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(CASE WHEN kind = ? THEN amount_micro ELSE 0 END), 0),
                        COALESCE(SUM(CASE WHEN kind = ? THEN amount_micro ELSE 0 END), 0),
                        COALESCE(SUM(CASE WHEN kind = ? THEN -amount_micro ELSE 0 END), 0),
                        COALESCE(SUM(CASE WHEN kind = ? THEN amount_micro ELSE 0 END), 0),
                        COALESCE(SUM(amount_micro), 0),
                        COALESCE(SUM(CASE WHEN kind = ? THEN 0 ELSE seconds END), 0)
                   FROM credit_charges`,
		CreditChargeReservation, CreditChargeUsage, CreditChargeRefund,
		CreditChargeStorage, CreditChargeStorage,
	).Scan(&out.ReservedMicro, &out.ChargedMicro, &out.RefundedMicro, &out.StorageMicro,
		&settled, &charged); err != nil {
		return out, err
	}

	// safety: counting a reservation whole would make the seconds total fall
	// by its refund, so a reservation a finish could still return is held
	// back: a node's until its machine starts on it, a trigger's until its
	// claim settles. Past that point the minimum is consumed rather than
	// refunded; only a setup the platform fails returns billed seconds, and
	// that is the one refund that lowers this total.
	var refundable int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(CASE WHEN credit_billing_from = 0 AND execution_started_at IS NULL
	                                  THEN ? ELSE 0 END), 0)
	                   FROM nodes WHERE credit_charged_through != 0`,
		int64(MinBillableSeconds)).Scan(&refundable); err != nil {
		return out, err
	}
	var triggerRefundable int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(COUNT(*), 0) * ? FROM triggers WHERE credit_reserved_at != 0`,
		int64(MinBillableSeconds)).Scan(&triggerRefundable); err != nil {
		return out, err
	}
	out.SettledSeconds = charged - refundable - triggerRefundable

	out.BalanceMicro = out.GrantedFreeMicro + out.GrantedPaidMicro - out.ReversedMicro - settled
	return out, tx.Commit()
}

// CreditRateMicroPerSecond returns the price of one cloud runner second in
// micro-credits. A controller that never set one reads the default.
func (s *Store) CreditRateMicroPerSecond(ctx context.Context) (int64, error) {
	return s.creditSetting(ctx, metaKeyCreditRateMicro, DefaultCreditRateMicro)
}

// CreditGraceSeconds returns how long a node keeps running once it has
// consumed the reservation its claim paid for with the balance at zero.
func (s *Store) CreditGraceSeconds(ctx context.Context) (int64, error) {
	return s.creditSetting(ctx, metaKeyCreditGraceSecs, DefaultCreditGraceSeconds)
}

// CreditMaxChargeSeconds returns the most seconds one charge may bill, which
// is what keeps a controller outage or a stalled heartbeat loop from billing
// the gap it left behind.
func (s *Store) CreditMaxChargeSeconds(ctx context.Context) (int64, error) {
	return s.creditSetting(ctx, metaKeyCreditMaxCharge, DefaultCreditMaxChargeSeconds)
}

// CreditSettings are the values the ledger prices work with. Every one of
// them is the deployment's, so a controller serving many teams prices them
// all the same way.
type CreditSettings struct {
	// RateMicroPerSecond is the price of one cloud runner second.
	RateMicroPerSecond int64
	// RateTable prices every cpu class. RateTableSet reports whether an
	// operator wrote the table or this is the default ladder.
	RateTable    CreditRateTable
	RateTableSet bool
	// WarmCPUClassCores is the largest cpu class a warm runner pool serves. A
	// node above it is never offered to or claimed by a warm runner and is
	// executed on a Kubernetes node sized to its class instead.
	WarmCPUClassCores int64
	// GraceSeconds is how long a node that has consumed its claim reservation
	// on an empty balance keeps running before the controller cancels it.
	GraceSeconds int64
	// MaxChargeSeconds is the most seconds any one charge may bill.
	MaxChargeSeconds int64
	// StorageRateMicroPerGBDay is what one gibibyte kept for one day costs.
	// Zero bills no storage, which is what an installation that never set it
	// reads.
	StorageRateMicroPerGBDay int64
	// StorageFreeAllowanceBytes is how many retained bytes a team keeps
	// without being billed for them.
	StorageFreeAllowanceBytes int64
}

// CreditSettingsUpdate names the settings to change. A nil field leaves that
// setting as it stands, so a caller changing one value sends one field and a
// field added later joins without disturbing the existing settings.
type CreditSettingsUpdate struct {
	RateMicroPerSecond        *int64
	RateTable                 *CreditRateTable
	WarmCPUClassCores         *int64
	GraceSeconds              *int64
	MaxChargeSeconds          *int64
	StorageRateMicroPerGBDay  *int64
	StorageFreeAllowanceBytes *int64
}

// CreditSettings reads every setting together. A controller that set none of
// them reads the defaults.
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
// scalar rate beside a rate table, a negative grace period, and a charge cap
// outside MinCreditMaxChargeSeconds to MaxCreditMaxChargeSeconds. Every
// refusal is an ErrInvalidCreditSetting. Programmatic callers may update the
// scalar after storing a table; operator surfaces use
// [Store.SetOperatorCreditSettings] to keep the table authoritative.
func (s *Store) SetCreditSettings(ctx context.Context, up CreditSettingsUpdate) (_ CreditSettings, err error) {
	writes, err := creditSettingsWrites(up)
	if err != nil {
		return CreditSettings{}, err
	}
	tx, err := s.beginCreditSettingsTx(ctx)
	if err != nil {
		return CreditSettings{}, err
	}
	defer rollbackUnlessDone(tx, &err)
	out, err := writeCreditSettingsTx(ctx, tx, writes)
	if err != nil {
		return CreditSettings{}, err
	}
	return out, tx.Commit()
}

// SetOperatorCreditSettings applies an operator's settings update and keeps a
// stored rate table authoritative. A scalar rate may win before the first
// table is stored; after that, the operator must update the table. The
// authority check and every write share one serialized transaction.
func (s *Store) SetOperatorCreditSettings(
	ctx context.Context, up CreditSettingsUpdate,
) (_ CreditSettings, err error) {
	writes, err := creditSettingsWrites(up)
	if err != nil {
		return CreditSettings{}, err
	}
	tx, err := s.beginCreditSettingsTx(ctx)
	if err != nil {
		return CreditSettings{}, err
	}
	defer rollbackUnlessDone(tx, &err)
	if up.RateMicroPerSecond != nil && up.RateTable == nil {
		raw, err := creditSettingRawTx(ctx, tx, metaKeyCreditRateTable)
		if err != nil {
			return CreditSettings{}, err
		}
		if raw != "" {
			return CreditSettings{}, fmt.Errorf(
				"%w: rate_micro_per_second is the four-core entry of the rate table; write rate_table instead",
				ErrInvalidCreditSetting)
		}
	}
	out, err := writeCreditSettingsTx(ctx, tx, writes)
	if err != nil {
		return CreditSettings{}, err
	}
	return out, tx.Commit()
}

func creditSettingsWrites(up CreditSettingsUpdate) (map[string]string, error) {
	if err := up.validate(); err != nil {
		return nil, err
	}
	scalars := up.byKey()
	writes := make(map[string]string, len(scalars)+2)
	for key, value := range scalars {
		writes[key] = strconv.FormatInt(value, 10)
	}
	if up.RateTable != nil {
		if err := addCreditRateTableWrites(writes, *up.RateTable); err != nil {
			return nil, err
		}
	}
	return writes, nil
}

// safety: SQLite's immediate transaction already serializes writers. The
// Postgres advisory lock gives its read-committed transaction the same single
// settings writer before an operator checks which rate representation owns the
// price.
func (s *Store) beginCreditSettingsTx(ctx context.Context) (*storeTx, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	if s.dialect == DialectPostgres {
		if _, err := tx.ExecContext(ctx,
			`SELECT pg_advisory_xact_lock(hashtext(?))`, "sparkwing/credit-settings"); err != nil {
			rollbackOrLog(tx)
			return nil, err
		}
	}
	return tx, nil
}

func writeCreditSettingsTx(
	ctx context.Context, tx *storeTx, writes map[string]string,
) (CreditSettings, error) {
	keys := make([]string, 0, len(writes))
	for key := range writes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	// safety: Postgres locks metadata rows as it updates them, so overlapping
	// batches use one key order and cannot wait on each other in reverse.
	for _, key := range keys {
		if _, err := tx.ExecContext(ctx, upsertCreditSettingSQL,
			key, writes[key], time.Now().UnixNano()); err != nil {
			return CreditSettings{}, err
		}
	}
	return creditSettingsTx(ctx, tx)
}

// ErrInvalidCreditSetting is the class of every refusal SetCreditSettings
// returns, so a caller answers a bad value differently from a database
// failure.
var ErrInvalidCreditSetting = errors.New("credits: invalid setting")

func (u CreditSettingsUpdate) byKey() map[string]int64 {
	out := make(map[string]int64, 3)
	if u.RateMicroPerSecond != nil {
		out[metaKeyCreditRateMicro] = *u.RateMicroPerSecond
	}
	if u.WarmCPUClassCores != nil {
		out[metaKeyWarmCPUClass] = *u.WarmCPUClassCores
	}
	if u.GraceSeconds != nil {
		out[metaKeyCreditGraceSecs] = *u.GraceSeconds
	}
	if u.MaxChargeSeconds != nil {
		out[metaKeyCreditMaxCharge] = *u.MaxChargeSeconds
	}
	if u.StorageRateMicroPerGBDay != nil {
		out[metaKeyStorageRateMicroPerGBDay] = *u.StorageRateMicroPerGBDay
	}
	if u.StorageFreeAllowanceBytes != nil {
		out[metaKeyStorageFreeAllowanceBytes] = *u.StorageFreeAllowanceBytes
	}
	return out
}

// safety: every value is judged before any of them is written, so a body that
// names one good setting and one bad one moves neither.
func (u CreditSettingsUpdate) validate() error {
	if u.RateMicroPerSecond == nil && u.RateTable == nil && u.WarmCPUClassCores == nil &&
		u.GraceSeconds == nil && u.MaxChargeSeconds == nil &&
		u.StorageRateMicroPerGBDay == nil && u.StorageFreeAllowanceBytes == nil {
		return fmt.Errorf(
			"%w: name at least one of the rate, the warm cpu class, the grace period, "+
				"the charge cap, the storage rate, or the free storage allowance",
			ErrInvalidCreditSetting)
	}
	if u.RateMicroPerSecond != nil && u.RateTable != nil {
		return fmt.Errorf(
			"%w: rate_micro_per_second is the four-core entry of the rate table; name one or the other",
			ErrInvalidCreditSetting)
	}
	if u.RateTable != nil {
		if err := u.RateTable.Validate(); err != nil {
			return err
		}
	}
	if u.WarmCPUClassCores != nil {
		if err := validWarmCPUClass(*u.WarmCPUClassCores); err != nil {
			return err
		}
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
	if u.StorageRateMicroPerGBDay != nil {
		if err := validStorageRate(*u.StorageRateMicroPerGBDay); err != nil {
			return err
		}
	}
	if u.StorageFreeAllowanceBytes != nil {
		if err := validStorageFreeAllowance(*u.StorageFreeAllowanceBytes); err != nil {
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

// safety: the warm class is a whole number of cores the ladder can name, and
// zero is the cluster that runs nothing warm, so only a negative one is refused.
func validWarmCPUClass(cores int64) error {
	if cores < 0 {
		return fmt.Errorf("%w: the warm cpu class must not be negative, got %d",
			ErrInvalidCreditSetting, cores)
	}
	return nil
}

// WarmCPUClassCores returns the largest cpu class a warm runner pool serves. A
// controller that set none serves [DefaultWarmCPUClassCores].
func (s *Store) WarmCPUClassCores(ctx context.Context) (int64, error) {
	return s.creditSetting(ctx, metaKeyWarmCPUClass, DefaultWarmCPUClassCores)
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
	warm, err := creditSettingTx(ctx, tx, metaKeyWarmCPUClass, DefaultWarmCPUClassCores)
	if err != nil {
		return out, err
	}
	storageRate, err := creditSettingTx(ctx, tx, metaKeyStorageRateMicroPerGBDay, 0)
	if err != nil {
		return out, err
	}
	storageFree, err := creditSettingTx(ctx, tx, metaKeyStorageFreeAllowanceBytes, 0)
	if err != nil {
		return out, err
	}
	tableRaw, err := creditSettingRawTx(ctx, tx, metaKeyCreditRateTable)
	if err != nil {
		return out, err
	}
	table, err := creditRateTable(tableRaw, rate)
	if err != nil {
		return out, err
	}
	out.RateMicroPerSecond = rate
	out.RateTable = table
	out.RateTableSet = tableRaw != ""
	out.WarmCPUClassCores = warm
	out.GraceSeconds = grace
	out.MaxChargeSeconds = maxCharge
	out.StorageRateMicroPerGBDay = storageRate
	out.StorageFreeAllowanceBytes = storageFree
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

func (s *Store) creditExhaustedAt(ctx context.Context, team Team) (*time.Time, error) {
	var ns int64
	err := s.queryRow(ctx,
		`SELECT credit_exhausted_at FROM teams WHERE name = ?`, string(team)).Scan(&ns)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if ns == 0 {
		return nil, nil
	}
	at := time.Unix(0, ns).UTC()
	return &at, nil
}

// safety: the first stamp is the one that stands, because the marker records
// when this team's balance first read empty and every later heartbeat would
// otherwise push it forward. The answer is the instant in force after the
// write, so a caller reads what it stamped or what got there first.
func stampTeamCreditExhaustedTx(
	ctx context.Context, tx *storeTx, team Team, nowNS int64,
) (int64, error) {
	if _, err := tx.ExecContext(ctx,
		`UPDATE teams SET credit_exhausted_at = ?, updated_at = ?
		  WHERE name = ? AND credit_exhausted_at = 0`,
		nowNS, nowNS, string(team)); err != nil {
		return 0, err
	}
	return teamCreditExhaustedAtTx(ctx, tx, team)
}

// safety: a team the registry does not hold reads zero rather than failing,
// because the marker only reports how long a balance has been empty and the
// cancellation deadline is the node's own anchor.
func teamCreditExhaustedAtTx(ctx context.Context, tx *storeTx, team Team) (int64, error) {
	var at int64
	err := tx.QueryRowContext(ctx,
		`SELECT credit_exhausted_at FROM teams WHERE name = ?`, string(team)).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return at, err
}

func clearTeamCreditExhaustedTx(ctx context.Context, tx *storeTx, team Team, nowNS int64) error {
	// safety: the predicate keeps a funded team from writing the row on every
	// heartbeat.
	_, err := tx.ExecContext(ctx,
		`UPDATE teams SET credit_exhausted_at = 0, updated_at = ?
		  WHERE name = ? AND credit_exhausted_at != 0`,
		nowNS, string(team))
	return err
}

// safety: the column and the backfill land together, because a stamp left
// behind in sparkwing_meta would read as a team that has never run out and
// would start that team's grace clock over.
func applyTeamCreditStateMigrationSQLite(ctx context.Context, tx *storeTx) error {
	if err := ensureColumnsSQLite(ctx, tx, "teams", teamsCreditExhaustedCols); err != nil {
		return err
	}
	return backfillTeamCreditStateTx(ctx, tx)
}

func applyTeamCreditStateMigrationPostgres(ctx context.Context, tx *storeTx) error {
	if err := addColumnsTx(ctx, tx, "teams", teamsCreditExhaustedCols); err != nil {
		return err
	}
	return backfillTeamCreditStateTx(ctx, tx)
}

func backfillTeamCreditStateTx(ctx context.Context, tx *storeTx) error {
	var raw string
	err := tx.QueryRowContext(ctx,
		`SELECT value FROM sparkwing_meta WHERE key = ?`, metaKeyCreditExhaustedAt).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		if at := parseCreditSetting(raw, 0); at != 0 {
			if _, err := tx.ExecContext(ctx,
				`UPDATE teams SET credit_exhausted_at = ? WHERE name = ?`,
				at, string(DefaultTeam)); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM sparkwing_meta WHERE key = ?`, metaKeyCreditExhaustedAt); err != nil {
			return err
		}
	}
	return backfillStorageWatermarkTeamTx(ctx, tx)
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
	rows, err := s.query(ctx, `SELECT id, run_id, node_id, token_prefix, principal, kind,
	         seconds, amount_micro, storage_bytes, cpu_class, rate_micro_per_second, charged_at
	  FROM credit_charges ORDER BY charged_at DESC, id DESC LIMIT ?`, creditLimit(limit))
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []CreditCharge
	for rows.Next() {
		var c CreditCharge
		var charged int64
		if err := rows.Scan(&c.ID, &c.RunID, &c.NodeID, &c.TokenPrefix, &c.Principal, &c.Kind,
			&c.Seconds, &c.AmountMicro, &c.StorageBytes,
			&c.CPUClassCores, &c.RateMicroPerSecond, &charged); err != nil {
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

// ErrMeteredInProcessNodes is returned when a metered credential claims a
// trigger without naming a node runner that claims each node, such as k8s or
// warm. Nodes a trigger holder runs in its own process hold no node claim,
// so no credit is ever charged for them.
var ErrMeteredInProcessNodes = errors.New("a metered credential must run a trigger's nodes through node claims")

// triggerCreditNodeID is the node id on the ledger rows that bill a trigger's
// own step, the planning and orchestration its holder runs on the claiming
// pool, which is no node of the run.
const triggerCreditNodeID = ""

// safety: the trigger step runs on the paid pool, so a metered claim reserves
// its minimum here, inside the claim's transaction and under the ledger lock,
// the way a node claim does; a check alone let every poller admit a run
// against the same minimum. The claim names no pool size, so the step is billed
// at the cheapest class. A refusal leaves the trigger pending, because it rolls
// the claim back.
func reserveTriggerCreditsTx(
	ctx context.Context, tx *storeTx, claimant ClaimIdentity, team Team, triggerID string, now time.Time,
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
	balance, err := creditBalanceTx(ctx, tx, team)
	if err != nil {
		return err
	}
	class := table.Sorted()[0]
	required := class.MicroPerSecond * MinBillableSeconds
	if balance < required {
		return &InsufficientCreditsError{BalanceMicro: balance, RequiredMicro: required, RunID: triggerID}
	}
	principal, err := runPrincipalTx(ctx, tx, triggerID)
	if err != nil {
		return err
	}
	if _, err := insertCreditChargeTx(ctx, tx, chargeWindow{
		Team: team, RunID: triggerID, NodeID: triggerCreditNodeID,
		TokenPrefix: claimant.TokenPrefix, Principal: principal,
		NowNS: now.UnixNano(), Rate: class.MicroPerSecond, Class: class.Cores,
	}, CreditChargeReservation, MinBillableSeconds, required); err != nil {
		return err
	}
	// safety: a window a previous claim left open is overwritten rather than
	// settled, so that claim keeps the minimum it reserved and no more; every
	// path that ends a claim settles it first, and only an older binary does not.
	_, err = tx.ExecContext(ctx,
		`UPDATE triggers SET credit_reserved_at = ? WHERE team = ? AND id = ?`,
		now.UnixNano(), string(team), triggerID)
	return err
}

// safety: every path that ends a trigger claim settles here before its own
// write, in the same transaction, so a fence that refuses the write rolls the
// settlement back with it. The step is billed through the lease's end when the
// lease lapsed first, because a holder that stopped heartbeating is not
// running. A step shorter than the minimum pays the minimum, and a claim that
// never started its run is refunded whole.
func settleTriggerCreditsTx(ctx context.Context, tx *storeTx, triggerID string, now time.Time, refundAll bool) error {
	var team string
	var reservedAt int64
	var lease sql.NullInt64
	err := tx.QueryRowContext(ctx,
		`SELECT team, credit_reserved_at, lease_expires_at FROM triggers WHERE id = ?`+tx.forUpdate(),
		triggerID).Scan(&team, &reservedAt, &lease)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil || reservedAt == 0 {
		return err
	}
	settleNS := now.UnixNano()
	if lease.Valid && lease.Int64 < settleNS {
		settleNS = lease.Int64
	}
	settleNS = max(settleNS, reservedAt)
	return settleTriggerWindowTx(ctx, tx, Team(team), triggerID, reservedAt, settleNS, refundAll)
}

func settleTriggerWindowTx(
	ctx context.Context, tx *storeTx, team Team, triggerID string, reservedAt, settleNS int64, refundAll bool,
) error {
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return err
	}
	var tokenPrefix, principal string
	var seconds, amount, class, rate int64
	err := tx.QueryRowContext(ctx,
		`SELECT token_prefix, principal, seconds, amount_micro, cpu_class, rate_micro_per_second
		  FROM credit_charges
		 WHERE team = ? AND run_id = ? AND node_id = ? AND kind = ?
		 ORDER BY charged_at DESC, id DESC LIMIT 1`,
		string(team), triggerID, triggerCreditNodeID, CreditChargeReservation).Scan(
		&tokenPrefix, &principal, &seconds, &amount, &class, &rate)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return err
	default:
		w := chargeWindow{
			Team: team, RunID: triggerID, NodeID: triggerCreditNodeID,
			TokenPrefix: tokenPrefix, Principal: principal,
			NowNS: settleNS, Rate: rate, Class: class,
		}
		elapsed := (settleNS - reservedAt) / int64(time.Second)
		switch {
		case refundAll:
			_, err = insertCreditChargeTx(ctx, tx, w, CreditChargeRefund, -seconds, -amount)
		case elapsed > seconds:
			table, tableErr := creditRateTableTx(ctx, tx)
			if tableErr != nil {
				return tableErr
			}
			w.Rate = chargeRate(table, class)
			over := elapsed - seconds
			_, err = insertCreditChargeTx(ctx, tx, w, CreditChargeUsage, over, w.Rate*over)
		}
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE triggers SET credit_reserved_at = 0 WHERE team = ? AND id = ?`,
		string(team), triggerID)
	return err
}

// safety: reserving inside the claim's own transaction is what keeps concurrent
// runners from each reading the same balance and claiming against it.
//
// executing reports that the claimant is the machine that runs the node, as a
// runner polling the queue or accepting an offer is, so billing starts at the
// claim and covers its fetch and compile. A dispatcher that claims a node
// before creating the pod that runs it passes false, and billing starts when
// that pod first renews the claim or starts execution.
func (s *Store) reserveNodeCreditsTx(
	ctx context.Context, tx *storeTx, claimant ClaimIdentity, runID, nodeID string, now time.Time,
	executing bool,
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
	team, err := creditTeamForRunTx(ctx, tx, runID)
	if err != nil {
		return err
	}
	balance, err := creditBalanceTx(ctx, tx, team)
	if err != nil {
		return err
	}
	class, classErr := nodeCreditClassTx(ctx, tx, table, runID, nodeID)
	var unpriced *UnpricedCPUClassError
	if classErr != nil && !errors.As(classErr, &unpriced) {
		return classErr
	}
	required := class.MicroPerSecond * MinBillableSeconds
	if unpriced != nil {
		required = table.BaseRate() * MinBillableSeconds
	}
	// safety: an empty balance is the refusal a runner already understands, so
	// it is reported before a guard that would mask it with a different code.
	if balance < required {
		return &InsufficientCreditsError{BalanceMicro: balance, RequiredMicro: required, RunID: runID, NodeID: nodeID}
	}
	if unpriced != nil {
		unpriced.RunID, unpriced.NodeID = runID, nodeID
		return unpriced
	}
	principal, err := runPrincipalTx(ctx, tx, runID)
	if err != nil {
		return err
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
		string(team), id, runID, nodeID, claimant.TokenPrefix, principal, CreditChargeReservation,
		int64(MinBillableSeconds), required, class.Cores, class.MicroPerSecond,
		now.UnixNano()); err != nil {
		return fmt.Errorf("credits: reserve: %w", err)
	}
	through := now.Add(MinBillableSeconds * time.Second).UnixNano()
	billingFrom := int64(0)
	if executing {
		billingFrom = now.UnixNano()
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE nodes SET credit_charged_through = ?, credit_cpu_class = ?, credit_billing_from = ?
		  WHERE run_id = ? AND node_id = ?`,
		through, class.Cores, billingFrom, runID, nodeID)
	return err
}

const insertCreditChargeSQL = `
        INSERT INTO credit_charges (team, id, run_id, node_id, token_prefix, principal, kind, seconds, amount_micro,
                cpu_class, rate_micro_per_second, charged_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

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

type creditChargeTxResult struct {
	CreditChargeResult
	settledAt time.Time
}

// ChargeNodeCredits bills the seconds this node has run since its previous
// charge and reports whether the balance can still pay for it. Charging is
// idempotent within a second: a second call in the same second advances
// nothing and writes no row. A node still inside its claim reservation is
// charged nothing, because the reservation already paid for those seconds.
func (s *Store) ChargeNodeCredits(ctx context.Context, runID, nodeID, tokenPrefix string, now time.Time) (CreditChargeResult, error) {
	return s.chargeNode(ctx, runID, nodeID, tokenPrefix, now, false)
}

// FinalizeNodeCredits settles a metered node when it stops running: it bills
// the tail since the last charge and releases the node's charge window so a
// later attempt starts its own. A node that finishes inside its reservation
// pays the whole reservation, which is [MinBillableSeconds]; one whose
// execution never started gets the reservation back. It is a no-op for a node
// that was never metered and for one already settled.
func (s *Store) FinalizeNodeCredits(ctx context.Context, runID, nodeID, tokenPrefix string, now time.Time) (CreditChargeResult, error) {
	return s.chargeNode(ctx, runID, nodeID, tokenPrefix, now, true)
}

func (s *Store) chargeNode(
	ctx context.Context, runID, nodeID, tokenPrefix string, now time.Time, final bool,
) (_ CreditChargeResult, err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return CreditChargeResult{}, err
	}
	defer rollbackUnlessDone(tx, &err)
	out, err := s.chargeNodeTx(ctx, tx, runID, nodeID, tokenPrefix, now, final)
	if err != nil {
		return out.CreditChargeResult, err
	}
	if err := tx.Commit(); err != nil {
		return out.CreditChargeResult, err
	}
	return out.CreditChargeResult, nil
}

func (s *Store) chargeNodeTx(
	ctx context.Context, tx *storeTx, runID, nodeID, tokenPrefix string, now time.Time, final bool,
) (creditChargeTxResult, error) {
	var out creditChargeTxResult
	var anchor, class, billingFrom int64
	var startedAt sql.NullInt64
	var failureReason string
	err := tx.QueryRowContext(ctx,
		`SELECT credit_charged_through, credit_cpu_class, execution_started_at,
		        credit_billing_from, failure_reason FROM nodes
		  WHERE run_id = ? AND node_id = ?`+tx.forUpdate(),
		runID, nodeID).Scan(&anchor, &class, &startedAt, &billingFrom, &failureReason)
	if errors.Is(err, sql.ErrNoRows) {
		return out, notFound("node", runID+"/"+nodeID)
	}
	if err != nil {
		return out, err
	}
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
	principal, err := runPrincipalTx(ctx, tx, runID)
	if err != nil {
		return out, err
	}
	team, err := creditTeamForRunTx(ctx, tx, runID)
	if err != nil {
		return out, err
	}

	// safety: a node claimed by an older binary has no billing start, and its
	// execution start is where that binary began billing.
	if billingFrom == 0 && startedAt.Valid {
		billingFrom = startedAt.Int64
	}
	nowNS := now.UnixNano()
	// safety: cancellation can wait on transaction locks while billing or a
	// fenced attempt starts. A stale caller timestamp must neither refund time
	// before that boundary nor finish a node before its execution began.
	if billingFrom != 0 && nowNS < billingFrom {
		nowNS = billingFrom
	}
	if startedAt.Valid && nowNS < startedAt.Int64 {
		nowNS = startedAt.Int64
	}
	out.settledAt = time.Unix(0, nowNS)
	rate := chargeRate(table, class)
	through := anchor
	switch {
	case anchor != 0 && billingFrom == 0 && !final:
		// safety: only the machine running the node renews its claim, so the
		// first renewal is where its work began; the reservation covers the
		// minimum from here rather than from a claim the pod never saw.
		through = nowNS + MinBillableSeconds*int64(time.Second)
		_, err := tx.ExecContext(ctx,
			`UPDATE nodes SET credit_billing_from = ?, credit_charged_through = ?
			  WHERE run_id = ? AND node_id = ?`,
			nowNS, through, runID, nodeID)
		return out, err
	case billingFrom == 0 || (!startedAt.Valid && platformSetupFailures[failureReason]):
		if final && anchor != 0 {
			out.Charge, err = refundClaimTx(ctx, tx, team, runID, nodeID, nowNS)
			if err != nil {
				return out, err
			}
			through = 0
		}
	default:
		out.Charge, out.ForgivenSeconds, through, err = settleChargeWindow(
			ctx, tx, chargeWindow{
				Team:  team,
				RunID: runID, NodeID: nodeID, TokenPrefix: tokenPrefix, Principal: principal,
				Anchor: anchor, NowNS: nowNS, Rate: rate, Class: class,
				MaxCharge: maxCharge, Final: final,
			})
		if err != nil {
			return out, err
		}
	}
	if through != anchor {
		if _, err := tx.ExecContext(ctx,
			`UPDATE nodes SET credit_charged_through = ? WHERE run_id = ? AND node_id = ?`,
			through, runID, nodeID); err != nil {
			return out, err
		}
	}
	if final && anchor == 0 {
		return out, nil
	}

	balance, err := creditBalanceTx(ctx, tx, team)
	if err != nil {
		return out, err
	}
	out.BalanceMicro = balance
	if billingFrom == 0 && !final {
		return out, nil
	}
	exhaustedFor, cancel, err := settleCreditExhaustionTx(ctx, tx, creditExhaustion{
		Team: team, Balance: balance, NowNS: nowNS, Grace: grace,
		RunID: runID, NodeID: nodeID, Anchor: anchor,
	})
	if err != nil {
		return out, err
	}
	out.ExhaustedFor = exhaustedFor
	out.Cancel = cancel
	return out, nil
}

// expiredClaim is a started node whose claim lapsed with its charge window
// open, the token that held it, and when the lease that stopped being renewed
// ran out.
type expiredClaim struct {
	runID, nodeID, tokenPrefix string
	leaseNS                    int64
}

// safety: the holder stopped renewing, so the node ran until its lease ran
// out and no later; the interval since the last charge is billed to there,
// under the same per-charge cap a heartbeat is held to, before the claim that
// anchors it is cleared.
func (s *Store) settleExpiredClaimTx(ctx context.Context, tx *storeTx, claim expiredClaim, nowNS int64) error {
	_, err := s.chargeNodeTx(ctx, tx, claim.runID, claim.nodeID, claim.tokenPrefix,
		time.Unix(0, min(claim.leaseNS, nowNS)), true)
	return err
}

// safety: the team travels in this struct rather than as a Team parameter,
// because the statements around it also read and write `nodes`, whose rows do
// not carry a usable team yet; a parameter would make the scope guard demand a
// predicate on a column every node row still defaults.
type chargeWindow struct {
	Team                                  Team
	RunID, NodeID, TokenPrefix, Principal string
	Anchor, NowNS                         int64
	Rate, Class, MaxCharge                int64
	Final                                 bool
}

// refundClaimTx returns everything the node's current claim billed: its
// reservation and any usage charged since. It serves a claim whose machine
// never started and a setup the platform failed, which are the two ways a
// customer pays nothing for a node.
//
// safety: the refund carries the reservation's terms, and the claim is
// bounded by its reservation row, so an earlier attempt of the same node
// keeps what it paid.
func refundClaimTx(
	ctx context.Context, tx *storeTx, team Team, runID, nodeID string, nowNS int64,
) (*CreditCharge, error) {
	var tokenPrefix, principal string
	var class, rate, reservedAt int64
	err := tx.QueryRowContext(ctx,
		`SELECT token_prefix, principal, cpu_class, rate_micro_per_second, charged_at
		  FROM credit_charges
		 WHERE team = ? AND run_id = ? AND node_id = ? AND kind = ?
		 ORDER BY charged_at DESC, id DESC LIMIT 1`,
		string(team), runID, nodeID, CreditChargeReservation).Scan(
		&tokenPrefix, &principal, &class, &rate, &reservedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var seconds, amount sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT SUM(seconds), SUM(amount_micro) FROM credit_charges
		  WHERE team = ? AND run_id = ? AND node_id = ? AND charged_at >= ?`,
		string(team), runID, nodeID, reservedAt).Scan(&seconds, &amount); err != nil {
		return nil, err
	}
	if amount.Int64 <= 0 {
		return nil, nil
	}
	return insertCreditChargeTx(ctx, tx, chargeWindow{
		Team: team, RunID: runID, NodeID: nodeID, TokenPrefix: tokenPrefix, Principal: principal,
		NowNS: nowNS, Rate: rate, Class: class,
	}, CreditChargeRefund, -seconds.Int64, -amount.Int64)
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
	// safety: the reservation paid through the anchor and is the minimum, so a
	// finish inside it releases the window and returns nothing.
	if w.NowNS <= w.Anchor {
		if !w.Final {
			return nil, 0, w.Anchor, nil
		}
		return nil, 0, 0, nil
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
		string(w.Team), id, w.RunID, w.NodeID, w.TokenPrefix, w.Principal, kind, seconds, amount,
		w.Class, w.Rate, w.NowNS); err != nil {
		return nil, fmt.Errorf("credits: insert charge: %w", err)
	}
	return &CreditCharge{
		ID: id, Team: w.Team, RunID: w.RunID, NodeID: w.NodeID, TokenPrefix: w.TokenPrefix,
		Principal: w.Principal, Kind: kind, Seconds: seconds, AmountMicro: amount,
		CPUClassCores: w.Class, RateMicroPerSecond: w.Rate,
		ChargedAt: time.Unix(0, w.NowNS).UTC(),
	}, nil
}

// safety: Anchor is the instant the node is paid through, read before this
// charge advanced it. Zero means no charge has anchored the node yet, which is
// a node still inside the claim that will set one.
// safety: Team travels in this struct rather than as a Team parameter, for
// the same reason [chargeWindow] carries it: the helpers beside it update
// `nodes`, whose rows do not carry a usable team yet.
type creditExhaustion struct {
	Team                  Team
	Balance, NowNS, Grace int64
	RunID, NodeID         string
	Anchor                int64
}

func settleCreditExhaustionTx(
	ctx context.Context, tx *storeTx, e creditExhaustion,
) (time.Duration, bool, error) {
	if e.Balance > 0 {
		if err := clearTeamCreditExhaustedTx(ctx, tx, e.Team, e.NowNS); err != nil {
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
	stamped, err := stampTeamCreditExhaustedTx(ctx, tx, e.Team, e.NowNS)
	if err != nil {
		return 0, false, err
	}
	if stamped == 0 {
		stamped = e.NowNS
	}
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

// FailNodeForUnpricedClass fails a node whose cpu request is above the largest
// class the credit rate table prices, and records an event naming both sizes.
// A claim the ledger cannot price would otherwise be retried by every poller
// forever, so the run is told why instead of waiting on a node nobody may take.
func (s *Store) FailNodeForUnpricedClass(
	ctx context.Context, refusal *UnpricedCPUClassError, now time.Time,
) error {
	payload, err := json.Marshal(map[string]int64{
		"cores": refusal.Cores, "max_cores": refusal.MaxCores,
	})
	if err != nil {
		return err
	}
	if _, err := s.AppendEventOnce(ctx, refusal.RunID, refusal.NodeID,
		EventKindCreditsUnpriced, payload); err != nil {
		return err
	}
	return s.cancelMeteredNode(ctx, refusal.RunID, refusal.NodeID, "",
		FailureUnpricedCPUClass, refusal.Error(), now)
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
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := lockExecutorEligibilityTx(ctx, tx, false); err != nil {
		return err
	}
	settlement, err := s.chargeNodeTx(ctx, tx, runID, nodeID, tokenPrefix, now, true)
	if err != nil {
		return err
	}
	stoppedAt := settlement.settledAt.UnixNano()
	if _, err := tx.ExecContext(ctx, `UPDATE nodes
   SET `+nodeFailSet+`, error = ?, failure_reason = ?, finished_at = ?,
       claimed_by = NULL, claim_principal = '', claim_token_prefix = '',
       claim_executor = '', claim_cores = 0, claim_memory_bytes = 0,
       claim_reservation = '', claim_slot = -1, lease_expires_at = NULL,
       ready_at = NULL, offer_started_at = NULL, reservation_id = '',
       credit_charged_through = 0
 WHERE run_id = ? AND node_id = ? AND `+nodeNotDone,
		message, reason, stoppedAt, runID, nodeID); err != nil {
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
		stoppedAt, reason, runID, nodeID); err != nil {
		return err
	}
	return tx.Commit()
}

func newCreditID(prefix string) (string, error) {
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(suffix[:]), nil
}

// FormatCredits renders micro-credits as credits with two decimal places.
// Every class bills whole credits a second, so the decimals show only on a
// storage charge or a rate an operator set off the ladder.
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
