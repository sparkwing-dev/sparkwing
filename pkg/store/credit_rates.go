package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
)

// Cloud runner seconds are priced by cpu class. A class is a whole number of
// cores with a price, and a node is billed at the smallest class that covers
// its resolved cpu request, the way GitHub Actions prices a 2-core or an
// 8-core runner rather than the cpu a job happened to use.
const (
	// CreditRateBaseClassCores is the class credit_rate_micro_per_second
	// prices. That single setting predates the table and every binary reads
	// it, so it holds the price a four-core node pays and the two settings
	// move together.
	CreditRateBaseClassCores = 4

	// MaxCreditRateTableEntries bounds the table an operator may store, which
	// keeps the settings row small enough to read on every claim.
	MaxCreditRateTableEntries = 32
)

// safety: these are GitHub Actions' Linux x64 rates carried to the second,
// which is the comparison every customer makes. The four-core entry is the
// single rate setting under another name, so it follows that setting.
var defaultCreditRates = []CreditRate{
	{Cores: 2, MicroPerSecond: 10_000},
	{Cores: CreditRateBaseClassCores, MicroPerSecond: DefaultCreditRateMicro},
	{Cores: 8, MicroPerSecond: 36_667},
	{Cores: 16, MicroPerSecond: 70_000},
	{Cores: 32, MicroPerSecond: 136_667},
	{Cores: 64, MicroPerSecond: 270_000},
}

const metaKeyCreditRateTable = "credit_rate_table"

// CreditRate prices one cloud runner second for one cpu class.
type CreditRate struct {
	Cores          int64 `json:"cores"`
	MicroPerSecond int64 `json:"micro_per_second"`
}

// CreditRateTable prices every cpu class a cluster offers, ordered from the
// smallest class to the largest. A node is billed at the first class whose
// cores cover its request, so a table names the sizes on offer rather than
// every core count a node might ask for.
type CreditRateTable []CreditRate

// ErrUnpricedCPUClass reports a cpu request larger than the biggest class the
// rate table prices. The claim path returns an [UnpricedCPUClassError], which
// wraps it and names both sides.
var ErrUnpricedCPUClass = errors.New("credits: cpu request above the largest priced class")

// UnpricedCPUClassError refuses a claim for a node asking for more cores than
// the rate table prices, and names the request and the largest class, so the
// operator knows which class to add.
type UnpricedCPUClassError struct {
	RunID    string
	NodeID   string
	Cores    int64
	MaxCores int64
}

func (e *UnpricedCPUClassError) Error() string {
	return fmt.Sprintf(
		"credits: this node asks for %d cpu cores and the largest priced class is %d; "+
			"add a class for it to the credit rate table", e.Cores, e.MaxCores)
}

// Unwrap reports [ErrUnpricedCPUClass], so a caller matches the condition with
// errors.Is without knowing this type.
func (e *UnpricedCPUClassError) Unwrap() error { return ErrUnpricedCPUClass }

// ClassFor returns the class that prices a node asking for cores, which is the
// smallest class whose cores cover the request rounded up to a whole core. It
// returns an [UnpricedCPUClassError] when the request is above every class.
func (t CreditRateTable) ClassFor(cores float64) (CreditRate, error) {
	if len(t) == 0 {
		return CreditRate{}, fmt.Errorf("%w: the rate table prices no cpu class", ErrInvalidCreditSetting)
	}
	want := int64(1)
	if cores > 1 {
		want = int64(math.Ceil(cores))
	}
	for _, entry := range t {
		if entry.Cores >= want {
			return entry, nil
		}
	}
	return CreditRate{}, &UnpricedCPUClassError{Cores: want, MaxCores: t[len(t)-1].Cores}
}

// RateFor returns the micro-credits a second costs at the recorded class,
// rounding up to the next class the table still prices and falling back to the
// largest one. A node keeps running at a price when its class leaves the
// table, because a refusal belongs at the claim and not mid-run.
func (t CreditRateTable) RateFor(cores int64) int64 {
	if len(t) == 0 {
		return 0
	}
	for _, entry := range t {
		if entry.Cores >= cores {
			return entry.MicroPerSecond
		}
	}
	return t[len(t)-1].MicroPerSecond
}

// BaseRate returns the price a node of [CreditRateBaseClassCores] cores pays,
// which is what credit_rate_micro_per_second carries for binaries that read
// only that setting.
func (t CreditRateTable) BaseRate() int64 {
	return t.RateFor(CreditRateBaseClassCores)
}

// safety: only a table that prices the base class itself may write the scalar,
// which is what keeps the two settings naming one price.
func (t CreditRateTable) baseClassRate() (int64, bool) {
	for _, entry := range t {
		if entry.Cores == CreditRateBaseClassCores {
			return entry.MicroPerSecond, true
		}
	}
	return 0, false
}

// Validate reports whether every entry prices a whole number of cores above
// zero, at a rate inside the bound the single rate setting is held to, with no
// class priced twice.
func (t CreditRateTable) Validate() error {
	if len(t) == 0 {
		return fmt.Errorf("%w: the rate table must price at least one cpu class", ErrInvalidCreditSetting)
	}
	if len(t) > MaxCreditRateTableEntries {
		return fmt.Errorf("%w: the rate table prices at most %d cpu classes",
			ErrInvalidCreditSetting, MaxCreditRateTableEntries)
	}
	seen := make(map[int64]bool, len(t))
	for _, entry := range t {
		if entry.Cores <= 0 {
			return fmt.Errorf("%w: cpu class %d must be a positive number of cores",
				ErrInvalidCreditSetting, entry.Cores)
		}
		if err := validCreditRate(entry.MicroPerSecond); err != nil {
			return fmt.Errorf("the %d-core class: %w", entry.Cores, err)
		}
		if seen[entry.Cores] {
			return fmt.Errorf("%w: the %d-core class is priced twice", ErrInvalidCreditSetting, entry.Cores)
		}
		seen[entry.Cores] = true
	}
	return nil
}

// Sorted returns the table ordered from the smallest class to the largest,
// which is the order [CreditRateTable.ClassFor] reads it in.
func (t CreditRateTable) Sorted() CreditRateTable {
	out := append(CreditRateTable(nil), t...)
	sort.Slice(out, func(i, j int) bool { return out[i].Cores < out[j].Cores })
	return out
}

// DefaultCreditRateTable is the ladder an installation that never set a table
// bills: GitHub Actions' Linux x64 rates, with the four-core class priced at
// rate because the single rate setting is that class under another name.
func DefaultCreditRateTable(rate int64) CreditRateTable {
	out := make(CreditRateTable, 0, len(defaultCreditRates))
	for _, entry := range defaultCreditRates {
		if entry.Cores == CreditRateBaseClassCores {
			entry.MicroPerSecond = rate
		}
		out = append(out, entry)
	}
	return out
}

// CreditRateTable returns the price of a cloud runner second at every cpu
// class. An installation that never set a table reads
// [DefaultCreditRateTable], whose four-core class is
// credit_rate_micro_per_second.
func (s *Store) CreditRateTable(ctx context.Context) (CreditRateTable, error) {
	rate, err := s.CreditRateMicroPerSecond(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := s.creditSettingRaw(ctx, metaKeyCreditRateTable)
	if err != nil {
		return nil, err
	}
	return creditRateTable(raw, rate)
}

// CreditRateTableSet reports whether an operator has written a rate table. An
// installation that has not prices every class at the single rate, and the
// single rate is a setting it may still write.
func (s *Store) CreditRateTableSet(ctx context.Context) (bool, error) {
	raw, err := s.creditSettingRaw(ctx, metaKeyCreditRateTable)
	return raw != "", err
}

func creditRateTableTx(ctx context.Context, tx *storeTx) (CreditRateTable, error) {
	rate, err := creditSettingTx(ctx, tx, metaKeyCreditRateMicro, DefaultCreditRateMicro)
	if err != nil {
		return nil, err
	}
	raw, err := creditSettingRawTx(ctx, tx, metaKeyCreditRateTable)
	if err != nil {
		return nil, err
	}
	return creditRateTable(raw, rate)
}

// safety: a table nothing can read is refused rather than replaced by the flat
// default, because falling back would bill every class at a price the operator
// did not choose and nobody would see it happen.
func creditRateTable(raw string, rate int64) (CreditRateTable, error) {
	if raw == "" {
		return DefaultCreditRateTable(rate), nil
	}
	var stored CreditRateTable
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return nil, fmt.Errorf("credits: the stored rate table %q is not readable: %w", raw, err)
	}
	if err := stored.Validate(); err != nil {
		return nil, fmt.Errorf("credits: the stored rate table %q prices nothing usable: %w", raw, err)
	}
	out := stored.Sorted()
	// safety: the two settings name one price, so the scalar an operator or an
	// older binary set wins for the class it prices.
	for i := range out {
		if out[i].Cores == CreditRateBaseClassCores {
			out[i].MicroPerSecond = rate
		}
	}
	return out, nil
}

// SetCreditRateTable prices every cpu class the cluster offers and writes the
// four-core price to credit_rate_micro_per_second, so a binary that reads only
// that setting still bills a four-core node correctly. Charges already written
// keep the class and rate they were charged at.
func (s *Store) SetCreditRateTable(ctx context.Context, table CreditRateTable) (err error) {
	if err := table.Validate(); err != nil {
		return err
	}
	sorted := table.Sorted()
	encoded, err := json.Marshal(sorted)
	if err != nil {
		return fmt.Errorf("credits: encode the rate table: %w", err)
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := setCreditSettingTx(ctx, tx, metaKeyCreditRateTable, string(encoded)); err != nil {
		return err
	}
	// safety: the scalar is the four-core price, so a table that prices no
	// four-core class leaves it where it stands rather than restating a
	// neighbouring class under a name that does not mean that class.
	if base, ok := sorted.baseClassRate(); ok {
		if err := setCreditRateMicroTx(ctx, tx, base); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// safety: the class is resolved from the same cpu figure the scheduler sizes
// the node by, and never exceeds the cpu the runner executing it reports, so a
// ceiling that clamped the pod clamps the bill with it.
func nodeCreditClassTx(
	ctx context.Context, tx *storeTx, table CreditRateTable, runID, nodeID string,
) (CreditRate, error) {
	charge, err := nodeChargeTx(ctx, tx, runID, nodeID)
	if err != nil {
		return CreditRate{}, err
	}
	class, classErr := table.ClassFor(charge.Cores)
	reported, ok := ClaimRunnerCoresFromContext(ctx)
	if !ok || reported <= 0 {
		return class, classErr
	}
	runnerClass, runnerErr := table.ClassFor(reported)
	if runnerErr != nil {
		return class, classErr
	}
	if classErr != nil || runnerClass.Cores < class.Cores {
		return runnerClass, nil
	}
	return class, classErr
}

type claimRunnerCoresKey struct{}

// WithClaimRunnerCores records the cpu the runner taking this claim reports for
// itself, which is the pod the customer actually gets. The ledger bills the
// smaller of that class and the class the node's own cpu request resolves to,
// so a ceiling that clamped the pod clamps the bill. A claim that reports
// nothing is priced by the node's request alone.
func WithClaimRunnerCores(ctx context.Context, cores float64) context.Context {
	return context.WithValue(ctx, claimRunnerCoresKey{}, cores)
}

// ClaimRunnerCoresFromContext returns the cpu a claim reported for its runner.
func ClaimRunnerCoresFromContext(ctx context.Context) (float64, bool) {
	cores, ok := ctx.Value(claimRunnerCoresKey{}).(float64)
	return cores, ok
}
