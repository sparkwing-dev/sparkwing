package store_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// safety: stripping the column and the index is what makes reopening the
// database run the migration against the shape a v41 binary left behind.
func downgradeGrantsToV41(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`DROP INDEX IF EXISTS idx_credit_grants_reference`,
		`ALTER TABLE credit_grants DROP COLUMN reverses`,
		`DELETE FROM sparkwing_schema_version WHERE version >= 42`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func insertGrantTheOldWay(t *testing.T, st *store.Store, id, kind, reference string, amount int64) error {
	t.Helper()
	_, err := st.DB().ExecContext(context.Background(), storetest.Rebind(st,
		`INSERT INTO credit_grants (id, kind, amount_micro, reference, created_by, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`), id, kind, amount, reference, "admin", int64(1))
	return err
}

// A database stamped short of the reference key takes the column and the index
// on its own, keeps the grants it already holds, and stays writable by a binary
// that names no reverses column.
func TestSchemaV42_UpgradeFromAStoreStampedShortOfTheReferenceKey(t *testing.T) {
	target := storetest.New(t)
	seeded, err := target.TryOpen()
	if err != nil {
		t.Fatalf("Open#1: %v", err)
	}
	ctx := context.Background()
	if _, err := seeded.GrantCredits(ctx, store.CreditGrantPaid,
		1000*store.MicroCreditsPerCredit, "pi_legacy", "admin"); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	before, err := seeded.Requirements(ctx)
	if err != nil {
		t.Fatalf("requirements before: %v", err)
	}
	downgradeGrantsToV41(t, seeded.DB())
	if v := readSchemaVersion(t, seeded.DB()); v != 41 {
		t.Fatalf("seeded version = %d, want 41", v)
	}
	_ = seeded.Close()

	upgraded, err := target.TryOpen()
	if err != nil {
		t.Fatalf("Open#2 (upgrade): %v", err)
	}
	defer func() { _ = upgraded.Close() }()
	if v := readSchemaVersion(t, upgraded.DB()); v != store.ExpectedSchemaVersion() {
		t.Fatalf("version after upgrade = %d, want %d", v, store.ExpectedSchemaVersion())
	}

	balance, err := upgraded.CreditBalanceMicro(ctx)
	if err != nil {
		t.Fatalf("balance after upgrade: %v", err)
	}
	if want := int64(1000 * store.MicroCreditsPerCredit); balance != want {
		t.Fatalf("balance after upgrade = %d, want the seeded grant %d", balance, want)
	}

	// safety: the previous release opens this database only while the migration
	// declares no requirement and its insert still names no reverses column.
	after, err := upgraded.Requirements(ctx)
	if err != nil {
		t.Fatalf("requirements after: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("requirements = %v after the upgrade, want the %v a v41 binary knows", after, before)
	}
	if err := insertGrantTheOldWay(t, upgraded, "grant-old", store.CreditGrantFree, "note", 5); err != nil {
		t.Errorf("an insert that names no reverses column was refused: %v", err)
	}

	if err := insertGrantTheOldWay(t, upgraded, "grant-dupe",
		store.CreditGrantPaid, "pi_legacy", 5); err == nil {
		t.Error("the reference key let a second paid grant of one payment in")
	}
	if _, err := upgraded.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantReversal, AmountMicro: -400 * store.MicroCreditsPerCredit,
		Reference: "re_legacy", Reverses: "pi_legacy", CreatedBy: "billing",
	}); err != nil {
		t.Fatalf("reversal on the migrated store: %v", err)
	}
	balance, err = upgraded.CreditBalanceMicro(ctx)
	if err != nil {
		t.Fatalf("balance after the reversal: %v", err)
	}
	if want := int64(600*store.MicroCreditsPerCredit + 5); balance != want {
		t.Fatalf("balance after the reversal = %d, want %d", balance, want)
	}
}

// Grants written before the reference was a key may already repeat one, and
// the ledger has to open anyway; the grant path still refuses the second
// delivery of a payment.
func TestSchemaV42_OpensOverGrantsThatAlreadyRepeatAReference(t *testing.T) {
	target := storetest.New(t)
	seeded, err := target.TryOpen()
	if err != nil {
		t.Fatalf("Open#1: %v", err)
	}
	ctx := context.Background()
	downgradeGrantsToV41(t, seeded.DB())
	for _, id := range []string{"grant-a", "grant-b"} {
		if err := insertGrantTheOldWay(t, seeded, id, store.CreditGrantFree, "welcome", 7); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	_ = seeded.Close()

	upgraded, err := target.TryOpen()
	if err != nil {
		t.Fatalf("Open#2 (upgrade over duplicates): %v", err)
	}
	defer func() { _ = upgraded.Close() }()
	if v := readSchemaVersion(t, upgraded.DB()); v != store.ExpectedSchemaVersion() {
		t.Fatalf("version after upgrade = %d, want %d", v, store.ExpectedSchemaVersion())
	}
	enforced, err := upgraded.CreditGrantReferenceIndexPresent(ctx)
	if err != nil {
		t.Fatalf("read whether the key is enforced: %v", err)
	}
	if enforced {
		t.Error("the key was created over rows that repeat a reference")
	}
	first, err := upgraded.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantPaid, AmountMicro: store.MicroCreditsPerCredit,
		Reference: "pi_after", CreatedBy: "billing",
	})
	if err != nil {
		t.Fatalf("grant after the upgrade: %v", err)
	}
	again, err := upgraded.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantPaid, AmountMicro: store.MicroCreditsPerCredit,
		Reference: "pi_after", CreatedBy: "billing",
	})
	if err != nil {
		t.Fatalf("redelivered grant after the upgrade: %v", err)
	}
	if again.Created || again.Grant.ID != first.Grant.ID {
		t.Fatalf("redelivery = %+v, want the first grant %q", again, first.Grant.ID)
	}

	if _, err := upgraded.DB().ExecContext(ctx,
		`DELETE FROM credit_grants WHERE id = 'grant-b'`); err != nil {
		t.Fatalf("delete the duplicate row: %v", err)
	}
	_ = upgraded.Close()

	repaired, err := target.TryOpen()
	if err != nil {
		t.Fatalf("Open#3 (after the duplicates went): %v", err)
	}
	defer func() { _ = repaired.Close() }()
	enforced, err = repaired.CreditGrantReferenceIndexPresent(ctx)
	if err != nil {
		t.Fatalf("read whether the key is enforced: %v", err)
	}
	if !enforced {
		t.Fatal("deleting the duplicates and reopening did not create the key")
	}
	if err := insertGrantTheOldWay(t, repaired, "grant-c",
		store.CreditGrantPaid, "pi_after", 1); err == nil {
		t.Error("the key let a second paid grant of one payment in")
	}
}
