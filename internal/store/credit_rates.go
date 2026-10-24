package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

const (
	// creditRateBaseClassCores is the class credit_rate_micro_per_second prices.
	creditRateBaseClassCores = 4

	// maxCreditRateTableEntries bounds the table an operator may store, which keeps the settings row small enough to read on every claim.
	maxCreditRateTableEntries = 32

	// DefaultWarmCPUClassCores is the class a warm runner pool serves when no operator set one.
	DefaultWarmCPUClassCores = 2
)

// CPUClassMemoryBytes returns the catalog allocation for a billed class in whole mebibytes.
func CPUClassMemoryBytes(cores int64) int64 { return match.CloudSizeMemoryBytes(cores) }

// safety: each class is priced at 90% of the GitHub Actions Linux runner of its size, so the
// ladder is a price list rather than a multiplication. A price change is made here, never to
// what a credit is. The four-core entry is the single rate setting under another name, so it
// follows that setting.
var defaultCreditRates = []CreditRate{
	{Cores: 2, MicroPerSecond: 9_000},
	{Cores: creditRateBaseClassCores, MicroPerSecond: DefaultCreditRateMicro},
	{Cores: 8, MicroPerSecond: 33_000},
}

const metaKeyCreditRateTable = "credit_rate_table"

// CreditRate prices one cloud runner second for one cpu class.
type CreditRate struct {
	Cores          int64 `json:"cores"`
	MicroPerSecond int64 `json:"micro_per_second"`
}

// CreditRateTable prices every cpu class a cluster offers, ordered from the smallest class to the largest.
type CreditRateTable []CreditRate

// ErrUnpricedCPUClass reports a size label whose class is not priced.
var ErrUnpricedCPUClass = errors.New("credits: runner size class is not priced")

// UnpricedCPUClassError refuses a claim whose exact Cloud size class has no
// price. MaxCores names the largest priced class for the operator's diagnosis.
type UnpricedCPUClassError struct {
	RunID    string
	NodeID   string
	Cores    int64
	MaxCores int64
}

func (e *UnpricedCPUClassError) Error() string {
	return fmt.Sprintf("credits: sparkwing-%dcpu has no price; add its exact class to the credit rate table (largest priced class: %d)", e.Cores, e.MaxCores)
}

// Unwrap reports [ErrUnpricedCPUClass], so a caller matches the condition with errors.Is without knowing this type.
func (e *UnpricedCPUClassError) Unwrap() error { return ErrUnpricedCPUClass }

// ClassForLabels prices the selected Cloud size's exact class.
func (t CreditRateTable) ClassForLabels(selector []string, profile match.Profile) (CreditRate, error) {
	size, err := match.CloudSizeForProfile(selector, profile)
	if err != nil {
		return CreditRate{}, fmt.Errorf("%w: %v", ErrUnpricedCPUClass, err)
	}
	for _, entry := range t {
		if entry.Cores == size.CreditClass {
			return entry, nil
		}
	}
	largest := int64(0)
	for _, entry := range t {
		largest = max(largest, entry.Cores)
	}
	return CreditRate{}, &UnpricedCPUClassError{Cores: size.CreditClass, MaxCores: largest}
}

// safety: only a table that prices the base class itself may write the scalar,
// which is what keeps the two settings naming one price.
func (t CreditRateTable) baseClassRate() (int64, bool) {
	for _, entry := range t {
		if entry.Cores == creditRateBaseClassCores {
			return entry.MicroPerSecond, true
		}
	}
	return 0, false
}

// Validate reports whether every entry prices a whole number of cores above zero, at a rate inside the bound the single rate setting is held to, with no class priced twice.
func (t CreditRateTable) Validate() error {
	if len(t) == 0 {
		return fmt.Errorf("%w: the rate table must price at least one cpu class", ErrInvalidCreditSetting)
	}
	if len(t) > maxCreditRateTableEntries {
		return fmt.Errorf("%w: the rate table prices at most %d cpu classes",
			ErrInvalidCreditSetting, maxCreditRateTableEntries)
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

// sorted returns a copy of the table ordered from the smallest class to the largest.
func (t CreditRateTable) sorted() CreditRateTable {
	out := append(CreditRateTable(nil), t...)
	sort.Slice(out, func(i, j int) bool { return out[i].Cores < out[j].Cores })
	return out
}

// DefaultCreditRateTable is the ladder an installation that never set a table bills, with the four-core class priced at rate because the single rate setting is that class under another name.
func DefaultCreditRateTable(rate int64) CreditRateTable {
	out := make(CreditRateTable, 0, len(defaultCreditRates))
	for _, entry := range defaultCreditRates {
		if entry.Cores == creditRateBaseClassCores {
			entry.MicroPerSecond = rate
		}
		out = append(out, entry)
	}
	return out
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
		return nil, fmt.Errorf("%w: stored rate table is not readable: %w", ErrInvalidCreditSetting, err)
	}
	if err := stored.Validate(); err != nil {
		return nil, fmt.Errorf("stored rate table prices nothing usable: %w", err)
	}
	return stored.sorted(), nil
}

func addCreditRateTableWrites(writes map[string]string, table CreditRateTable) error {
	if err := table.Validate(); err != nil {
		return err
	}
	sorted := table.sorted()
	encoded, err := json.Marshal(sorted)
	if err != nil {
		return fmt.Errorf("credits: encode the rate table: %w", err)
	}
	writes[metaKeyCreditRateTable] = string(encoded)
	// safety: the scalar is the four-core price, so a table that prices no
	// four-core class leaves it where it stands rather than restating a
	// neighboring class under a name that does not mean that class.
	if base, ok := sorted.baseClassRate(); ok {
		writes[metaKeyCreditRateMicro] = formatCreditSetting(base)
	}
	return nil
}
