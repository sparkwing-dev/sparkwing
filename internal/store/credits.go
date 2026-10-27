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

	// SliceSeconds is the metered time paid for ahead: admission reserves the first slice and every lease renewal
	// the next, and a metered lease ends where that payment does. A refusal grants a drain of at most one slice, so
	// work runs unpaid for at most one slice per job.
	SliceSeconds = 60

	// MaxCreditGrantMicro caps the size of one grant at ten million dollars.
	MaxCreditGrantMicro = 1_000_000_000_000_000

	// maxCreditRateMicro is the highest price an operator may put on a cloud runner second: ten thousand dollars.
	maxCreditRateMicro = 1_000_000_000_000
)

const (
	CreditGrantFree     = "free"
	CreditGrantPaid     = "paid"
	creditGrantReversal = "reversal"
)

const (
	creditChargeReservation = "reservation"
	creditChargeSlice       = "slice"
	creditChargeUsage       = "usage"
	creditChargeRefund      = "refund"
	CreditChargeStorage     = "storage"
)

// ErrInsufficientCredits is returned when a metered runner's controller has no balance left to pay for the work it asked for.
var ErrInsufficientCredits = errors.New("insufficient credits")

// ErrCreditGrantConflict is returned when a grant names a kind and reference another grant already carries but asks for different terms.
var ErrCreditGrantConflict = errors.New("credits: a different grant already carries this reference")

// InsufficientCreditsError refuses a claim the balance cannot pay for and names both sides of the comparison, so the refusal a runner receives says how much is missing.
type InsufficientCreditsError struct {
	BalanceMicro  int64
	RequiredMicro int64
	// RunID and NodeID name the node the claim was refused for, which is in the claimant's own team; a record of the refusal belongs on that node and on no other team's.
	RunID  string
	NodeID string
	// Hold names the cause of the hold that refused the claim; empty when the balance did.
	Hold string
}

func (e *InsufficientCreditsError) Error() string {
	switch e.Hold {
	case holdCauseDispute:
		return "credits held: a payment dispute on this team is open; Cloud work stops until it is resolved; contact support"
	case HoldCauseWarning:
		return "credits held: the card issuer reported possible fraud on a payment; Cloud work stops; contact support"
	case HoldCauseOperator:
		return "credits held by Sparkwing support; Cloud work stops; contact support"
	case holdCauseRevoked:
		return "Cloud standing revoked by Sparkwing support; Cloud work stops; contact support"
	}
	return fmt.Sprintf("out of credits: balance %s, need %s; add funds or turn on automatic refill on the billing page",
		FormatCredits(e.BalanceMicro), FormatCredits(e.RequiredMicro))
}

// Unwrap reports [ErrInsufficientCredits], so a caller matches the condition with errors.Is without knowing this type.
func (e *InsufficientCreditsError) Unwrap() error { return ErrInsufficientCredits }

// safety: each of these is one price the operator sets for the whole controller, so they stay in the deployment's
// bag; a per-team rate is a discount the product does not sell.
const (
	metaKeyCreditRateMicro = "credit_rate_micro_per_second"
	metaKeyWarmCPUClass    = "warm_cpu_class_cores"
)

// CreditHistoryMaxLimit is the most rows of each kind one history read returns.
const CreditHistoryMaxLimit = 1000

const creditHistoryDefaultLimit = 200

// CreditGrant is one row of credit_grants: an operator grant, a paid grant
// written when a payment settles, or a reversal of either.
type CreditGrant struct {
	ID string
	// Team is whose balance the grant pays for.
	Team        Team
	Kind        string
	AmountMicro int64
	Reference   string
	// Reverses names the reference of the grant a reversal takes back, and is empty on every other kind.
	Reverses string
	// ReversesKind distinguishes corrections of free grants from payment refunds.
	ReversesKind string
	CreatedBy    string
	CreatedAt    time.Time
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
	// StorageRateMicroPerGBDay is what one gibibyte kept for one day costs, and zero is an installation that bills no storage.
	StorageRateMicroPerGBDay int64
	// StorageFreeAllowanceBytes is how many retained bytes a team keeps without being billed for them.
	StorageFreeAllowanceBytes int64
	BurnWindow                time.Duration
	BurnMicro                 int64
}

// CreditGrantRequest carries an operator free grant or its correction and the idempotency reference.
type CreditGrantRequest struct {
	Kind        string
	AmountMicro int64
	Reference   string
	Reverses    string
	CreatedBy   string
	// Reason is operator context and is excluded from the retained audit fact.
	Reason string
}

// CreditGrantResult is the row the ledger holds for a request together with whether this call wrote it.
type CreditGrantResult struct {
	Grant   CreditGrant
	Created bool
}

func (req CreditGrantRequest) validate() error {
	if strings.TrimSpace(req.Reference) == "" {
		return fmt.Errorf("%w: a grant requires an idempotency reference", ErrInvalidInput)
	}
	switch {
	case req.Kind == CreditGrantFree && req.Reverses == "" && req.AmountMicro > 0 && req.AmountMicro <= MaxCreditGrantMicro:
	case req.Kind == creditGrantReversal && req.Reverses != "" && req.AmountMicro < 0 && req.AmountMicro >= -MaxCreditGrantMicro:
	default:
		return fmt.Errorf("%w: grant a positive free amount or reverse an existing free grant with a negative amount", ErrInvalidInput)
	}
	return nil
}

// RecordCreditGrant writes a free grant or a bounded negative correction of a free grant and reports whether it was created.
func (t *Tenant) RecordCreditGrant(
	ctx context.Context, req CreditGrantRequest,
) (CreditGrantResult, error) {
	return t.s.recordCreditGrant(ctx, t.team, req)
}

func (s *Store) recordCreditGrant(
	ctx context.Context, team Team, req CreditGrantRequest,
) (_ CreditGrantResult, err error) {
	var out CreditGrantResult
	err = s.inTx(ctx, func(tx *storeTx) error {
		var err error
		out, err = recordCreditGrantTx(ctx, tx, team, req, auditAccountActor(req.CreatedBy))
		return err
	})
	if err != nil {
		return CreditGrantResult{}, err
	}
	return out, nil
}

func recordCreditGrantTx(ctx context.Context, tx *storeTx, team Team, req CreditGrantRequest, actor auditActor) (_ CreditGrantResult, err error) {
	if err := lockPaymentTeamTx(ctx, tx, team); err != nil {
		return CreditGrantResult{}, err
	}
	if err := req.validate(); err != nil {
		return CreditGrantResult{}, err
	}
	id, err := newCreditID("grant")
	if err != nil {
		return CreditGrantResult{}, err
	}
	grant := CreditGrant{
		ID: id, Team: team, Kind: req.Kind, AmountMicro: req.AmountMicro,
		Reference: req.Reference, Reverses: req.Reverses,
		CreatedBy: req.CreatedBy, CreatedAt: tx.now(ctx), ReversesKind: CreditGrantFree,
	}
	// safety: the lock is what makes the reference check and the insert one
	// step, so two deliveries of the same grant cannot both find nothing.
	l, err := lockLedgerTx(ctx, tx, team)
	if err != nil {
		return CreditGrantResult{}, err
	}
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
	if req.Kind == creditGrantReversal {
		original, found, err := creditGrantByReferenceTx(ctx, tx, team, CreditGrantFree, req.Reverses)
		if err != nil {
			return CreditGrantResult{}, err
		}
		if !found {
			return CreditGrantResult{}, fmt.Errorf("%w: reversal must name a free grant", ErrInvalidInput)
		}
		reversed, err := reversedMicroTx(ctx, tx, team, CreditGrantFree, req.Reverses)
		if err != nil {
			return CreditGrantResult{}, err
		}
		if -req.AmountMicro > original.AmountMicro-reversed {
			return CreditGrantResult{}, fmt.Errorf("%w: reversal exceeds the remaining free grant", ErrInvalidInput)
		}
	}
	if err := l.insertGrant(ctx, grant); err != nil {
		return CreditGrantResult{}, err
	}
	if err := recordGrantEventTx(ctx, tx, grant, actor, auditGrant{}); err != nil {
		return CreditGrantResult{}, err
	}
	return CreditGrantResult{Grant: grant, Created: true}, nil
}

// safety: a paid grant is the checkout's payment arriving, so it is recorded
// as the checkout paid rather than as a grant.
func recordGrantEventTx(ctx context.Context, tx *storeTx, g CreditGrant, actor auditActor, payload auditGrant) error {
	payload.GrantID, payload.Kind, payload.AmountMicro = g.ID, auditGrantKind(g.Kind), g.AmountMicro
	payload.Reference, payload.Reverses = digestAudit(g.Reference), digestAudit(g.Reverses)
	return tx.operator.recordAudit(ctx, tx, auditContext{At: g.CreatedAt, Team: g.Team, Actor: actor}, payload)
}

// safety: operator references are scoped by team and kind. Changed terms under the same key must fail
// rather than hide a mistaken grant or correction.
func sameGrantTerms(stored CreditGrant, team Team, req CreditGrantRequest) error {
	if stored.Team != team {
		return fmt.Errorf("%w: %q belongs to team %s, not %s",
			ErrCreditGrantConflict, req.Reference, stored.Team, team)
	}
	if stored.AmountMicro != req.AmountMicro {
		return fmt.Errorf("%w: %q holds %d micro-credits, not %d",
			ErrCreditGrantConflict, req.Reference, stored.AmountMicro, req.AmountMicro)
	}
	if stored.Reverses != req.Reverses || req.Kind == creditGrantReversal && stored.ReversesKind != CreditGrantFree {
		return fmt.Errorf("%w: %q reverses %q, not %q",
			ErrCreditGrantConflict, req.Reference, stored.Reverses, req.Reverses)
	}
	return nil
}

func reversedMicroTx(ctx context.Context, tx *storeTx, team Team, kind, payment string) (int64, error) {
	mode, err := creditPaymentMode(ctx, tx)
	if err != nil {
		return 0, err
	}
	var sum sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT SUM(amount_micro) FROM credit_grants
	  WHERE team = ? AND payment_mode = ? AND kind = ? AND reverses_kind = ? AND reverses = ?`, string(team), mode, creditGrantReversal, kind, payment).Scan(&sum)
	return -sum.Int64, err
}

func creditGrantByReferenceTx(
	ctx context.Context, tx *storeTx, team Team, kind, reference string,
) (CreditGrant, bool, error) {
	mode, err := creditPaymentMode(ctx, tx)
	if err != nil {
		return CreditGrant{}, false, err
	}
	var g CreditGrant
	var created int64
	var owner string
	err = tx.QueryRowContext(ctx, `SELECT team, id, kind, amount_micro, reference, reverses, reverses_kind, created_by, created_at
	  FROM credit_grants WHERE team = ? AND payment_mode = ? AND kind = ? AND reference = ?
	  ORDER BY created_at ASC, id ASC LIMIT 1`, string(team), mode, kind, reference).
		Scan(&owner, &g.ID, &g.Kind, &g.AmountMicro, &g.Reference, &g.Reverses, &g.ReversesKind, &g.CreatedBy, &created)
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

const creditBalanceSQL = `SELECT (SELECT SUM(amount_micro) FROM credit_grants WHERE team = ? AND payment_mode = ?),
        (SELECT SUM(amount_micro) FROM credit_charges WHERE team = ? AND payment_mode = ?)`

// safety: a reversal is a grant row with a negative amount, so a reader that
// wants the two apart asks for them apart; the balance is the same either way.
const creditGrantSplitSQL = `SELECT
        (SELECT SUM(amount_micro) FROM credit_grants WHERE team = ? AND payment_mode = ? AND kind != ?),
        (SELECT -SUM(amount_micro) FROM credit_grants WHERE team = ? AND payment_mode = ? AND kind = ?),
        (SELECT SUM(amount_micro) FROM credit_charges WHERE team = ? AND payment_mode = ?)`

func creditBalanceTx(ctx context.Context, tx rowQuerier, team Team) (int64, error) {
	mode, err := creditPaymentMode(ctx, tx)
	if err != nil {
		return 0, err
	}
	var granted, charged sql.NullInt64
	if err := tx.QueryRowContext(ctx, creditBalanceSQL,
		string(team), mode, string(team), mode).Scan(&granted, &charged); err != nil {
		return 0, err
	}
	return granted.Int64 - charged.Int64, nil
}

// CreditLedgerTotals is the ledger summed the way a meter reads it: what was granted, split by whether the operator paid for it, and what the charge rows did with it.
type CreditLedgerTotals struct {
	// BalanceMicro is what is left to spend.
	BalanceMicro int64
	// GrantedFreeMicro and GrantedPaidMicro sum the grants of each kind.
	GrantedFreeMicro int64
	GrantedPaidMicro int64
	// ReversedMicro is what reversals took back, reported as a positive amount.
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
		u.StorageRateMicroPerGBDay == nil && u.StorageFreeAllowanceBytes == nil {
		return fmt.Errorf(
			"%w: name at least one of the rate, the warm cpu class, "+
				"the storage rate, or the free storage allowance",
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
	if micro <= 0 || micro > maxCreditRateMicro {
		return fmt.Errorf("%w: the rate must be between 1 and %d micro-credits per second, got %d",
			ErrInvalidCreditSetting, int64(maxCreditRateMicro), micro)
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

func creditSettingsTx(ctx context.Context, tx *storeTx) (CreditSettings, error) {
	var out CreditSettings
	rate, err := creditSettingTx(ctx, tx, metaKeyCreditRateMicro, DefaultCreditRateMicro)
	if err != nil {
		return out, err
	}
	warm, err := creditSettingTx(ctx, tx, metaKeyWarmCPUClass, DefaultWarmCPUClassCores)
	if err != nil {
		return out, err
	}
	storageRate, err := storageRateTx(ctx, tx)
	if err != nil {
		return out, err
	}
	storageFree, err := storageFreeAllowanceTx(ctx, tx)
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

func creditLimit(limit int) int {
	if limit <= 0 {
		return creditHistoryDefaultLimit
	}
	if limit > CreditHistoryMaxLimit {
		return CreditHistoryMaxLimit
	}
	return limit
}

// DisableCreditMetering keeps trigger settlement and storage writes out of
// the credit ledger and the free-tier allowance for the life of the store.
// A deployment without metering calls it once while it is composed; there
// is no call that turns metering back on.
func (s *Store) DisableCreditMetering() { s.creditMeteringOff.Store(true) }

// EnableCloudStorage applies Sparkwing Cloud's storage defaults: a team without an explicit quota or a deployment
// default tier gets the free storage tier, or the paid tier once it has bought credits, and stored bytes are priced
// at CloudStorageRateMicroPerGBDay unless the operator set a rate. Sparkwing Cloud calls it once while it is composed.
func (s *Store) EnableCloudStorage() { s.cloudStorage.Store(true) }

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
