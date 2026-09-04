package store_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A failing statement mid-version must leave neither the version row nor the
// statements that ran before it. v13 adds two trigger columns and then a
// unique index; a table squatting on the index name fails that last step.
func TestMigrateSQLite_FailedVersionLeavesNoPartialSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partial.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP INDEX ` + store.TriggerIdempotencyIndexName,
		`ALTER TABLE triggers DROP COLUMN idempotency_key`,
		`ALTER TABLE triggers DROP COLUMN claim_seq`,
		`DELETE FROM sparkwing_schema_version WHERE version >= 13`,
		`CREATE TABLE ` + store.TriggerIdempotencyIndexName + ` (blocker TEXT)`,
	} {
		if _, err := st.DB().Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	deleteFleetRequirements(t, st.DB())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(path)
	if err == nil {
		_ = reopened.Close()
		t.Fatal("Open succeeded; want the v13 migration to fail")
	}
	if !strings.Contains(err.Error(), "apply migration v13") {
		t.Fatalf("Open err = %v, want it to name migration v13", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	var stamped int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sparkwing_schema_version WHERE version = 13`).Scan(&stamped); err != nil {
		t.Fatalf("count version 13: %v", err)
	}
	if stamped != 0 {
		t.Errorf("sparkwing_schema_version has %d rows for v13, want 0", stamped)
	}

	for _, column := range []string{"idempotency_key", "claim_seq"} {
		var found int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('triggers') WHERE name = ?`, column,
		).Scan(&found); err != nil {
			t.Fatalf("inspect triggers.%s: %v", column, err)
		}
		if found != 0 {
			t.Errorf("triggers.%s survived the failed migration", column)
		}
	}
}

// The min-binary-version stamp belongs to the newest version's transaction.
// Stamped after the loop instead, a failure here would leave the version row
// committed, and the next open would skip both the version and the stamp.
func TestMigrateSQLite_FailedMinVersionStampRollsBackTheVersionRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stamp.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	newest := store.ExpectedSchemaVersion()
	for _, stmt := range []string{
		`DROP TABLE sparkwing_meta`,
		fmt.Sprintf(`DELETE FROM sparkwing_schema_version WHERE version >= %d`, newest),
	} {
		if _, err := st.DB().Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	deleteFleetRequirements(t, st.DB())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(path)
	if err == nil {
		_ = reopened.Close()
		t.Fatal("Open succeeded; want the min version stamp to fail")
	}
	if !strings.Contains(err.Error(), "stamp min version") {
		t.Fatalf("Open err = %v, want it to name the min version stamp", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	var stamped int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sparkwing_schema_version WHERE version = ?`, newest).Scan(&stamped); err != nil {
		t.Fatalf("count version %d: %v", newest, err)
	}
	if stamped != 0 {
		t.Errorf("version %d committed without its min version stamp", newest)
	}
}
