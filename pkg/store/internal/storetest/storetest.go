// Package storetest opens stores for the pkg/store suite in whichever
// dialect the run selects, so one test body covers both.
package storetest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	// DialectEnv selects the dialect the suite runs against; "postgres"
	// is the only value that changes anything.
	DialectEnv = "SPARKWING_TEST_STORE"
	// URLEnv names the Postgres server that schema-scoped test stores
	// are created on.
	URLEnv = "SPARKWING_TEST_PG_URL"
)

func suiteRunsOnPostgres() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(DialectEnv)), "postgres")
}

// Target is a store location that survives Close, so a test can open it
// again the way a migration test reopens a database.
type Target struct {
	dialect store.Dialect
	path    string
	dsn     string
}

// New reserves a location in the suite's dialect: a file under the test's
// temporary directory, or a schema of its own on the configured server.
func New(t *testing.T) *Target {
	t.Helper()
	if !suiteRunsOnPostgres() {
		return &Target{dialect: store.DialectSQLite, path: filepath.Join(t.TempDir(), "state.db")}
	}
	dsn := os.Getenv(URLEnv)
	if strings.TrimSpace(dsn) == "" {
		t.Fatalf("%s=postgres requires %s to name a reachable Postgres", DialectEnv, URLEnv)
	}
	return &Target{dialect: store.DialectPostgres, dsn: newSchema(t, dsn)}
}

// NewSQLite reserves a SQLite file whatever the suite's dialect is, for
// the few tests that name both dialects themselves.
func NewSQLite(t *testing.T) *Target {
	t.Helper()
	return &Target{dialect: store.DialectSQLite, path: filepath.Join(t.TempDir(), "state.db")}
}

// OpenSQLite opens a SQLite store whatever the suite's dialect is.
func OpenSQLite(t *testing.T) *store.Store {
	t.Helper()
	return NewSQLite(t).Open(t)
}

// NewPostgres reserves a Postgres schema whatever the suite's dialect is,
// skipping the test when no server is configured and RequireEnv is unset.
func NewPostgres(t *testing.T) *Target {
	t.Helper()
	return &Target{dialect: store.DialectPostgres, dsn: newSchema(t, PostgresURL(t))}
}

// PostgresURL returns the configured server URL. A suite that selected
// the Postgres dialect fails without one; any other suite skips.
func PostgresURL(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(URLEnv)
	if strings.TrimSpace(dsn) == "" {
		if suiteRunsOnPostgres() {
			t.Fatalf("%s=postgres requires %s to name a reachable Postgres", DialectEnv, URLEnv)
		}
		t.Skipf("%s not set; skipping Postgres test", URLEnv)
	}
	return dsn
}

// Open opens a store for the test in the suite's dialect.
func Open(t *testing.T) *store.Store {
	t.Helper()
	return New(t).Open(t)
}

// OpenPostgres opens a schema-scoped Postgres store whatever the suite's
// dialect is, skipping when no server is configured.
func OpenPostgres(t *testing.T) *store.Store {
	t.Helper()
	return NewPostgres(t).Open(t)
}

// DSN is the schema-scoped connection string the target opens, empty
// under SQLite.
func (tg *Target) DSN() string { return tg.dsn }

// Open opens the target and closes the store when the test ends.
func (tg *Target) Open(t *testing.T) *store.Store {
	t.Helper()
	st, err := tg.open()
	if err != nil {
		t.Fatalf("open %s store: %v", tg.dialect, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TryOpen opens the target and hands back the failure instead of ending
// the test, for the tests whose subject is a refused open.
func (tg *Target) TryOpen() (*store.Store, error) {
	return tg.open()
}

func (tg *Target) open() (*store.Store, error) {
	if tg.dialect == store.DialectPostgres {
		return store.OpenPostgres(context.Background(), tg.dsn)
	}
	return store.Open(tg.path)
}

func newSchema(t *testing.T, baseDSN string) string {
	t.Helper()
	schema := schemaName(t.Name())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := store.OpenPostgres(ctx, baseDSN)
	if err != nil {
		t.Fatalf("open admin postgres: %v", err)
	}
	if _, err := admin.DB().ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS `+schema); err != nil {
		_ = admin.Close()
		t.Fatalf("create schema %s: %v", schema, err)
	}
	_ = admin.Close()

	t.Cleanup(func() {
		cleanup, err := store.OpenPostgres(context.Background(), baseDSN)
		if err != nil {
			return
		}
		_, _ = cleanup.DB().Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
		_ = cleanup.Close()
	})
	return withSearchPath(baseDSN, schema)
}

// Rebind translates a query written with SQLite's "?" placeholders into
// the dialect the store speaks, so one raw statement seeds both.
func Rebind(st *store.Store, query string) string {
	if st.Dialect() != store.DialectPostgres {
		return query
	}
	var out strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			fmt.Fprintf(&out, "$%d", n)
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

func withSearchPath(dsn, schema string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%ssearch_path=%s", dsn, sep, schema)
}

// Unique returns a short token that is distinct within one test binary,
// for naming schemas and other shared resources.
func Unique() string {
	uniqCounter.Lock()
	defer uniqCounter.Unlock()
	uniqCounter.n++
	return fmt.Sprintf("%d_%d", time.Now().UnixNano()&0xffffff, uniqCounter.n)
}

var uniqCounter struct {
	sync.Mutex
	n int
}

// schemaName keeps the whole name inside Postgres's 63-byte identifier
// limit with the uniquifier last, because Postgres truncates the tail.
func schemaName(testName string) string {
	const prefix = "sw_test_"
	const identifierLimit = 63
	unique := Unique()
	name := sanitize(testName)
	if room := identifierLimit - len(prefix) - len(unique) - 1; len(name) > room {
		name = name[:max(room, 0)]
	}
	return prefix + name + "_" + unique
}

func sanitize(s string) string {
	out := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		}
		return '_'
	}, s)
	return out
}
