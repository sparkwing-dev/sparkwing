package storetest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestConcurrentPostgresFixturesKeepTheirRowsPrivate(t *testing.T) {
	PostgresURL(t)
	for i := 0; i < 6; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			st := OpenPostgres(t)
			tenant := teststore.ForTeam(t, st, store.DefaultTeam)
			run := store.Run{RunSummary: runstate.RunSummary{ID: "same-id", Pipeline: t.Name(), Status: "running", StartedAt: time.Now()}}
			if err := teststore.SeedRun(st, t.Context(), store.DefaultTeam, run); err != nil {
				t.Fatal(err)
			}
			got, err := tenant.GetRun(t.Context(), run.ID)
			if err != nil || got == nil || got.Pipeline != run.Pipeline {
				t.Fatalf("run=%+v error=%v", got, err)
			}
		})
	}
}

func TestReadyPostgresFixtureDoesNotCopyAnotherFixturesRows(t *testing.T) {
	first := OpenPostgres(t)
	run := store.Run{RunSummary: runstate.RunSummary{ID: "private-run", Pipeline: "p", Status: "running", StartedAt: time.Now()}}
	if err := teststore.SeedRun(first, t.Context(), store.DefaultTeam, run); err != nil {
		t.Fatal(err)
	}
	second := OpenPostgres(t)
	got, err := teststore.ForTeam(t, second, store.DefaultTeam).GetRun(t.Context(), run.ID)
	if !errors.Is(err, store.ErrNotFound) || got != nil {
		t.Fatalf("second fixture inherited run=%+v error=%v", got, err)
	}
}

func TestRawPostgresTargetIsEmptyAndSurvivesReopening(t *testing.T) {
	target := NewPostgres(t)
	db, err := sql.Open("pgx", target.DSN())
	if err != nil {
		t.Fatal(err)
	}
	var tables int
	err = db.QueryRowContext(t.Context(), "SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema()").Scan(&tables)
	if closeErr := db.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil || tables != 0 {
		t.Fatalf("raw target tables=%d error=%v", tables, err)
	}
	first := target.Open(t)
	run := store.Run{RunSummary: runstate.RunSummary{ID: "reopened-run", Pipeline: "p", Status: "running", StartedAt: time.Now()}}
	if err := teststore.SeedRun(first, t.Context(), store.DefaultTeam, run); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := target.Open(t)
	got, err := teststore.ForTeam(t, second, store.DefaultTeam).GetRun(t.Context(), run.ID)
	if err != nil || got == nil {
		t.Fatalf("reopened run=%+v error=%v", got, err)
	}
}

func TestPostgresFixtureWorksWithoutCreateDatabasePermission(t *testing.T) {
	base := PostgresURL(t)
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Close(); err != nil {
			t.Error(err)
		}
	})
	var canCreateRole bool
	if err := admin.QueryRowContext(t.Context(), "SELECT rolcreaterole OR rolsuper FROM pg_roles WHERE rolname=current_user").Scan(&canCreateRole); err != nil {
		t.Fatal(err)
	}
	if !canCreateRole {
		t.Skip("configured test role cannot create a restricted role")
	}
	role, err := postgresFixtureName("role")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(t.Context(), "CREATE ROLE "+role+" LOGIN PASSWORD 'fixture-only'"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec("DROP OWNED BY " + role); err != nil {
			t.Error(err)
		}
		if _, err := admin.Exec("DROP ROLE " + role); err != nil {
			t.Error(err)
		}
	})
	var database string
	if err := admin.QueryRowContext(t.Context(), "SELECT current_database()").Scan(&database); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(t.Context(), "GRANT CREATE ON DATABASE "+pgx.Identifier{database}.Sanitize()+" TO "+role); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(role, "fixture-only")
	t.Setenv(URLEnv, u.String())
	st := OpenPostgres(t)
	if _, err := teststore.ForTeam(t, st, store.DefaultTeam).GetRun(t.Context(), "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestPostgresFixtureURLBindsDatabaseAndSchema(t *testing.T) {
	raw, err := postgresFixtureURL("postgres://fixture:fixture@localhost/source?sslmode=disable&dbname=other&database=also_other&search_path=foreign", "owned_clone")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := pgx.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database != "owned_clone" || cfg.RuntimeParams["search_path"] != postgresFixtureSchema {
		t.Fatalf("database=%q search_path=%q", cfg.Database, cfg.RuntimeParams["search_path"])
	}
	if strings.Contains(raw, "foreign") || strings.Contains(raw, "also_other") {
		t.Fatal("original location escaped the fixture binding")
	}
}

func TestPostgresCloneDatabasesIsolateSettingsAndExtensions(t *testing.T) {
	base := PostgresURL(t)
	template, err := postgresTemplate(t, t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	if template == "" {
		t.Skip("configured role cannot create fixture databases")
	}
	first := newClonedPostgres(t, base)
	a, err := sql.Open("pgx", first.DSN())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	var aName string
	if err := a.QueryRowContext(t.Context(), "SELECT current_database()").Scan(&aName); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ExecContext(t.Context(), "ALTER DATABASE "+pgx.Identifier{aName}.Sanitize()+" SET application_name='fixture-mutated'"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ExecContext(t.Context(), "CREATE EXTENSION hstore"); err != nil {
		t.Fatal(err)
	}
	second := newClonedPostgres(t, base)
	b, err := sql.Open("pgx", second.DSN())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var bName string
	if err := b.QueryRowContext(t.Context(), "SELECT current_database()").Scan(&bName); err != nil {
		t.Fatal(err)
	}
	if aName == bName || aName == template || bName == template {
		t.Fatalf("fixtures share a database: first=%s second=%s template=%s", aName, bName, template)
	}
	for _, tc := range []struct {
		name string
		db   *sql.DB
		want int
	}{{"mutated", a, 1}, {"isolated", b, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			var extensions, settings int
			err := tc.db.QueryRowContext(t.Context(), `SELECT
    (SELECT count(*) FROM pg_extension WHERE extname='hstore'),
    (SELECT count(*) FROM pg_db_role_setting WHERE setdatabase=(SELECT oid FROM pg_database WHERE datname=current_database())
     AND setrole=0 AND setconfig @> ARRAY['application_name=fixture-mutated'])`).Scan(&extensions, &settings)
			if err != nil {
				t.Fatal(err)
			}
			if extensions != tc.want || settings != tc.want {
				t.Fatalf("extensions=%d settings=%d, want %d", extensions, settings, tc.want)
			}
		})
	}
}

func TestPostgresTemplateHasNoSessionsAndRejectsConnections(t *testing.T) {
	base := PostgresURL(t)
	template, err := postgresTemplate(t, t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	if template == "" {
		t.Skip("configured role cannot create fixture databases")
	}
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var allow, isTemplate bool
	var sessions int
	err = admin.QueryRowContext(t.Context(), `SELECT datallowconn, datistemplate,
  (SELECT count(*) FROM pg_stat_activity WHERE datname=$1)
  FROM pg_database WHERE datname=$1`, template).Scan(&allow, &isTemplate, &sessions)
	if err != nil {
		t.Fatal(err)
	}
	if allow || isTemplate || sessions != 0 {
		t.Fatalf("template allow_connections=%t is_template=%t sessions=%d", allow, isTemplate, sessions)
	}
	dsn, err := postgresFixtureURL(base, template)
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer blocked.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err = blocked.PingContext(ctx)
	var refused *pgconn.PgError
	if !errors.As(err, &refused) || refused.Code != "55000" {
		t.Fatalf("template connection error=%v, want SQLSTATE 55000", err)
	}
}
