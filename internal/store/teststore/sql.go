package teststore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// safety: a fixture writes through its own connection, so the store hands
// no raw handle to any holder; the location is known only where the
// fixture opened it.
type location struct {
	dialect store.Dialect
	dsn     string
}

var (
	locations sync.Map // *store.Store -> location
	pools     sync.Map // poolKey -> *sql.DB
)

type poolKey struct {
	t  T
	st *store.Store
}

// OpenExisting opens path with store.Open and records it for SQL. Use it
// to reopen or migrate a database a test already created.
func OpenExisting(path string, opts ...store.Option) (*store.Store, error) {
	st, err := store.Open(path, opts...)
	if err != nil {
		return nil, err
	}
	RegisterSQLite(st, path)
	return st, nil
}

// OpenPostgres opens dsn with store.OpenPostgres and records it for SQL.
func OpenPostgres(ctx context.Context, dsn string, opts ...store.Option) (*store.Store, error) {
	st, err := store.OpenPostgres(ctx, dsn, opts...)
	if err != nil {
		return nil, err
	}
	locations.Store(st, location{dialect: store.DialectPostgres, dsn: dsn})
	return st, nil
}

// RegisterSQLite records the file behind a store that production code
// opened, so SQL can reach it.
func RegisterSQLite(st *store.Store, path string) {
	locations.Store(st, location{dialect: store.DialectSQLite, dsn: sqliteDSN(path)})
}

func sqliteDSN(path string) string {
	uri := "file:" + (&url.URL{Path: path}).EscapedPath()
	if strings.HasPrefix(uri, "file://") {
		uri = "file://" + strings.TrimPrefix(uri, "file:")
	}
	return fmt.Sprintf("%s?_txlock=immediate&_pragma=busy_timeout(%d)&_pragma=foreign_keys(on)", uri, store.DefaultBusyTimeoutMS)
}

// SQL returns a connection of the fixture's own to the database behind st.
// It closes when t ends. Queries use the store's dialect placeholders.
func SQL(t T, st *store.Store) *sql.DB {
	t.Helper()
	key := poolKey{t: t, st: st}
	if db, ok := pools.Load(key); ok {
		return db.(*sql.DB)
	}
	db, err := open(st)
	if err != nil {
		t.Fatal(err)
	}
	if existing, loaded := pools.LoadOrStore(key, db); loaded {
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		return existing.(*sql.DB)
	}
	t.Cleanup(func() {
		pools.Delete(key)
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	})
	return db
}

// SQLite returns a fixture connection to the SQLite file at path.
func SQLite(t T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	})
	return db
}

func open(st *store.Store) (*sql.DB, error) {
	value, ok := locations.Load(st)
	if !ok {
		return nil, fmt.Errorf("teststore: store was not opened through teststore; open it with teststore.Open, OpenExisting or OpenPostgres, or call RegisterSQLite")
	}
	loc := value.(location)
	driver := "sqlite"
	if loc.dialect == store.DialectPostgres {
		driver = "pgx"
	}
	db, err := sql.Open(driver, loc.dsn)
	if err != nil {
		return nil, err
	}
	db.SetConnMaxIdleTime(time.Second)
	return db, nil
}

// Exec runs one statement through a short-lived fixture connection, for
// seeders that have no test to own a pool.
func Exec(ctx context.Context, st *store.Store, query string, args ...any) (_ sql.Result, err error) {
	db, err := open(st)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	result, err := db.ExecContext(ctx, rebind(st, query), args...)
	if err != nil {
		return nil, err
	}
	if err := projectClaimRunIdentities(ctx, db, st, query); err != nil {
		return nil, err
	}
	return result, nil
}

func rebind(st *store.Store, query string) string {
	if st.Dialect() != store.DialectPostgres || !strings.Contains(query, "?") {
		return query
	}
	var b strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// SeedTeam registers team the way a fixture needs it: no account, no
// tombstone check and no billing setup.
func SeedTeam(t T, st *store.Store, team store.Team) {
	t.Helper()
	if _, err := Exec(t.Context(), st, `INSERT INTO teams(name,created_at,updated_at) VALUES (?,1,1) ON CONFLICT(name) DO NOTHING`, string(team)); err != nil {
		t.Fatal(err)
	}
	if _, err := Exec(t.Context(), st, `INSERT INTO team_counters(team) VALUES (?) ON CONFLICT(team) DO NOTHING`, string(team)); err != nil {
		t.Fatal(err)
	}
}

// SeedGitHubInstallation binds an installation to team with no webhook or
// connect authority.
func SeedGitHubInstallation(t T, st *store.Store, team store.Team, in store.GitHubAppInstallation, now time.Time) store.GitHubAppInstallation {
	t.Helper()
	suspended := 0
	if in.Suspended {
		suspended = 1
	}
	at := now.UTC().UnixNano()
	if _, err := Exec(t.Context(), st, `INSERT INTO github_app_installations(installation_id,team,account_id,account_login,account_type,suspended,connected_by,github_user_id,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		in.InstallationID, string(team), in.AccountID, in.AccountLogin, in.AccountType, suspended, in.ConnectedBy, in.GitHubUserID, at, at); err != nil {
		t.Fatal(err)
	}
	in.Team = team
	in.CreatedAt, in.UpdatedAt = now.UTC(), now.UTC()
	return in
}

func projectClaimRunIdentities(ctx context.Context, db *sql.DB, st *store.Store, query string) error {
	upper := strings.ToUpper(strings.TrimSpace(query))
	if !strings.HasPrefix(upper, "INSERT INTO RUNS") && !(strings.HasPrefix(upper, "UPDATE RUNS") && strings.Contains(upper, "REPO_")) {
		return nil
	}
	rows, err := db.QueryContext(ctx, `SELECT id,repo_key,repo_display,request_json FROM runs`)
	if err != nil {
		return err
	}
	type row struct {
		id, key, display string
		request          []byte
	}
	var runs []row
	for rows.Next() {
		var v row
		if err = rows.Scan(&v.id, &v.key, &v.display, &v.request); err != nil {
			return errors.Join(err, rows.Close())
		}
		runs = append(runs, v)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, v := range runs {
		if v.display == "" {
			var request struct {
				Request struct {
					Git struct {
						Repository struct {
							Display string `json:"display"`
						} `json:"repository"`
					} `json:"git"`
				} `json:"request"`
			}
			if err = json.Unmarshal(v.request, &request); err != nil {
				return err
			}
			v.display = request.Request.Git.Repository.Display
		}
		identity, err := sourceurl.RoutingIdentity(v.key, v.display)
		if err != nil {
			identity = ""
		}
		if _, err = db.ExecContext(ctx, rebind(st, `UPDATE runs SET repo_identity=? WHERE id=?`), identity, v.id); err != nil {
			return err
		}
	}
	return nil
}
