package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	CreditsPerDollar       = 1_000
	MicroCreditsPerCredit  = 100_000
	DefaultCreditRateMicro = 18_000

	// MicroCreditsPerCent is what one cent of a payment buys, which a checkout converts by.
	MicroCreditsPerCent = MicroCreditsPerCredit * CreditsPerDollar / 100

	// DefaultCreditGraceSeconds is how long a node keeps running after the balance reaches zero before the controller cancels it.
	DefaultCreditGraceSeconds = 10 * 60

	// MinBillableSeconds is the least one metered node or trigger step pays for, on every class.
	MinBillableSeconds = 60

	// MaxCreditGrantMicro caps the size of one grant at ten billion dollars.
	MaxCreditGrantMicro = 1_000_000_000_000_000

	// DefaultCreditMaxChargeSeconds caps the seconds one charge may bill.
	DefaultCreditMaxChargeSeconds = 30

	// MaxCreditRateMicro is the highest price an operator may put on a cloud runner second: ten thousand dollars.
	MaxCreditRateMicro = 1_000_000_000_000

	// MaxCreditMaxChargeSeconds is the highest charge cap an operator may set.
	MaxCreditMaxChargeSeconds = 86_400
)

const (
	// PoolHeartbeatInterval is how often a pooled runner renews its claim.
	PoolHeartbeatInterval = 3 * time.Second

	// DispatchedHeartbeatInterval is how often a dispatched node and a warm pool runner renew theirs, which is the longest cadence any metered node renews on.
	DispatchedHeartbeatInterval = 5 * time.Second
)

// MinCreditMaxChargeSeconds is the lowest charge cap an operator may set: one second past the longest heartbeat cadence.
const MinCreditMaxChargeSeconds = int64(DispatchedHeartbeatInterval/time.Second) + 1

const (
	CreditGrantFree     = "free"
	CreditGrantPaid     = "paid"
	CreditGrantReversal = "reversal"
)

const (
	CreditChargeReservation = "reservation"
	CreditChargeUsage       = "usage"
	CreditChargeRefund      = "refund"
	CreditChargeStorage     = "storage"
)

const (
	// EventKindCreditsBlocked records a claim refused for an empty balance, so a run waiting on a metered runner says why.
	EventKindCreditsBlocked = "credits_blocked"
	// EventKindCreditsExhausted records a node cancelled after the balance reached zero and the grace period elapsed.
	EventKindCreditsExhausted = "credits_exhausted"
)

// ErrInsufficientCredits is returned when a metered runner's controller has no balance left to pay for the work it asked for.
var ErrInsufficientCredits = errors.New("insufficient credits")

// ErrReversalExceedsPayment is returned when a reversal would take back more than the payment it names paid, counting the reversals already written.
var ErrReversalExceedsPayment = errors.New("credits: the reversal exceeds what the payment paid")

// ErrCreditGrantConflict is returned when a grant names a kind and reference another grant already carries but asks for different terms.
var ErrCreditGrantConflict = errors.New("credits: a different grant already carries this reference")

// InsufficientCreditsError refuses a claim the balance cannot pay for and names both sides of the comparison, so the refusal a runner receives says how much is missing.
type InsufficientCreditsError struct {
	BalanceMicro  int64
	RequiredMicro int64
	// RunID and NodeID name the node the claim was refused for, which is in the claimant's own team; a record of the refusal belongs on that node and on no other team's.
	RunID  string
	NodeID string
	// Frozen is set when the team's cloud usage is held while a payment dispute is open, whatever the balance reads.
	Frozen bool
	// Limit names the spend limit that refused the claim, one of the SpendLimit values; empty is the balance.
	Limit string
}

func (e *InsufficientCreditsError) Error() string {
	if e.Limit == SpendLimitChargeFailed {
		return "spend limit: the charge to this team's card failed; an owner can pay it or update the card on the billing page"
	}
	if e.Frozen {
		return "credits frozen: this team's cloud usage is held while a payment dispute is open; contact support"
	}
	switch e.Limit {
	case SpendLimitChargePending:
		return "Cloud work waits while the pending card charge completes"
	case SpendLimitBudget:
		return "spend limit: this team reached the budget its owner set; an owner can raise it on the billing page"
	}
	return fmt.Sprintf("insufficient credits: balance %s, need %s",
		FormatCredits(e.BalanceMicro), FormatCredits(e.RequiredMicro))
}

// Unwrap reports [ErrInsufficientCredits], so a caller matches the condition with errors.Is without knowing this type.
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
)

// CreditHistoryMaxLimit is the most rows of each kind one history read returns.
const CreditHistoryMaxLimit = 1000

const creditHistoryDefaultLimit = 200

// CreditGrant is one row of credit_grants: credits the operator added.
type CreditGrant struct {
	ID string
	// Team is whose balance the grant pays for.
	Team        Team
	Kind        string
	AmountMicro int64
	Reference   string
	// Reverses names the reference of the paid grant a reversal takes back, and is empty on every other kind.
	Reverses  string
	CreatedBy string
	CreatedAt time.Time
}

// CreditCharge is one persisted reservation, usage debit or refund.
type CreditCharge struct {
	ID string
	// Team is whose balance the row drew on.
	Team        Team
	RunID       string
	NodeID      string
	TokenPrefix string
	// Principal names the team whose storage or runner work this row billed.
	Principal   string
	Kind        string
	Seconds     int64
	AmountMicro int64
	// StorageBytes is the retained bytes above the free allowance a storage row billed, and zero on every other kind.
	StorageBytes int64
	// CPUClassCores is the compute class, or zero for storage.
	CPUClassCores int64
	// RateMicroPerSecond is the immutable price of this charge.
	RateMicroPerSecond int64
	ChargedAt          time.Time
}

// CreditState is the ledger as an operator reads it: what was granted, what was spent, what is left, the price of a cloud runner second, and the burn over a recent window.
type CreditState struct {
	GrantedMicro int64
	// ReversedMicro is what reversals took back, reported as a positive amount and already subtracted from the balance.
	ReversedMicro int64
	// ChargedMicro is every charge row, reservations, refunds and storage included, so that granted less reversed less charged is the balance.
	ChargedMicro int64
	// StorageChargedMicro is the part of ChargedMicro that billed retained bytes rather than runner time.
	StorageChargedMicro int64
	BalanceMicro        int64
	RateMicroPerSecond  int64
	RateTable           CreditRateTable
	// RateTableSet reports whether an operator wrote the table.
	RateTableSet bool
	// WarmCPUClassCores is the largest class a warm runner pool serves.
	WarmCPUClassCores int64
	GraceSeconds      int64
	MaxChargeSeconds  int64
	// StorageRateMicroPerGBDay is what one gibibyte kept for one day costs, and zero is an installation that bills no storage.
	StorageRateMicroPerGBDay int64
	// StorageFreeAllowanceBytes is how many retained bytes a team keeps without being billed for them.
	StorageFreeAllowanceBytes int64
	BurnWindow                time.Duration
	BurnMicro                 int64
	ExhaustedAt               *time.Time
}

// ValidCreditGrantKind reports whether kind is one this ledger stores.
func ValidCreditGrantKind(kind string) bool {
	return kind == CreditGrantFree || kind == CreditGrantPaid || kind == CreditGrantReversal
}

// CreditGrantRequest carries a free grant or a negative, payment-linked reversal with its idempotency reference.
type CreditGrantRequest struct {
	Kind        string
	AmountMicro int64
	Reference   string
	Reverses    string
	CreatedBy   string
	// Reason is why an operator made the grant, recorded on its business event.
	Reason string
}

// CreditGrantResult is the row the ledger holds for a request together with whether this call wrote it.
type CreditGrantResult struct {
	Grant   CreditGrant
	Created bool
}

func (req CreditGrantRequest) validate() error {
	if req.Kind == CreditGrantPaid {
		return fmt.Errorf("%w: paid credit requires verified checkout settlement", ErrInvalidInput)
	}
	if !ValidCreditGrantKind(req.Kind) {
		return fmt.Errorf("credits: unknown grant kind %q", req.Kind)
	}
	if strings.HasPrefix(req.Reference, unappliedPrefix) {
		return fmt.Errorf("%w: the reference prefix %q is reserved", ErrInvalidInput, unappliedPrefix)
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

// RecordCreditGrant writes a free grant or payment-linked reversal and reports whether it was created.
func (t *Tenant) RecordCreditGrant(
	ctx context.Context, req CreditGrantRequest,
) (CreditGrantResult, error) {
	return t.s.recordCreditGrant(ctx, t.team, req)
}

func (s *Store) recordCreditGrant(
	ctx context.Context, team Team, req CreditGrantRequest,
) (_ CreditGrantResult, err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return CreditGrantResult{}, err
	}
	defer rollbackOrLog(tx)
	out, err := recordCreditGrantTx(ctx, tx, team, req)
	if err != nil {
		return CreditGrantResult{}, err
	}
	return out, tx.Commit()
}

func recordCreditGrantTx(ctx context.Context, tx *storeTx, team Team, req CreditGrantRequest) (_ CreditGrantResult, err error) {
	if err := req.validate(); err != nil {
		return CreditGrantResult{}, err
	}
	id, err := newCreditID("grant")
	if err != nil {
		return CreditGrantResult{}, err
	}
	now := tx.now()
	grant := CreditGrant{
		ID: id, Team: team, Kind: req.Kind, AmountMicro: req.AmountMicro,
		Reference: req.Reference, Reverses: req.Reverses,
		CreatedBy: req.CreatedBy, CreatedAt: now,
	}

	// safety: the lock is what makes the reference check and the insert one
	// step, so two deliveries of the same payment cannot both find nothing.
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return CreditGrantResult{}, err
	}
	if req.Reference != "" {
		existing, found, err := creditGrantByReferenceTx(ctx, tx, team, req.Kind, req.Reference)
		if err != nil {
			return CreditGrantResult{}, err
		}
		if found {
			if err := sameGrantTerms(existing, team, req); err != nil {
				return CreditGrantResult{}, err
			}
			return CreditGrantResult{Grant: existing}, nil
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
	if _, err := tx.ExecContext(ctx, `
        INSERT INTO credit_grants (team, id, kind, amount_micro, reference, reverses, created_by, created_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		string(team), id, req.Kind, req.AmountMicro, req.Reference, req.Reverses,
		req.CreatedBy, now.UnixNano()); err != nil {
		return CreditGrantResult{}, fmt.Errorf("credits: insert grant: %w", err)
	}
	if err := recordGrantEventTx(tx, grant, "", req.Reason); err != nil {
		return CreditGrantResult{}, err
	}
	room, err := headroomTx(ctx, tx, team, tx.now())
	if err != nil {
		return CreditGrantResult{}, err
	}
	if room > 0 {
		if err := clearTeamCreditExhaustedTx(ctx, tx, team, now.UnixNano()); err != nil {
			return CreditGrantResult{}, err
		}
	}
	return CreditGrantResult{Grant: grant, Created: true}, nil
}

// safety: a paid grant is the checkout's payment arriving, so it is recorded
// as the checkout paid rather than as a grant.
func recordGrantEventTx(tx *storeTx, g CreditGrant, checkout, reason string) error {
	kind := BusinessEventCreditGranted
	switch g.Kind {
	case CreditGrantPaid:
		kind = BusinessEventCheckoutPaid
	case CreditGrantReversal:
		kind = BusinessEventCreditReversed
	}
	attrs := map[string]any{"grant_kind": g.Kind, "amount_micro": g.AmountMicro, "reference": g.Reference}
	if g.Reverses != "" {
		attrs["reverses"] = g.Reverses
	}
	if checkout != "" {
		attrs["session_id"] = checkout
	}
	if reason != "" {
		attrs["reason"] = reason
	}
	return RecordBusinessEvent(tx, BusinessEvent{
		At: g.CreatedAt, Team: g.Team, Kind: kind, SubjectID: g.ID, Actor: g.CreatedBy, Attrs: attrs,
	})
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

func reversedMicroTx(ctx context.Context, tx *storeTx, team Team, payment string) (int64, error) {
	var sum sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT SUM(amount_micro) FROM credit_grants
	  WHERE team = ? AND kind = ? AND reverses = ?`, string(team), CreditGrantReversal, payment).Scan(&sum)
	return -sum.Int64, err
}

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

const creditBalanceSQL = `SELECT (SELECT SUM(amount_micro) FROM credit_grants WHERE team = ?),
        (SELECT SUM(amount_micro) FROM credit_charges WHERE team = ?)`

// safety: a reversal is a grant row with a negative amount, so a reader that
// wants the two apart asks for them apart; the balance is the same either way.
const creditGrantSplitSQL = `SELECT
        (SELECT SUM(amount_micro) FROM credit_grants WHERE team = ? AND kind != ?),
        (SELECT -SUM(amount_micro) FROM credit_grants WHERE team = ? AND kind = ?),
        (SELECT SUM(amount_micro) FROM credit_charges WHERE team = ?)`

func creditBalanceTx(ctx context.Context, tx rowQuerier, team Team) (int64, error) {
	var granted, charged sql.NullInt64
	if err := tx.QueryRowContext(ctx, creditBalanceSQL,
		string(team), string(team)).Scan(&granted, &charged); err != nil {
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

// CreditLedgerTotals is the ledger summed the way a meter reads it: what was granted, split by whether the operator paid for it, and what the charge rows did with it.
type CreditLedgerTotals struct {
	// BalanceMicro is what is left to spend.
	BalanceMicro int64
	// GrantedFreeMicro and GrantedPaidMicro sum the grants of each kind.
	GrantedFreeMicro int64
	GrantedPaidMicro int64
	// ReversedMicro is what refunds took back out of the paid grants, reported as a positive amount.
	ReversedMicro int64
	// ReservedMicro is the runway claims took up front, refunds included, so it is the gross reservation rather than the part never returned.
	ReservedMicro int64
	// ChargedMicro is what runner execution billed, and storage is not in it; StorageMicro carries that.
	ChargedMicro int64
	// RefundedMicro is the unused reservation tail returned at finish, reported as a positive amount.
	RefundedMicro int64
	// StorageMicro is what retained bytes billed, which is the part of the ledger's spend that bought no runner time.
	StorageMicro int64
	// SettledSeconds excludes the refundable part of unsettled awards.
	SettledSeconds int64
}

// CreditSettings are the values the ledger prices work with.
type CreditSettings struct {
	// RateMicroPerSecond is the price of one cloud runner second.
	RateMicroPerSecond int64
	// RateTable prices every cpu class.
	RateTable    CreditRateTable
	RateTableSet bool
	// WarmCPUClassCores is the largest cpu class a warm runner pool serves.
	WarmCPUClassCores int64
	// GraceSeconds is how long a node that has consumed its claim reservation on an empty balance keeps running before the controller cancels it.
	GraceSeconds int64
	// MaxChargeSeconds is the most seconds any one charge may bill.
	MaxChargeSeconds int64
	// StorageRateMicroPerGBDay is what one gibibyte kept for one day costs.
	StorageRateMicroPerGBDay int64
	// StorageFreeAllowanceBytes is how many retained bytes a team keeps without being billed for them.
	StorageFreeAllowanceBytes int64
}

// CreditSettingsUpdate names the settings to change.
type CreditSettingsUpdate struct {
	RateMicroPerSecond        *int64
	RateTable                 *CreditRateTable
	WarmCPUClassCores         *int64
	GraceSeconds              *int64
	MaxChargeSeconds          *int64
	StorageRateMicroPerGBDay  *int64
	StorageFreeAllowanceBytes *int64
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
	if err := advisoryLockTx(ctx, tx, "sparkwing/credit-settings"); err != nil {
		rollbackOrLog(tx)
		return nil, err
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
		if err := setCreditSettingTx(ctx, tx, key, writes[key]); err != nil {
			return CreditSettings{}, err
		}
	}
	return creditSettingsTx(ctx, tx)
}

// ErrInvalidCreditSetting is the class of every refusal SetCreditSettings returns, so a caller answers a bad value differently from a database failure.
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

func formatCreditSetting(v int64) string { return strconv.FormatInt(v, 10) }

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

func creditLimit(limit int) int {
	if limit <= 0 {
		return creditHistoryDefaultLimit
	}
	if limit > CreditHistoryMaxLimit {
		return CreditHistoryMaxLimit
	}
	return limit
}

type creditMeteringDisabledKey struct{}

// WithoutCreditMetering keeps claims, trigger settlement and storage writes out of the credit ledger while preserving token-bound claim ownership.
func WithoutCreditMetering(ctx context.Context) context.Context {
	return context.WithValue(ctx, creditMeteringDisabledKey{}, true)
}

func creditMeteringDisabled(ctx context.Context) bool {
	return ctx.Value(creditMeteringDisabledKey{}) == true
}

func refuseFrozenTeamTx(
	ctx context.Context, tx *storeTx, team Team, balance, required int64, runID, nodeID string,
) error {
	freeze, err := teamCreditFreezeTx(ctx, tx, team)
	if err != nil || !freeze.Frozen {
		return err
	}
	out := &InsufficientCreditsError{
		BalanceMicro: balance, RequiredMicro: required, RunID: runID, NodeID: nodeID, Frozen: true,
	}
	// safety: a declined charge's hold is the owner's to lift by paying, so
	// its refusal says so instead of pointing them at support.
	if blocking, err := rowPresentTx(ctx, tx, blockingHoldSQL, string(team), FreezeCauseDecline); err != nil {
		return err
	} else if !blocking {
		out.Limit = SpendLimitChargeFailed
	}
	return out
}

// AppendEventOnce writes a tenant run event unless that run or job already has its kind.
func (t *Tenant) AppendEventOnce(ctx context.Context, runID, nodeID, kind string, payload []byte) (_ bool, err error) {
	// safety: the common call finds the event already there, so the read comes
	// before the transaction rather than inside one opened twice a second.
	present, err := t.eventKindPresent(ctx, runID, nodeID, kind)
	if err != nil || present {
		return false, err
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := assertRunBelongsToTeamTx(ctx, tx, t.team, runID); err != nil {
		return false, err
	}
	var existing int
	err = tx.QueryRowContext(ctx,
		`SELECT 1 FROM events WHERE team = ? AND run_id = ? AND node_id = ? AND kind = ? LIMIT 1`,
		string(t.team), runID, nodeID, kind).Scan(&existing)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if _, err := appendEventTx(ctx, tx, t.team, runID, nodeID, kind, payload, time.Now()); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (t *Tenant) eventKindPresent(ctx context.Context, runID, nodeID, kind string) (bool, error) {
	var existing int
	err := t.s.queryRow(ctx,
		`SELECT 1 FROM events WHERE team = ? AND run_id = ? AND node_id = ? AND kind = ? LIMIT 1`,
		string(t.team), runID, nodeID, kind).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func newCreditID(prefix string) (string, error) {
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(suffix[:]), nil
}

// FormatCredits renders micro-credits as credits with two decimal places, truncated toward zero so a balance never reads larger than it holds.
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
