package store_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestSchemaUpgradePreservesACopyOfConfiguredVersion37(t *testing.T) {
	target := storetest.NewSQLite(t)
	source := target.Open(t)
	cipher := drillCipher(t)
	populateInstall(t, source, cipher)
	before := fingerprintInstall(t, source, cipher)
	for _, statement := range []string{`ALTER TABLE nodes DROP COLUMN group_wait_started_at`, `UPDATE sparkwing_schema_version SET version=37,baseline_digest='7da3f341c2c5b6cc6fd60afb1a881979dfc5b64e79589a551522c655b5099e64'`} {
		if _, err := source.DB().ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	copied := filepath.Join(t.TempDir(), "copied-schema37.db")
	if _, err := source.DB().ExecContext(t.Context(), `VACUUM INTO ?`, copied); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	readVersion := func(path string) int {
		t.Helper()
		db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := db.Close(); err != nil {
				t.Error(err)
			}
		}()
		var version int
		if err := db.QueryRowContext(t.Context(), `SELECT version FROM sparkwing_schema_version`).Scan(&version); err != nil {
			t.Fatal(err)
		}
		return version
	}
	if got := readVersion(copied); got != 37 {
		t.Fatalf("copied schema=%d, want 37", got)
	}
	upgraded, err := teststore.OpenExisting(t, copied)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := upgraded.Close(); err != nil {
			t.Error(err)
		}
	}()
	assertSameInstall(t, before, fingerprintInstall(t, upgraded, cipher))
	fresh := storetest.NewSQLite(t).Open(t)
	if a, b := schemaShape(t, upgraded), schemaShape(t, fresh); a != b {
		t.Fatalf("copied upgrade differs: %s", shapeDiff(a, b))
	}
	if got := readVersion(target.Path()); got != 37 {
		t.Fatalf("source fixture changed schema: %d", got)
	}
	t.Log("schema37 fixture copy upgraded to38; original remains37; 3 runs,6 nodes,2 secrets,credit grants and runner capabilities preserved")
}
