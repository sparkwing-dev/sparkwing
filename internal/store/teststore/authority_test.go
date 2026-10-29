package teststore_test

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestAuthorityFixturesCommitAuthorizedWrites(t *testing.T) {
	st, err := teststore.OpenExisting(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	teststore.SeedTeam(t, st, "a")
	if _, err := st.AsOperator().WriteComputeLimits(t.Context(), teststore.OperatorAuthorized(t, st, ""), map[string]int64{store.ComputeLimitRunSeconds: 60}); err != nil {
		t.Fatalf("operator write: %v", err)
	}
	if _, err := st.AsOperator().WriteStorageQuota(t.Context(), teststore.OperatorAuthorized(t, st, "a"), store.StorageQuota{MaxBytesPerRun: 4}); err != nil {
		t.Fatalf("operator quota: %v", err)
	}
	console, a := teststore.Console(t, st, "a", "operator")
	rate := int64(5)
	if _, err := console.ConsoleSetCreditSettings(t.Context(), a, store.CreditSettingsUpdate{StorageRateMicroPerGBDay: &rate}); err != nil {
		t.Fatalf("console write: %v", err)
	}
}

func TestSeedCreditRatesMatchesTheConsoleWrite(t *testing.T) {
	table := store.CreditRateTable{{Cores: 8, MicroPerSecond: 40}, {Cores: 4, MicroPerSecond: 20}}
	read := func(seed func(*store.Store)) store.CreditSettings {
		st, err := teststore.OpenExisting(filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		seed(st)
		got, err := st.AsOperator().CreditSettings(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	want := read(func(st *store.Store) {
		teststore.SetCreditSettings(t, st, store.CreditSettingsUpdate{RateTable: &table})
	})
	got := read(func(st *store.Store) { teststore.SeedCreditRates(t, st, table) })
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("seeded settings = %+v, console write = %+v", got, want)
	}
}
