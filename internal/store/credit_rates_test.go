package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// safety: the rates GitHub charges a Linux x64 runner per second, stored by an
// operator. It reaches past the default ladder on purpose, because a table an
// operator stored has to keep pricing classes the default no longer names.
func githubRateTable() store.CreditRateTable {
	return store.CreditRateTable{
		{Cores: 2, MicroPerSecond: 10_000},
		{Cores: 4, MicroPerSecond: 20_000},
		{Cores: 8, MicroPerSecond: 36_667},
		{Cores: 16, MicroPerSecond: 70_000},
		{Cores: 32, MicroPerSecond: 136_667},
		{Cores: 64, MicroPerSecond: 270_000},
	}
}

func TestRateTablePricesTheRequiredSizeExactly(t *testing.T) {
	table := githubRateTable()
	for _, size := range match.CloudSizes() {
		class, err := table.ClassForLabels([]string{size.Label}, match.Profile{})
		if err != nil || class.Cores != size.CreditClass {
			t.Fatalf("%s: %+v %v", size.Label, class, err)
		}
	}
	table = store.CreditRateTable{{Cores: 4, MicroPerSecond: 1}}
	if _, err := table.ClassForLabels([]string{"sparkwing-2cpu"}, match.Profile{}); !errors.Is(err, store.ErrUnpricedCPUClass) {
		t.Fatalf("unpriced size substituted another class: %v", err)
	}
}

func TestRateTableRejectsAnUnusableEntry(t *testing.T) {
	for name, table := range map[string]store.CreditRateTable{
		"empty":          {},
		"zero cores":     {{Cores: 0, MicroPerSecond: 1}},
		"zero rate":      {{Cores: 2, MicroPerSecond: 0}},
		"class twice":    {{Cores: 2, MicroPerSecond: 1}, {Cores: 2, MicroPerSecond: 2}},
		"negative cores": {{Cores: -2, MicroPerSecond: 1}},
	} {
		if err := table.Validate(); err == nil {
			t.Errorf("Validate accepted the %s table", name)
		}
	}
}

// An installation that never set a table bills the GitHub ladder, and its
// four-core class is the single rate setting under another name.
func TestUnsetRateTablePricesTheDefaultLadder(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	table, err := s.AsOperator().CreditRateTable(ctx)
	if err != nil {
		t.Fatalf("rate table: %v", err)
	}
	want := store.CreditRateTable{
		{Cores: 2, MicroPerSecond: 9_000},
		{Cores: 4, MicroPerSecond: 18_000},
		{Cores: 8, MicroPerSecond: 33_000},
	}
	if len(table) != len(want) {
		t.Fatalf("the default table prices %d classes, want %d", len(table), len(want))
	}
	for i, entry := range table {
		if entry != want[i] {
			t.Fatalf("the default table prices %+v, want %+v", entry, want[i])
		}
	}

	single := int64(50_000)
	if _, err := s.AsOperator().SetCreditSettings(ctx, store.CreditSettingsUpdate{RateMicroPerSecond: &single}); err != nil {
		t.Fatalf("set the single rate: %v", err)
	}
	table, err = s.AsOperator().CreditRateTable(ctx)
	if err != nil {
		t.Fatalf("rate table after the single rate moved: %v", err)
	}
	if got := table.RateFor(store.CreditRateBaseClassCores); got != 50_000 {
		t.Fatalf("four-core class = %d, want the single rate 50000", got)
	}
	if got := table.RateFor(8); got != 33_000 {
		t.Fatalf("eight-core class = %d, want the ladder's 33000", got)
	}
	settings, err := s.AsOperator().CreditSettings(ctx)
	if err != nil || settings.RateTableSet {
		t.Fatalf("RateTableSet = %v, %v; want false with no table written", settings.RateTableSet, err)
	}
}

func TestSetRateTableWritesTheFourCorePriceToTheSingleRate(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	rates := githubRateTable()
	if _, err := s.AsOperator().SetCreditSettings(ctx, store.CreditSettingsUpdate{RateTable: &rates}); err != nil {
		t.Fatalf("set the rate table: %v", err)
	}
	settings, err := s.AsOperator().CreditSettings(ctx)
	if err != nil {
		t.Fatalf("read the single rate: %v", err)
	}
	if settings.RateMicroPerSecond != 20_000 {
		t.Fatalf("single rate = %d, want the four-core price 20000", settings.RateMicroPerSecond)
	}

	single := int64(21_000)
	if _, err := s.AsOperator().SetCreditSettings(ctx, store.CreditSettingsUpdate{RateMicroPerSecond: &single}); !errors.Is(err, store.ErrInvalidCreditSetting) {
		t.Fatalf("scalar replaced an installed table: %v", err)
	}
	table, err := s.AsOperator().CreditRateTable(ctx)
	if err != nil {
		t.Fatalf("rate table: %v", err)
	}
	if got := table.RateFor(4); got != 20_000 {
		t.Fatalf("four-core class = %d, want the installed table price 20000", got)
	}
	if got := table.RateFor(8); got != 36_667 {
		t.Fatalf("eight-core class = %d, want the table's 36667", got)
	}
}

// A rate large enough to overflow the reservation is refused before it can
// write a negative charge and hand the payer credits it never bought.
func TestRateTableRefusesARateThatWouldOverflowAReservation(t *testing.T) {
	table := store.CreditRateTable{{Cores: 2, MicroPerSecond: 200_000_000_000_000_000}}
	if err := table.Validate(); err == nil {
		t.Fatal("Validate accepted a rate of 2e17 micro-credits a second")
	}
	if err := (store.CreditRateTable{{Cores: 2, MicroPerSecond: store.MaxCreditRateMicro}}).Validate(); err != nil {
		t.Fatalf("Validate refused the ceiling itself: %v", err)
	}
	s := storetest.Open(t)
	past := int64(store.MaxCreditRateMicro) + 1
	if _, err := s.AsOperator().SetCreditSettings(context.Background(),
		store.CreditSettingsUpdate{RateMicroPerSecond: &past}); err == nil {
		t.Fatal("the single rate accepted a value above the ceiling")
	}
}

// The warm class names where a class runs, so an operator may retire the warm
// pool with zero but never name a negative class.
func TestTheWarmCPUClassDefaultsToTwoCoresAndRefusesANegative(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	cores, err := s.AsOperator().WarmCPUClassCores(ctx)
	if err != nil {
		t.Fatalf("read the warm class: %v", err)
	}
	if cores != store.DefaultWarmCPUClassCores {
		t.Fatalf("warm class = %d, want %d", cores, store.DefaultWarmCPUClassCores)
	}
	retired := int64(0)
	if _, err := s.AsOperator().SetCreditSettings(ctx, store.CreditSettingsUpdate{WarmCPUClassCores: &retired}); err != nil {
		t.Fatalf("retire the warm pool: %v", err)
	}
	if cores, err := s.AsOperator().WarmCPUClassCores(ctx); err != nil || cores != 0 {
		t.Fatalf("warm class = %d, %v; want 0", cores, err)
	}
	negative := int64(-1)
	if _, err := s.AsOperator().SetCreditSettings(ctx,
		store.CreditSettingsUpdate{WarmCPUClassCores: &negative}); err == nil {
		t.Fatal("a negative warm class was accepted")
	}
}

func TestAStoredRateTableNothingCanReadIsAnError(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	if _, err := s.DB().Exec(
		storetest.Rebind(s, `INSERT INTO settings (key, value) VALUES ('credit_rate_table', ?)`), []byte(`not json`),
	); err != nil {
		t.Fatalf("write an unreadable table: %v", err)
	}
	if _, err := s.AsOperator().CreditRateTable(ctx); err == nil {
		t.Fatal("an unreadable stored table read as a usable one")
	}
	if _, err := tenant(t, s, store.DefaultTeam).CreditState(ctx, time.Hour); err == nil {
		t.Fatal("the credit state hid an unreadable rate table")
	}
}

// The scalar is the four-core price, so a table that prices no four-core class
// leaves it where it stands.
func TestATableWithoutAFourCoreClassLeavesTheScalarAlone(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	if _, err := s.AsOperator().SetCreditSettings(ctx, store.CreditSettingsUpdate{RateTable: &store.CreditRateTable{
		{Cores: 2, MicroPerSecond: 10_000}, {Cores: 8, MicroPerSecond: 36_667},
	}}); err != nil {
		t.Fatalf("set the rate table: %v", err)
	}
	settings, err := s.AsOperator().CreditSettings(ctx)
	if err != nil {
		t.Fatalf("read the single rate: %v", err)
	}
	if settings.RateMicroPerSecond != store.DefaultCreditRateMicro {
		t.Fatalf("single rate = %d, want the default it was left at", settings.RateMicroPerSecond)
	}
	if !settings.RateTableSet {
		t.Fatalf("RateTableSet = %v, %v; want true", settings.RateTableSet, err)
	}
}

func TestCPUClassMemoryBytes_LeavesTheMachinesOverhead(t *testing.T) {
	for cores, mib := range map[int64]int64{2: 5529, 4: 13107, 8: 28262} {
		if got := store.CPUClassMemoryBytes(cores); got != mib<<20 {
			t.Fatalf("%d-core class = %d MiB, want %d: an N-vCPU general-purpose node leaves no more",
				cores, got>>20, mib)
		}
	}
}
