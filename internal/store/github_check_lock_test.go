package store_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

// Two controllers reporting one run take its GitHub report lock in turn: the
// second waits while the first holds it, and enters seeing the check run the
// first created. On Postgres the two hold separate connection pools, as two
// processes do.
func TestReportGitHubCheckAdmitsOneHolderAtATime(t *testing.T) {
	target := storetest.New(t)
	first := target.Open(t)
	second := first
	if first.Dialect() == store.DialectPostgres {
		second = target.Open(t)
	}
	ctx := context.Background()
	a, accepted, err := storePlanSealRequest(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	runID := accepted.RunID
	b, err := second.ForTeam(ctx, a.Team())
	if err != nil {
		t.Fatal(err)
	}

	entered, release, secondIn := make(chan struct{}), make(chan struct{}), make(chan struct{})
	done := make(chan error, 2)
	go func() {
		done <- a.ReportGitHubCheck(ctx, runID, func(_ context.Context, id int64) (int64, error) {
			if id != 0 {
				t.Errorf("first holder saw check run %d, want none", id)
			}
			close(entered)
			<-release
			return 41, nil
		})
	}()
	<-entered
	go func() {
		done <- b.ReportGitHubCheck(ctx, runID, func(_ context.Context, id int64) (int64, error) {
			if id != 41 {
				t.Errorf("second holder saw check run %d, want the first holder's 41", id)
			}
			close(secondIn)
			return 0, nil
		})
	}()
	if first.Dialect() == store.DialectPostgres {
		waitForAdvisoryWaiter(t, first, "sparkwing/github-check/"+string(a.Team())+"/"+runID, secondIn)
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

// A holder whose context ends after it created a check run records nothing,
// so the next holder sees no id rather than one recorded after it let go.
func TestReportGitHubCheckRecordsNothingPastTheHoldersDeadline(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	tenant, accepted, err := storePlanSealRequest(ctx, st)
	if err != nil {
		t.Fatal(err)
	}

	held, cancel := context.WithCancel(ctx)
	if err := tenant.ReportGitHubCheck(held, accepted.RunID, func(context.Context, int64) (int64, error) {
		cancel()
		return 99, nil
	}); err == nil {
		t.Fatal("a holder past its deadline reported success")
	}
	if id, err := tenant.GitHubCheckRun(ctx, accepted.RunID); err != nil || id != 0 {
		t.Fatalf("recorded check run = %d, %v; want none", id, err)
	}
}

// The report lock outlives no transaction: while fn would call GitHub, the
// holder's connection is idle rather than idle in a transaction.
func TestReportGitHubCheckHoldsNoTransactionDuringFn(t *testing.T) {
	st := storetest.OpenPostgres(t)
	st.DB().SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	tenant, accepted, err := storePlanSealRequest(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	key := "sparkwing/github-check/" + string(tenant.Team()) + "/" + accepted.RunID
	if err := tenant.ReportGitHubCheck(ctx, accepted.RunID, func(ctx context.Context, _ int64) (int64, error) {
		probe, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		var state string
		if err := st.DB().QueryRowContext(probe, `SELECT a.state FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
			WHERE l.locktype = 'advisory' AND l.granted
			  AND l.objid::text::bigint = (hashtext($1)::bigint & 4294967295)`, key).Scan(&state); err != nil {
			return 0, err
		}
		if state != "idle" {
			t.Errorf("report lock holder is %q while fn runs, want idle", state)
		}
		if err := tenant.UseGitHubCommitStatuses(probe, accepted.RunID); err != nil {
			return 0, err
		}
		return 7, nil
	}); err != nil {
		t.Fatal(err)
	}
	if id, err := tenant.GitHubCheckRun(ctx, accepted.RunID); err != nil || id != 7 {
		t.Fatalf("recorded check run = %d, %v; want 7", id, err)
	}
}

func TestReportGitHubCheckConcurrentReportsKeepOperationalCapacity(t *testing.T) {
	st := storetest.OpenPostgres(t)
	st.DB().SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	tenant, accepted, err := storePlanSealRequest(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 25)
	var active atomic.Int32
	report := func(first bool) {
		done <- tenant.ReportGitHubCheck(ctx, accepted.RunID, func(ctx context.Context, id int64) (int64, error) {
			if active.Add(1) != 1 {
				t.Error("concurrent report callbacks")
			}
			defer active.Add(-1)
			if first {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return 0, ctx.Err()
				}
			} else if id != 41 {
				return 0, fmt.Errorf("recorded check run = %d, want 41", id)
			}
			if _, err := tenant.GitHubCommitStatuses(ctx, accepted.RunID); err != nil {
				return 0, err
			}
			if err := tenant.UseGitHubCommitStatuses(ctx, accepted.RunID); err != nil {
				return 0, err
			}
			return 41, nil
		})
	}
	go report(true)
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("initial report: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for range 24 {
		go report(false)
	}
	key := "sparkwing/github-check/" + string(tenant.Team()) + "/" + accepted.RunID
	for {
		var blocked int
		if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_locks l JOIN pg_stat_activity a ON a.pid=l.pid
			WHERE l.locktype='advisory' AND l.granted AND l.objid::text::bigint=(hashtext($1)::bigint & 4294967295)
			AND EXISTS (SELECT 1 FROM pg_stat_activity waiter WHERE a.pid=ANY(pg_blocking_pids(waiter.pid)))`, key).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked > 0 {
			break
		}
	}
	close(release)
	for range 25 {
		if err := <-done; err != nil {
			t.Fatalf("concurrent report: %v", err)
		}
	}
	if id, err := tenant.GitHubCheckRun(ctx, accepted.RunID); err != nil || id != 41 {
		t.Fatalf("recorded check run = %d, %v; want 41", id, err)
	}
}
