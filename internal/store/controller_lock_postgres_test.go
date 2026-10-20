package store_test

import (
	"context"
	"database/sql"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestPostgresControllerPromotionFencesWrites(t *testing.T) {
	target := storetest.NewPostgres(t)
	observer := teststore.SQL(t, target.Open(t))
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	dsn := func(name string) string {
		u, err := url.Parse(target.DSN())
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("application_name", name)
		u.RawQuery = q.Encode()
		return u.String()
	}
	firstName, secondName := "active_"+storetest.Unique(), "standby_"+storetest.Unique()
	fenced := make(chan error, 2)
	first, err := store.OpenControllerPostgres(ctx, dsn(firstName), func(err error) {
		// Keep the former process alive to prove fencing precedes its exit.
		if strings.Contains(err.Error(), "controller epoch lost") {
			fenced <- err
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	tenant, err := first.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	held, release, committed := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	released := false
	defer func() {
		cancel()
		if !released {
			close(release)
		}
	}()
	go func() {
		committed <- first.InTxForTest(ctx, func(tx *sql.Tx) error {
			close(held)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES ('before_promotion', $1)`, []byte("1"))
			return err
		})
	}()
	select {
	case <-held:
	case err := <-committed:
		t.Fatalf("start in-flight write: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := observer.ExecContext(ctx, `SELECT pg_terminate_backend(l.pid) FROM pg_locks l JOIN pg_stat_activity a ON a.pid=l.pid WHERE a.application_name=$1 AND l.locktype='advisory' AND l.granted`, firstName); err != nil {
		t.Fatal(err)
	}
	type opened struct {
		st  *store.Store
		err error
	}
	promoted := make(chan opened, 1)
	go func() {
		st, err := store.OpenControllerPostgres(ctx, dsn(secondName), func(err error) { t.Error(err) })
		promoted <- opened{st, err}
	}()
	for {
		var waiting bool
		if err := observer.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock' AND query LIKE 'INSERT INTO settings%')`, secondName).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case result := <-promoted:
			if result.st != nil {
				_ = result.st.Close()
			}
			t.Fatalf("promotion passed an in-flight write: %v", result.err)
		default:
		}
	}
	close(release)
	released = true
	if err := <-committed; err != nil {
		t.Fatal(err)
	}
	result := <-promoted
	if result.err != nil {
		t.Fatal(result.err)
	}
	defer func() { _ = result.st.Close() }()
	if _, err := first.AsOperator().SeedRunRetention(ctx, 14); err == nil {
		t.Fatal("former controller committed a standalone write after promotion")
	}
	if _, err := tenant.HoldByOperator(ctx, "stale", "after promotion", time.Now()); err == nil {
		t.Fatal("former controller committed a transaction after promotion")
	}
	if len(fenced) != 2 {
		t.Fatalf("epoch mismatches did not invoke fail-stop: %d", len(fenced))
	}
	var count int
	if err := observer.QueryRowContext(ctx, `SELECT COUNT(*) FROM settings WHERE key='before_promotion'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("in-flight write was not committed before promotion: %d, %v", count, err)
	}
}
