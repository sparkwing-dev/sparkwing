package store_test

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// Two controllers reporting one run take its GitHub report lock in turn: the
// second waits while the first holds it and enters once it is released. On
// Postgres the two hold separate connection pools, as two processes do.
func TestLockGitHubCheckAdmitsOneHolderAtATime(t *testing.T) {
	target := storetest.New(t)
	first := target.Open(t)
	second := first
	if first.Dialect() == store.DialectPostgres {
		second = target.Open(t)
	}
	ctx := context.Background()
	a, err := first.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}

	entered, release, secondIn := make(chan struct{}), make(chan struct{}), make(chan struct{})
	done := make(chan error, 2)
	go func() {
		done <- a.LockGitHubCheck(ctx, "run-1", func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	go func() {
		done <- b.LockGitHubCheck(ctx, "run-1", func(context.Context) error {
			close(secondIn)
			return nil
		})
	}()
	if first.Dialect() == store.DialectPostgres {
		waitForAdvisoryWaiter(t, first, "sparkwing/github-check/"+string(store.DefaultTeam)+"/run-1", secondIn)
	}
	select {
	case <-secondIn:
		t.Fatal("the second holder entered while the first held the lock")
	default:
	}
	close(release)
	<-secondIn
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("lock: %v", err)
		}
	}
}

func waitForAdvisoryWaiter(t *testing.T, st *store.Store, key string, entered <-chan struct{}) {
	t.Helper()
	for {
		select {
		case <-entered:
			t.Fatal("the second holder entered while the first held the lock")
		default:
		}
		var waiting int
		if err := st.DB().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM pg_locks
			WHERE locktype = 'advisory' AND NOT granted
			  AND objid::text::bigint = (hashtext($1)::bigint & 4294967295)`, key).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			return
		}
	}
}
