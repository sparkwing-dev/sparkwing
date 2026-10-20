package store_test

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestPostgresIdentityUsesTableSchema(t *testing.T) {
	empty := storetest.NewPostgres(t)
	a, b := storetest.NewPostgres(t), storetest.NewPostgres(t)
	first, second := a.Open(t), b.Open(t)
	id, err := first.Connection(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.SQL(t, second).ExecContext(t.Context(), `UPDATE sparkwing_schema_version SET applied_at = $1`, id.Created); err != nil {
		t.Fatal(err)
	}
	prefix, _ := url.Parse(empty.DSN())
	var ids []store.Connection
	for _, target := range []*storetest.Target{a, b} {
		u, _ := url.Parse(target.DSN())
		q := u.Query()
		schema := q.Get("search_path")
		q.Set("search_path", prefix.Query().Get("search_path")+","+schema)
		u.RawQuery = q.Encode()
		st, err := store.OpenExisting(t.Context(), store.DialectPostgres, u.String())
		if err != nil {
			t.Fatal(err)
		}
		got, err := st.Connection(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(got.Target, "/"+schema) {
			t.Fatalf("identity %q does not name table schema %q", got.Target, schema)
		}
		ids = append(ids, got)
	}
	if ids[0] == ids[1] || ids[0].Created != ids[1].Created {
		t.Fatalf("restored schemas must retain distinct targets: %+v", ids)
	}
}

func TestPostgresExistingSessionCannotReconnectAfterVerification(t *testing.T) {
	target := storetest.NewPostgres(t)
	admin := target.Open(t)
	u, _ := url.Parse(target.DSN())
	q := u.Query()
	app := "admin_" + storetest.Unique()
	q.Set("application_name", app)
	u.RawQuery = q.Encode()
	st, err := store.OpenExisting(t.Context(), store.DialectPostgres, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.Connection(t.Context()); err != nil {
		t.Fatal(err)
	}
	tenant, err := st.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.SQL(t, admin).ExecContext(t.Context(), `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name = $1`, app); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.HoldByOperator(t.Context(), "test", "lost verified session", time.Now()); err == nil {
		t.Fatal("admin mutation reconnected after its verified connection was terminated")
	}
}

func TestPostgresIdentityRetainsUnixSocketEndpoint(t *testing.T) {
	target := storetest.NewPostgres(t)
	admin := target.Open(t)
	var dirs, port string
	if err := teststore.SQL(t, admin).QueryRowContext(t.Context(), `SELECT current_setting('unix_socket_directories'), current_setting('port')`).Scan(&dirs, &port); err != nil {
		t.Fatal(err)
	}
	var socketDir string
	for _, dir := range strings.Split(dirs, ",") {
		dir = strings.TrimSpace(dir)
		if _, err := os.Stat(filepath.Join(dir, ".s.PGSQL."+port)); err == nil {
			socketDir = dir
			break
		}
	}
	if socketDir == "" {
		t.Skip("PostgreSQL fixture has no locally accessible Unix socket")
	}
	u, _ := url.Parse(target.DSN())
	u.Host = ""
	q := u.Query()
	q.Set("host", socketDir)
	q.Set("port", port)
	u.RawQuery = q.Encode()
	st, err := store.OpenExisting(t.Context(), store.DialectPostgres, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	id, err := st.Connection(t.Context())
	if err != nil || !strings.Contains(id.Target, "unix:"+filepath.Join(socketDir, ".s.PGSQL."+port)) {
		t.Fatalf("identity = %+v, %v; want actual Unix socket endpoint", id, err)
	}
}
