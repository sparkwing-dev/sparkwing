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

	// safety: the memory a cpu class carries for each of its cores, matching
	// the hosted runners a customer compares against. A node asking for more
	// takes the class whose memory covers it.
	cpuClassMemoryBytesPerCore = 4 << 30

	// DefaultWarmCPUClassCores is the class a warm runner pool serves when no
	// operator set one. A node above it is executed on a node of its own.
	DefaultWarmCPUClassCores = 2
)

// CPUClass is one rung of the runner ladder: a whole number of cores and the
// memory that comes with them. It is the shape a node's executor owes it, and
// the shape its claim was billed at.
type CPUClass struct {
	Cores       int64 `json:"cores"`
	MemoryBytes int64 `json:"memory_bytes"`
}

// CPUClassMemoryBytes returns the memory a class of this many cores carries.
func CPUClassMemoryBytes(cores int64) int64 {
	return cores * cpuClassMemoryBytesPerCore
}

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
	// MemoryBytes is what the node asked for when memory, not cpu, is what no
	// class covers. Zero when the cpu request alone is above every class.
	MemoryBytes int64
}

func (e *UnpricedCPUClassError) Error() string {
	if e.MemoryBytes > 0 {
		return fmt.Sprintf(
			"credits: this node asks for %d bytes of memory, which needs a %d-core class, "+
				"and the largest priced class is %d; add a class for it to the credit rate table",
			e.MemoryBytes, e.Cores, e.MaxCores)
	}
	return fmt.Sprintf(
		"credits: this node asks for %d cpu cores and the largest priced class is %d; "+
			"add a class for it to the credit rate table", e.Cores, e.MaxCores)
}

// Unwrap reports [ErrUnpricedCPUClass], so a caller matches the condition with
// errors.Is without knowing this type.
func (e *UnpricedCPUClassError) Unwrap() error { return ErrUnpricedCPUClass }

// ClassForResource returns the class that covers both halves of a node's
// request: the smallest class whose cores cover the cpu rounded up to a whole
// core and whose memory, four gibibytes for each core, covers the
// memory asked for. It returns an [UnpricedCPUClassError] when no class is
// large enough.
func (t CreditRateTable) ClassForResource(res ExecutorResource) (CreditRate, error) {
	if len(t) == 0 {
		return CreditRate{}, fmt.Errorf("%w: the rate table prices no cpu class", ErrInvalidCreditSetting)
	}
	want := int64(1)
	if res.Cores > 1 {
		want = int64(math.Ceil(res.Cores))
	}
	for _, entry := range t {
		if entry.Cores >= want && CPUClassMemoryBytes(entry.Cores) >= res.MemoryBytes {
			return entry, nil
		}
	}
	largest := t[len(t)-1]
	if CPUClassMemoryBytes(largest.Cores) < res.MemoryBytes {
		return CreditRate{}, &UnpricedCPUClassError{
			Cores: max(want, memoryClassCores(res.MemoryBytes)), MaxCores: largest.Cores,
			MemoryBytes: res.MemoryBytes,
		}
	}
	return CreditRate{}, &UnpricedCPUClassError{Cores: want, MaxCores: largest.Cores}
}

// safety: a memory request above every class is reported as the core count
// that much memory would come with, so the operator adds a class that fits
// rather than one that still refuses the node.
func memoryClassCores(bytes int64) int64 {
	cores := bytes / cpuClassMemoryBytesPerCore
	if bytes%cpuClassMemoryBytesPerCore != 0 {
		cores++
	}
	return cores
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
	_, err = s.SetCreditSettings(ctx, CreditSettingsUpdate{RateTable: &table})
	return err
}

func setCreditRateTableTx(ctx context.Context, tx *storeTx, table CreditRateTable) error {
	if err := table.Validate(); err != nil {
		return err
	}
	sorted := table.Sorted()
	encoded, err := json.Marshal(sorted)
	if err != nil {
		return fmt.Errorf("credits: encode the rate table: %w", err)
	}
	if err := setCreditSettingTx(ctx, tx, metaKeyCreditRateTable, string(encoded)); err != nil {
		return err
	}
	// safety: the scalar is the four-core price, so a table that prices no
	// four-core class leaves it where it stands rather than restating a
	// neighboring class under a name that does not mean that class.
	if base, ok := sorted.baseClassRate(); ok {
		if err := setCreditRateMicroTx(ctx, tx, base); err != nil {
			return err
		}
	}
	return nil
}

// safety: the class is resolved from the cpu and memory the scheduler sizes the
// node by, which is what the pod is given. Nothing a claimant says about itself
// reaches this, because a runner that priced its own work could bill a 64-core
// node at the smallest class.
func nodeCreditClassTx(
	ctx context.Context, q rowQuerier, table CreditRateTable, runID, nodeID string,
) (CreditRate, error) {
	charge, err := nodeChargeTx(ctx, q, runID, nodeID)
	if err != nil {
		return CreditRate{}, err
	}
	return table.ClassForResource(charge)
}
