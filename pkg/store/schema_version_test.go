package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestSchemaVersion_FreshSQLiteRecordsExpected(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	v := readSchemaVersion(t, st.DB())
	if v != store.ExpectedSchemaVersion() {
		t.Errorf("MAX(version) = %d, want %d", v, store.ExpectedSchemaVersion())
	}
}

func TestSchemaVersion_ReopenSQLiteIsNoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reopen.db")

	st1, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open#1: %v", err)
	}
	_ = st1.Close()

	st2, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open#2: %v", err)
	}
	defer func() { _ = st2.Close() }()

	count := schemaVersionRowCount(t, st2.DB())
	want := store.ExpectedSchemaVersion()
	if count != want {
		t.Errorf("version rows = %d after reopen, want %d", count, want)
	}
}

func TestSchemaVersion_SQLiteSkewRefuses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skew.db")

	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open#1: %v", err)
	}
	future := store.ExpectedSchemaVersion() + 1
	if _, err := st.DB().Exec(
		`INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (?, ?)`,
		future, 1,
	); err != nil {
		t.Fatalf("seed future version: %v", err)
	}
	_ = st.Close()

	_, err = store.Open(path)
	if err == nil {
		t.Fatal("Open against future-version DB should fail")
	}
	var skew *store.SkewError
	if !errors.As(err, &skew) {
		t.Fatalf("err = %v, want *SkewError", err)
	}
	if skew.DBVersion != future || skew.BinaryVersion != store.ExpectedSchemaVersion() {
		t.Errorf("skew = {DB:%d, Binary:%d}, want {DB:%d, Binary:%d}",
			skew.DBVersion, skew.BinaryVersion, future, store.ExpectedSchemaVersion())
	}
	msg := err.Error()
	if !strings.Contains(strings.ToLower(msg), "upgrade sparkwing") {
		t.Errorf("error message should mention 'upgrade sparkwing'; got: %v", msg)
	}
	if !strings.Contains(msg, fmt.Sprintf("%d", future)) {
		t.Errorf("error message should mention DB version %d; got: %v", future, msg)
	}
}

func TestSchemaVersion_FreshPostgresRecordsExpected(t *testing.T) {
	st := openPGTestStore(t)
	v := readSchemaVersion(t, st.DB())
	if v != store.ExpectedSchemaVersion() {
		t.Errorf("MAX(version) = %d, want %d", v, store.ExpectedSchemaVersion())
	}
}

func TestSchemaVersion_PostgresSkewRefuses(t *testing.T) {
	dsn := pgTestDSN(t)
	st := openPGTestStore(t)
	future := store.ExpectedSchemaVersion() + 1
	if _, err := st.DB().Exec(
		`INSERT INTO sparkwing_schema_version (version, applied_at) VALUES ($1, $2)`,
		future, int64(1),
	); err != nil {
		t.Fatalf("seed future version: %v", err)
	}
	var searchPath string
	if err := st.DB().QueryRow(`SHOW search_path`).Scan(&searchPath); err != nil {
		t.Fatalf("read search_path: %v", err)
	}
	schema := strings.TrimSpace(strings.Split(searchPath, ",")[0])
	scoped := fmt.Sprintf("%s%ssearch_path=%s", dsn, querySep(dsn), schema)
	_, err := store.OpenPostgres(context.Background(), scoped)
	if err == nil {
		t.Fatal("Open against future-version pg DB should fail")
	}
	var skew *store.SkewError
	if !errors.As(err, &skew) {
		t.Fatalf("err = %v, want *SkewError", err)
	}
	if skew.DBVersion != future || skew.BinaryVersion != store.ExpectedSchemaVersion() {
		t.Errorf("skew = {DB:%d, Binary:%d}, want {DB:%d, Binary:%d}",
			skew.DBVersion, skew.BinaryVersion, future, store.ExpectedSchemaVersion())
	}
	if !strings.Contains(strings.ToLower(err.Error()), "upgrade sparkwing") {
		t.Errorf("error message should mention 'upgrade sparkwing'; got: %v", err)
	}
}

func TestSchemaVersion_ConcurrentPostgresOpens(t *testing.T) {
	dsn := pgTestDSN(t)
	schema := "sw_test_concurrent_" + uniq()
	admin, err := store.OpenPostgres(context.Background(), dsn)
	if err != nil {
		t.Fatalf("admin open: %v", err)
	}
	if _, err := admin.DB().Exec(`CREATE SCHEMA IF NOT EXISTS ` + schema); err != nil {
		_ = admin.Close()
		t.Fatalf("create schema: %v", err)
	}
	_ = admin.Close()
	t.Cleanup(func() {
		cleanup, _ := store.OpenPostgres(context.Background(), dsn)
		if cleanup != nil {
			_, _ = cleanup.DB().Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
			_ = cleanup.Close()
		}
	})

	scoped := fmt.Sprintf("%s%ssearch_path=%s", dsn, querySep(dsn), schema)

	const n = 5
	type result struct {
		st  *store.Store
		err error
	}
	results := make([]result, n)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			st, err := store.OpenPostgres(context.Background(), scoped)
			results[i] = result{st: st, err: err}
		}(i)
	}
	close(start)
	wg.Wait()

	for i, r := range results {
		if r.err != nil {
			t.Errorf("opener %d: %v", i, r.err)
			continue
		}
		t.Cleanup(func() { _ = r.st.Close() })
	}

	verify, err := store.OpenPostgres(context.Background(), scoped)
	if err != nil {
		t.Fatalf("verify open: %v", err)
	}
	defer func() { _ = verify.Close() }()
	count := schemaVersionRowCount(t, verify.DB())
	want := store.ExpectedSchemaVersion()
	if count != want {
		t.Errorf("version rows = %d after %d concurrent opens, want %d", count, n, want)
	}
}

func readSchemaVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v sql.NullInt64
	if err := db.QueryRow(`SELECT MAX(version) FROM sparkwing_schema_version`).Scan(&v); err != nil {
		t.Fatalf("read schema_version: %v", err)
	}
	if !v.Valid {
		return 0
	}
	return int(v.Int64)
}

func schemaVersionRowCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sparkwing_schema_version`).Scan(&n); err != nil {
		t.Fatalf("count schema_version: %v", err)
	}
	return n
}

func querySep(dsn string) string {
	if strings.Contains(dsn, "?") {
		return "&"
	}
	return "?"
}
