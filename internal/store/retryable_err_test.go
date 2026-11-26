package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"modernc.org/sqlite"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func openRawSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func rawConn(t *testing.T, db *sql.DB) *sql.Conn {
	t.Helper()
	c, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func execRaw(t *testing.T, c *sql.Conn, statement string) {
	t.Helper()
	if _, err := c.ExecContext(t.Context(), statement); err != nil {
		t.Fatal(err)
	}
}

// safety: sqliteLockedError is SQLITE_LOCKED: a connection dropping a table its own open statement still reads.
func sqliteLockedError(t *testing.T) error {
	t.Helper()
	c := rawConn(t, openRawSQLite(t, filepath.Join(t.TempDir(), "locked.db")))
	execRaw(t, c, "CREATE TABLE t(x)")
	execRaw(t, c, "INSERT INTO t VALUES (1),(2)")
	rows, err := c.QueryContext(t.Context(), "SELECT x FROM t")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	rows.Next()
	_, err = c.ExecContext(t.Context(), "DROP TABLE t")
	return err
}

// safety: SQLITE_BUSY_SNAPSHOT is an extended BUSY code; a WAL reader gets it when it upgrades to a writer
// after another connection committed past its snapshot.
func sqliteBusySnapshotError(t *testing.T) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), "snapshot.db")
	reader, writer := rawConn(t, openRawSQLite(t, path)), rawConn(t, openRawSQLite(t, path))
	execRaw(t, reader, "CREATE TABLE t(x)")
	execRaw(t, reader, "BEGIN")
	execRaw(t, reader, "SELECT count(*) FROM t")
	execRaw(t, writer, "INSERT INTO t VALUES (1)")
	_, err := reader.ExecContext(t.Context(), "INSERT INTO t VALUES (2)")
	return err
}

func TestRetryableErrReadsDriverCodesNotMessages(t *testing.T) {
	retryable := map[string]error{
		"sqlite busy":                 teststore.SQLiteBusyError(t),
		"sqlite locked":               sqliteLockedError(t),
		"sqlite busy snapshot":        sqliteBusySnapshotError(t),
		"wrapped sqlite busy":         fmt.Errorf("read run: %w", teststore.SQLiteBusyError(t)),
		"postgres serialization":      &pgconn.PgError{Code: "40001"},
		"postgres deadlock":           &pgconn.PgError{Code: "40P01"},
		"postgres lock not available": &pgconn.PgError{Code: "55P03"},
		"store deadline":              context.DeadlineExceeded,
	}
	for name, err := range retryable {
		t.Run(name, func(t *testing.T) {
			var sqliteErr *sqlite.Error
			if errors.As(err, &sqliteErr) {
				t.Logf("modernc code %d: %v", sqliteErr.Code(), err)
			}
			if !store.IsRetryableErr(err) {
				t.Fatalf("IsRetryableErr(%v) = false, want true", err)
			}
		})
	}
	notRetryable := map[string]error{
		"busy text in a stored value": errors.New(`scan run: malformed lineage "database is locked (5) (SQLITE_BUSY)"`),
		"locked text in a message":    errors.New("database table is locked"),
		"postgres unique violation":   &pgconn.PgError{Code: "23505"},
		"cancelled request":           context.Canceled,
	}
	for name, err := range notRetryable {
		t.Run(name, func(t *testing.T) {
			if store.IsRetryableErr(err) {
				t.Fatalf("IsRetryableErr(%v) = true, want false", err)
			}
		})
	}
}
