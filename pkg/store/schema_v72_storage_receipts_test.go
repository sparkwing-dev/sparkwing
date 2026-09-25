package store_test

import (
	"context"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestV72StorageReceiptsUpgradeSQLite(t *testing.T) {
	assertV72StorageReceiptsUpgrade(t, storetest.NewSQLite(t))
}

func TestV72StorageReceiptsUpgradePostgres(t *testing.T) {
	assertV72StorageReceiptsUpgrade(t, storetest.NewPostgres(t))
}

func assertV72StorageReceiptsUpgrade(t *testing.T, target *storetest.Target) {
	t.Helper()
	st, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	downgradeTriggerCreditCursor(t, st)
	for _, statement := range []string{
		`DROP TABLE storage_commit_receipts`,
		`DELETE FROM sparkwing_requirements WHERE name = 'storage-commit-receipts-v1'`,
		`DELETE FROM sparkwing_schema_version WHERE version = 72`,
	} {
		if _, err := st.DB().ExecContext(t.Context(), statement); err != nil {
			t.Fatalf("downgrade v72 with %q: %v", statement, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	up, err := target.TryOpen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = up.Close() }()
	if got, err := up.CurrentSchemaVersion(context.Background()); err != nil || got != store.ExpectedSchemaVersion() {
		t.Fatalf("upgraded schema = %d, %v", got, err)
	}
	var count int
	if err := up.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM storage_commit_receipts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("receipt table after upgrade = %d, %v", count, err)
	}
	requirements, err := up.Requirements(t.Context())
	if err != nil || !slices.Contains(requirements, "storage-commit-receipts-v1") {
		t.Fatalf("upgraded requirements = %v, %v", requirements, err)
	}
	old := slices.DeleteFunc(slices.Clone(store.KnownRequirements()), func(s string) bool { return s == "storage-commit-receipts-v1" })
	if missing := store.MissingRequirements(old, requirements); !slices.Contains(missing, "storage-commit-receipts-v1") {
		t.Fatalf("older controller accepts new receipt requirement: %v", missing)
	}
}
