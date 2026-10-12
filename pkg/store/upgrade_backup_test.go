package store_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestOpenBacksUpAnOlderSQLiteDatabaseBeforeUpgrading(t *testing.T) {
	path := sqliteV49Main(t, v49MainScaleStepSQL)
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open v0.63.0 database: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(path), "backups", "state-v49-*.db"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups = %v, %v; want one copy of the v49 database", backups, err)
	}
	info, err := os.Stat(backups[0])
	expectedMode := os.FileMode(0o600)
	if runtime.GOOS == "windows" {
		expectedMode = 0o666
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != expectedMode {
		t.Fatalf("backup mode = %v, %v; want 0600", info, err)
	}
	db, err := sql.Open("sqlite", backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if got := v49Count(t, db, `SELECT MAX(version) FROM sparkwing_schema_version`); got != 49 {
		t.Fatalf("backup schema = %d, want the untouched v49", got)
	}
	if got := v49Count(t, db, `SELECT COUNT(*) FROM runs`); got != 2 {
		t.Fatalf("backup runs = %d, want 2", got)
	}

	again, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := again.Close(); err != nil {
		t.Fatal(err)
	}
	if backups, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "backups", "*.db")); len(backups) != 1 {
		t.Fatalf("backups after a current open = %v; want no new copy", backups)
	}
}
