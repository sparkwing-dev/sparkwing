package store_test

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

// Break-glass must never act on a store it just created, so a missing or
// empty target is refused and left as it was.
func TestOpenExistingNeverCreatesASQLiteStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	if _, err := store.OpenExisting(t.Context(), store.DialectSQLite, path); err == nil {
		t.Fatal("OpenExisting opened a missing store")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenExisting created %s: %v", path, err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenExisting(t.Context(), store.DialectSQLite, path); err == nil {
		t.Fatal("OpenExisting accepted an empty file")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	created, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := created.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenExisting(t.Context(), store.DialectSQLite, path)
	if err != nil {
		t.Fatalf("OpenExisting on a created store: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenExistingNeverCreatesAPostgresStore(t *testing.T) {
	target := storetest.NewPostgres(t)
	u, err := url.Parse(target.DSN())
	if err != nil {
		t.Fatal(err)
	}
	password, _ := u.User.Password()
	if _, err := store.OpenExisting(t.Context(), store.DialectPostgres, target.DSN()); err == nil {
		t.Fatal("OpenExisting opened an empty database")
	} else if strings.Contains(err.Error(), target.DSN()) || (password != "" && strings.Contains(err.Error(), password)) {
		t.Fatalf("schema error leaked credentials: %v", err)
	}
	target.Open(t)
	st, err := store.OpenExisting(t.Context(), store.DialectPostgres, target.DSN())
	if err != nil {
		t.Fatalf("OpenExisting on a created store: %v", err)
	}
	conn, err := st.Connection(t.Context())
	if err != nil || !strings.HasPrefix(conn.Target, "postgres://") || !strings.Contains(conn.Target, "/sw_") || conn.Created == 0 {
		t.Fatalf("connection = %+v, %v; want the server, database and baseline it reached", conn, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresConfigurationErrorsOmitCredentials(t *testing.T) {
	const password = "private-admin-password"
	_, err := store.OpenExisting(t.Context(), store.DialectPostgres, "postgres://admin:"+password+"@localhost:bad-port/db")
	if err == nil || strings.Contains(err.Error(), password) || strings.Contains(err.Error(), "postgres://") {
		t.Fatalf("configuration error must omit credentials: %v", err)
	}
}

func TestOpenExistingSchemaErrorsAreDirectional(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version int
		want    string
	}{
		{"older", store.ExpectedSchemaVersion() - 1, "older than this binary"},
		{"newer", store.ExpectedSchemaVersion() + 1, "newer than this binary"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")
			st, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.DB().Exec(`UPDATE sparkwing_schema_version SET version = ?`, tc.version); err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			_, err = store.OpenExisting(t.Context(), store.DialectSQLite, path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("OpenExisting error = %v, want %q", err, tc.want)
			}
			if tc.name == "newer" && (strings.Contains(err.Error(), "delete") || strings.Contains(err.Error(), "recreate")) {
				t.Fatalf("rollback suggests discarding data: %v", err)
			}
		})
	}
}
