package teststore

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
)

// SQLiteBusyError returns the error modernc reports when a second writer finds a SQLite file already locked, so a
// test can feed a real lock conflict to code that classifies store errors.
func SQLiteBusyError(t T) error {
	t.Helper()
	dir, err := os.MkdirTemp("", "sqlite-busy-")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "busy.db")
	holder, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(errors.Join(err, os.RemoveAll(dir)))
	}
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(errors.Join(err, holder.Close(), os.RemoveAll(dir)))
	}
	release := func() error { return errors.Join(writer.Close(), holder.Close(), os.RemoveAll(dir)) }
	held, err := holder.Conn(t.Context())
	if err != nil {
		t.Fatal(errors.Join(err, release()))
	}
	if _, err := held.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(errors.Join(err, held.Close(), release()))
	}
	_, busy := writer.ExecContext(t.Context(), "BEGIN IMMEDIATE")
	_, rollback := held.ExecContext(t.Context(), "ROLLBACK")
	if err := errors.Join(rollback, held.Close(), release()); err != nil {
		t.Fatal(err)
	}
	if busy == nil {
		t.Fatal("a second writer took a held SQLite write lock")
	}
	return busy
}
