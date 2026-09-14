package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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

// safety: a class the table does not list is priced by rounding up, so the
// ladder only has to name the sizes a cluster actually offers.
var defaultCreditCPUClasses = []int64{2, 4, 8, 16, 32, 64}

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
		return CreditRate{}, errors.New("credits: the rate table prices no cpu class")
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

// Validate reports whether every entry prices a whole number of cores above
// zero at a positive rate and no class appears twice.
func (t CreditRateTable) Validate() error {
	if len(t) == 0 {
		return errors.New("credits: the rate table must price at least one cpu class")
	}
	if len(t) > MaxCreditRateTableEntries {
		return fmt.Errorf("credits: the rate table prices at most %d cpu classes", MaxCreditRateTableEntries)
	}
	seen := make(map[int64]bool, len(t))
	for _, entry := range t {
		if entry.Cores <= 0 {
			return fmt.Errorf("credits: cpu class %d must be a positive number of cores", entry.Cores)
		}
		if entry.MicroPerSecond <= 0 {
			return fmt.Errorf("credits: the rate for the %d-core class must be positive", entry.Cores)
		}
		if seen[entry.Cores] {
			return fmt.Errorf("credits: the %d-core class is priced twice", entry.Cores)
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

// DefaultCreditRateTable prices every default cpu class at rate, which is what
// an installation that never set a table bills: one price for every size, the
// behavior of the single rate setting on its own.
func DefaultCreditRateTable(rate int64) CreditRateTable {
	out := make(CreditRateTable, 0, len(defaultCreditCPUClasses))
	for _, cores := range defaultCreditCPUClasses {
		out = append(out, CreditRate{Cores: cores, MicroPerSecond: rate})
	}
	return out
}

// CreditRateTable returns the price of a cloud runner second at every cpu
// class. An installation that never set a table reads every class at
// credit_rate_micro_per_second, so its bill is what the single rate charged.
func (s *Store) CreditRateTable(ctx context.Context) (CreditRateTable, error) {
	rate, err := s.CreditRateMicroPerSecond(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := s.creditSettingRaw(ctx, metaKeyCreditRateTable)
	if err != nil {
		return nil, err
	}
	return creditRateTable(raw, rate), nil
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
	return creditRateTable(raw, rate), nil
}

// safety: an unreadable table must not stop the ledger charging, so the flat
// default stands and the unusable value is named in the log.
func creditRateTable(raw string, rate int64) CreditRateTable {
	if raw == "" {
		return DefaultCreditRateTable(rate)
	}
	var stored CreditRateTable
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		slog.Warn("credits: the stored rate table is not readable; pricing every class at the single rate",
			"value", raw, "rate_micro_per_second", rate, "err", err)
		return DefaultCreditRateTable(rate)
	}
	if err := stored.Validate(); err != nil {
		slog.Warn("credits: the stored rate table prices nothing usable; pricing every class at the single rate",
			"value", raw, "rate_micro_per_second", rate, "err", err)
		return DefaultCreditRateTable(rate)
	}
	out := stored.Sorted()
	// safety: the two settings name one price, so the scalar an operator or an
	// older binary set wins for the class it prices.
	for i := range out {
		if out[i].Cores == CreditRateBaseClassCores {
			out[i].MicroPerSecond = rate
		}
	}
	return out
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
	if err := setCreditSettingTx(ctx, tx, metaKeyCreditRateMicro,
		formatCreditSetting(sorted.BaseRate())); err != nil {
		return err
	}
	return tx.Commit()
}

// safety: the class is resolved from the same cpu figure the scheduler sizes
// the node by, so the price follows the pod the cluster actually starts.
func nodeCreditClassTx(
	ctx context.Context, tx *storeTx, table CreditRateTable, runID, nodeID string,
) (CreditRate, error) {
	charge, err := nodeChargeTx(ctx, tx, runID, nodeID)
	if err != nil {
		return CreditRate{}, err
	}
	return table.ClassFor(charge.Cores)
}
