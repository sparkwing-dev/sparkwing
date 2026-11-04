package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

func openStatementCachingDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open(cachedSQLiteDriver, sqliteDSN(filepath.Join(t.TempDir(), "cache.db")))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE item (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestStatementCacheReusesStatementsAcrossTransactions(t *testing.T) {
	db := openStatementCachingDB(t)
	ctx := context.Background()
	for i := range 3 {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO item (id, name) VALUES (?, ?)`, i, fmt.Sprint("item", i)); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM item WHERE id <= ?`, i).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != i+1 {
			t.Fatalf("count after %d inserts = %d", i+1, count)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.Raw(func(raw any) error {
		cached := raw.(*statementCachingConn)
		if len(cached.statements) != 3 {
			t.Errorf("cached statements = %d, want the table, insert and count statements", len(cached.statements))
		}
		for query, statement := range cached.statements {
			if statement.busy {
				t.Errorf("statement %q stayed busy after its call", query)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStatementCacheGivesAnOpenCursorItsOwnStatement(t *testing.T) {
	db := openStatementCachingDB(t)
	ctx := context.Background()
	for i := range 3 {
		if _, err := db.ExecContext(ctx, `INSERT INTO item (id, name) VALUES (?, ?)`, i, fmt.Sprint("item", i)); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	const query = `SELECT id FROM item WHERE id >= ? ORDER BY id`
	outer, err := tx.QueryContext(ctx, query, 0)
	if err != nil {
		t.Fatal(err)
	}
	var seen []int
	for outer.Next() {
		var id int
		if err := outer.Scan(&id); err != nil {
			t.Fatal(err)
		}
		seen = append(seen, id)
		inner, err := tx.QueryContext(ctx, query, 2)
		if err != nil {
			t.Fatal(err)
		}
		var last int
		for inner.Next() {
			if err := inner.Scan(&last); err != nil {
				t.Fatal(err)
			}
		}
		if err := inner.Close(); err != nil {
			t.Fatal(err)
		}
		if last != 2 {
			t.Fatalf("inner query = %d, want 2", last)
		}
	}
	if err := outer.Err(); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(seen) != "[0 1 2]" {
		t.Fatalf("outer rows = %v, want every row despite the nested identical query", seen)
	}
}

func TestStatementCacheStopsGrowingAtItsBound(t *testing.T) {
	db := openStatementCachingDB(t)
	ctx := context.Background()
	for i := range maxCachedStatements + 10 {
		var n int
		if err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT %d`, i)).Scan(&n); err != nil || n != i {
			t.Fatalf("SELECT %d = %d, %v", i, n, err)
		}
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.Raw(func(raw any) error {
		if got := len(raw.(*statementCachingConn).statements); got > maxCachedStatements {
			t.Errorf("cached statements = %d, past the bound %d", got, maxCachedStatements)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
