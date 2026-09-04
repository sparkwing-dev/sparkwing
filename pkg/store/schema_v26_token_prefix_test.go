package store_test

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/storetest"
)

func downgradeTokenPrefixIndex(t *testing.T, path string, seed ...[2]string) {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP INDEX ` + store.TokenPrefixIndexName,
		`CREATE INDEX ` + store.TokenPrefixIndexName + ` ON tokens(prefix)`,
	} {
		if _, err := st.DB().Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	for _, row := range seed {
		if err := insertTokenRow(t, st.DB(), row[0], "swu_dupdupdu", row[1]); err != nil {
			t.Fatalf("seed %s: %v", row[1], err)
		}
	}
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version >= 26`); err != nil {
		t.Fatalf("reset version: %v", err)
	}
	deleteFleetRequirements(t, st.DB())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}

func insertTokenRow(t *testing.T, db *sql.DB, hash, prefix, principal string) error {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO tokens (hash, prefix, principal, kind, scopes, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		hash, prefix, principal, "user", "admin", time.Now().Unix(),
	)
	return err
}

func TestSchemaV26_MakesTheTokenPrefixIndexUniqueOnAnOlderDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token-prefix.db")
	downgradeTokenPrefixIndex(t, path)

	up, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen at schema %d: %v", store.ExpectedSchemaVersion(), err)
	}
	defer func() { _ = up.Close() }()

	var indexSQL string
	if err := up.DB().QueryRow(storetest.Rebind(up,
		`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`),
		store.TokenPrefixIndexName).Scan(&indexSQL); err != nil {
		t.Fatalf("migrated database has no %s index: %v", store.TokenPrefixIndexName, err)
	}
	if !strings.Contains(strings.ToUpper(indexSQL), "UNIQUE") {
		t.Fatalf("%s is not unique after migration: %s", store.TokenPrefixIndexName, indexSQL)
	}

	if err := insertTokenRow(t, up.DB(), "argon2id$00$00", "swu_dupdupdu", "alice"); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	if err := insertTokenRow(t, up.DB(), "argon2id$11$11", "swu_dupdupdu", "mallory"); err == nil {
		t.Fatal("a second row on one prefix should violate the migrated unique index")
	}
}

func TestSchemaV26_RefusesToMigrateADatabaseHoldingADuplicatePrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "duplicate-prefix.db")
	downgradeTokenPrefixIndex(t, path,
		[2]string{"argon2id$00$00", "alice"},
		[2]string{"argon2id$11$11", "mallory"},
	)

	_, err := store.Open(path)
	if err == nil {
		t.Fatal("migrating a database with two rows on one prefix should fail")
	}
	if !strings.Contains(err.Error(), "swu_dupdupdu") {
		t.Fatalf("err = %v, want the offending prefix named", err)
	}
}
