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
	// RequireEnv turns a missing URLEnv into a failure instead of a skip
	// for tests that are Postgres-only.
	RequireEnv = "SPARKWING_REQUIRE_PG"
)

// Dialect reports the dialect the suite runs against.
func Dialect() store.Dialect {
	if strings.EqualFold(strings.TrimSpace(os.Getenv(DialectEnv)), "postgres") {
		return store.DialectPostgres
	}
	return store.DialectSQLite
}

// IsPostgres reports whether the suite runs against Postgres.
func IsPostgres() bool { return Dialect() == store.DialectPostgres }

// SkipOnPostgres skips a test whose subject is the SQLite file itself.
func SkipOnPostgres(t *testing.T, why string) {
	t.Helper()
	if IsPostgres() {
		t.Skip(why)
	}
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
	if !IsPostgres() {
		return &Target{dialect: store.DialectSQLite, path: filepath.Join(t.TempDir(), "state.db")}
	}
	dsn := os.Getenv(URLEnv)
	if strings.TrimSpace(dsn) == "" {
		t.Fatalf("%s=postgres requires %s to name a reachable Postgres", DialectEnv, URLEnv)
	}
	return &Target{dialect: store.DialectPostgres, dsn: newSchema(t, dsn)}
}

// NewPostgres reserves a Postgres schema whatever the suite's dialect is,
// skipping the test when no server is configured and RequireEnv is unset.
func NewPostgres(t *testing.T) *Target {
	t.Helper()
	return &Target{dialect: store.DialectPostgres, dsn: newSchema(t, PostgresURL(t))}
}

// PostgresURL returns the configured server URL, skipping the test when
// there is none and RequireEnv is unset.
func PostgresURL(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(URLEnv)
	if strings.TrimSpace(dsn) == "" {
		if os.Getenv(RequireEnv) != "" {
			t.Fatalf("%s is set, so %s must name a reachable Postgres", RequireEnv, URLEnv)
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

// Dialect reports the dialect this target opens.
func (tg *Target) Dialect() store.Dialect { return tg.dialect }

// Path is the SQLite file the target opens, empty under Postgres.
func (tg *Target) Path() string { return tg.path }

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

func (tg *Target) open() (*store.Store, error) {
	if tg.dialect == store.DialectPostgres {
		return store.OpenPostgres(context.Background(), tg.dsn)
	}
	return store.Open(tg.path)
}

func newSchema(t *testing.T, baseDSN string) string {
	t.Helper()
	schema := "sw_test_" + sanitize(t.Name()) + "_" + Unique()

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
	return WithSearchPath(baseDSN, schema)
}

// WithSearchPath points a connection string at one schema.
func WithSearchPath(dsn, schema string) string {
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

func sanitize(s string) string {
	r := strings.NewReplacer("/", "_", " ", "_", "-", "_", ".", "_", "#", "_", "(", "_", ")", "_")
	out := r.Replace(s)
	if len(out) > 40 {
		out = out[:40]
	}
	return strings.ToLower(out)
}
