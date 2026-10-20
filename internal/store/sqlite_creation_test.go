package store

import (
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"modernc.org/sqlite"
)

func TestPreparingSQLitePreservesAnotherConnectionsLock(t *testing.T) {
	if path := os.Getenv("SPARKWING_STORE_CREATION_LOCK_HELPER"); path != "" {
		db, err := sql.Open("sqlite", sqliteURI(path))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		_, err = db.Exec("BEGIN IMMEDIATE")
		var sqliteErr *sqlite.Error
		if !errors.As(err, &sqliteErr) || sqliteErr.Code() != 5 {
			t.Fatalf("external writer = %v, want SQLITE_BUSY", err)
		}
		return
	}
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", sqliteURI(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec("CREATE TABLE sample (value INTEGER)"); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec("INSERT INTO sample VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	if err := preparePrivateSQLite(path); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestPreparingSQLitePreservesAnotherConnectionsLock$")
	cmd.Env = append(os.Environ(), "SPARKWING_STORE_CREATION_LOCK_HELPER="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("external lock probe: %v\n%s", err, out)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestPreparingExistingSQLitePreservesItsContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	original := []byte("existing database")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := preparePrivateSQLite(path); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("existing database replaced")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("contents = %q, want %q", got, original)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("creation left temporary files: %v", entries)
	}
}
