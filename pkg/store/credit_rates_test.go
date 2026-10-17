package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
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

func TestRateTableRoundsACPURequestUpToItsClass(t *testing.T) {
	table := githubRateTable()
	for _, tc := range []struct {
		cores float64
		want  int64
	}{
		{cores: 0, want: 2},
		{cores: 0.25, want: 2},
		{cores: 1, want: 2},
		{cores: 2, want: 2},
		{cores: 2.1, want: 4},
		{cores: 4, want: 4},
		{cores: 5, want: 8},
		{cores: 8, want: 8},
		{cores: 33, want: 64},
		{cores: 64, want: 64},
	} {
		class, err := table.ClassForResource(match.Resources{Cores: tc.cores})
		if err != nil {
			t.Fatalf("ClassForResource(%g): %v", tc.cores, err)
		}
		if class.Cores != tc.want {
			t.Errorf("ClassForResource(%g) = %d cores, want %d", tc.cores, class.Cores, tc.want)
		}
	}
}

func TestRateTableRefusesARequestAboveItsLargestClass(t *testing.T) {
	_, err := githubRateTable().ClassForResource(match.Resources{Cores: 96})
	if !errors.Is(err, store.ErrUnpricedCPUClass) {
		t.Fatalf("ClassForResource(96) = %v, want ErrUnpricedCPUClass", err)
	}
	var unpriced *store.UnpricedCPUClassError
	if !errors.As(err, &unpriced) {
		t.Fatalf("error %v does not name the request", err)
	}
	if unpriced.Cores != 96 || unpriced.MaxCores != 64 {
		t.Fatalf("refusal = %+v, want 96 cores against a 64-core ceiling", unpriced)
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

func TestClassForResource_NeverGivesAMemoryPinLessThanItAsked(t *testing.T) {
	table := store.CreditRateTable{
		{Cores: 2, MicroPerSecond: 10_000},
		{Cores: 4, MicroPerSecond: 20_000},
		{Cores: 8, MicroPerSecond: 36_667},
	}
	for _, tc := range []struct {
		gib  float64
		want int64
	}{
		{gib: 5, want: 2},
		{gib: 6, want: 4},
		{gib: 15, want: 8},
	} {
		pin := int64(tc.gib * (1 << 30))
		class, err := table.ClassForResource(match.Resources{Cores: 1, MemoryBytes: pin})
		if err != nil {
			t.Fatalf("%g GiB: %v", tc.gib, err)
		}
		if class.Cores != tc.want || store.CPUClassMemoryBytes(class.Cores) < pin {
			t.Fatalf("%g GiB took the %d-core class, whose Job gets %d bytes; want the %d-core class",
				tc.gib, class.Cores, store.CPUClassMemoryBytes(class.Cores), tc.want)
		}
	}

	_, err := table.ClassForResource(match.Resources{Cores: 1, MemoryBytes: 30 << 30})
	var unpriced *store.UnpricedCPUClassError
	if !errors.As(err, &unpriced) || unpriced.Cores != 9 ||
		!strings.Contains(err.Error(), fmt.Sprint(store.CPUClassMemoryBytes(8))) {
		t.Fatalf("30 GiB against an 8-core ladder = %v, want a refusal naming a 9-core class "+
			"and the 8-core class's usable memory", err)
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
