package storetest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

const postgresFixtureSchema = "sw_fixture"

type postgresTemplateEntry struct {
	name string
	err  error
}

var postgresTemplates = struct {
	sync.Mutex
	entries map[string]postgresTemplateEntry
	closed  bool
}{entries: make(map[string]postgresTemplateEntry)}

func newClonedPostgres(t *testing.T, base string) *Target {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	template, err := postgresTemplate(t, ctx, base)
	if err != nil {
		t.Fatalf("prepare postgres fixture template: %v", err)
	}
	if template == "" {
		return &Target{t: t, dialect: store.DialectPostgres, dsn: newSchema(t, base)}
	}
	name, err := postgresFixtureName("clone")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := postgresFixtureExec(ctx, base, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop postgres fixture %s: %v", name, err)
		}
	})
	if err := postgresFixtureExec(ctx, base, "CREATE DATABASE "+name+" TEMPLATE "+template); err != nil {
		t.Fatalf("clone postgres fixture: %v", err)
	}
	dsn, err := postgresFixtureURL(base, name)
	if err != nil {
		t.Fatal(err)
	}
	return &Target{t: t, dialect: store.DialectPostgres, dsn: dsn}
}

func postgresTemplate(t *testing.T, ctx context.Context, base string) (name string, err error) {
	postgresTemplates.Lock()
	defer postgresTemplates.Unlock()
	if postgresTemplates.closed {
		return "", errors.New("postgres fixtures are closed")
	}
	if entry, ok := postgresTemplates.entries[base]; ok {
		return entry.name, entry.err
	}
	name, err = postgresFixtureName("template")
	if err != nil {
		return "", err
	}
	// safety: retain ownership even when CREATE committed but its reply was lost.
	defer func() { postgresTemplates.entries[base] = postgresTemplateEntry{name: name, err: err} }()
	if err = postgresFixtureExec(ctx, base, "CREATE DATABASE "+name); err != nil {
		var denied *pgconn.PgError
		if errors.As(err, &denied) && denied.Code == "42501" {
			return "", nil
		}
		return name, err
	}
	dsn, err := postgresFixtureURL(base, name)
	if err != nil {
		return name, err
	}
	if err = postgresFixtureExec(ctx, dsn, "CREATE SCHEMA "+postgresFixtureSchema); err != nil {
		return name, err
	}
	st, err := teststore.OpenPostgres(t, ctx, dsn, defaultOptions()...)
	if err != nil {
		return name, err
	}
	if err = st.Close(); err != nil {
		return name, err
	}
	// safety: a template must have no sessions; its baseline remains immutable between clones.
	if err = postgresFixtureExec(ctx, base, "ALTER DATABASE "+name+" ALLOW_CONNECTIONS false"); err != nil {
		return name, err
	}
	err = postgresFixtureExec(ctx, base, "SELECT pg_terminate_backend(pid, 5000) FROM pg_stat_activity WHERE datname='"+name+"'")
	return name, err
}

func postgresFixtureName(kind string) (string, error) {
	var entropy [12]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("sw_fixture_%s_%x", kind, entropy), nil
}

func postgresFixtureURL(base, name string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", errors.New("invalid postgres fixture URL")
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return "", errors.New("postgres fixture requires a postgres URL")
	}
	u.Path = "/" + name
	q := u.Query()
	for _, key := range []string{"dbname", "database"} {
		if q.Has(key) {
			q.Set(key, name)
		}
	}
	q.Set("search_path", postgresFixtureSchema)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func postgresFixtureExec(ctx context.Context, dsn, query string) (err error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	_, err = db.ExecContext(ctx, query)
	return err
}

func closePostgresTemplates() error {
	postgresTemplates.Lock()
	entries := postgresTemplates.entries
	postgresTemplates.entries = nil
	postgresTemplates.closed = true
	postgresTemplates.Unlock()
	var result error
	for base, entry := range entries {
		if entry.name == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := postgresFixtureExec(ctx, base, "DROP DATABASE IF EXISTS "+entry.name+" WITH (FORCE)")
		cancel()
		result = errors.Join(result, err)
	}
	return result
}
