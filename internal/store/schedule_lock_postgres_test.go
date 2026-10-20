package store_test

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func scheduleLockFixture(t *testing.T, target *storetest.Target) (*store.Store, *store.Tenant, store.CronSchedule, time.Time) {
	t.Helper()
	s := target.Open(t, store.WithPolicies(func(s *store.Store) (store.Policies, error) {
		return controller.New(s, nil).StorePolicies(), nil
	}))
	s.DB().SetMaxOpenConns(8)
	member := signIn(t, s, "planner", "planner@example.test")
	tenant := tenant(t, s, member.PersonalTeam)
	a := storetest.AccountAuthority(t, s, member.Account.ID)
	a.Target.Team = string(tenant.Team())
	remote := "https://93.184.216.34/acme/repo.git"
	body := controllerwire.ScheduleRepoRequest{Mode: controllerwire.ScheduleReplace, RepoURL: remote, Branch: "main", SHA: sourcetest.FixedRemote(t, remote), Schedules: []controllerwire.ScheduleDeclaration{{Pipeline: "nightly", Cron: "* * * * *"}}}
	ids, _, err := tenant.ReplaceAuthorizedSchedules(t.Context(), a, body)
	if err != nil {
		t.Fatal(err)
	}
	now, err := s.TestOnlyDatabaseTime(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	due := now.Truncate(time.Minute)
	if _, err := s.DB().ExecContext(t.Context(), `UPDATE cron_schedules SET cursor_at=$1,next_due_at=$2 WHERE team=$3 AND id=$4`, due.Add(-time.Minute).UnixNano(), due.UnixNano(), tenant.Team(), ids[0]); err != nil {
		t.Fatal(err)
	}
	row, err := tenant.GetCronSchedule(t.Context(), ids[0])
	if err != nil {
		t.Fatal(err)
	}
	return s, tenant, row, due
}

func TestScheduledRunSealsPinnedRemoteSource(t *testing.T) {
	_, tenant, row, due := scheduleLockFixture(t, storetest.New(t))
	admitted, err := tenant.AdmitScheduledRun(t.Context(), row, due)
	if err != nil {
		t.Fatal(err)
	}
	request := admitted.Request.Request
	if request.Git.SHA != row.LockedRef || request.Source.Ref != row.LockedRef || request.Source.Location.RemoteURL != row.RepoPath {
		t.Fatalf("scheduled source changed: %+v", request)
	}
}

func TestPostgresScheduleConcurrentFiresCommitOneRunWithoutDeadlock(t *testing.T) {
	s, tenant, row, due := scheduleLockFixture(t, storetest.NewPostgres(t))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	intake, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = intake.Rollback() }()
	if _, err := intake.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "intake:"+string(tenant.Team())); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := intake.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	type result struct {
		admission store.AdmissionResult
		err       error
	}
	done := make(chan result, 2)
	for range 2 {
		go func() { admission, err := tenant.AdmitScheduledRun(ctx, row, due); done <- result{admission, err} }()
	}
	for blocked := 0; blocked < 2; {
		select {
		case got := <-done:
			t.Fatalf("fire finished before intake unlocked: %v", got.err)
		default:
		}
		err := s.DB().QueryRowContext(ctx, `WITH RECURSIVE blocked AS (
   SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid))
   UNION SELECT a.pid FROM pg_stat_activity a JOIN blocked b ON b.pid=ANY(pg_blocking_pids(a.pid))
  ) SELECT COUNT(*) FROM blocked`, pid).Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		runtime.Gosched()
	}
	if err := intake.Rollback(); err != nil {
		t.Fatal(err)
	}
	first, second := <-done, <-done
	if first.err != nil || second.err != nil || first.admission.RunID == "" || first.admission.RunID != second.admission.RunID {
		t.Fatalf("concurrent fires: %+v %+v", first, second)
	}
	fires, err := tenant.ListCronFires(ctx, row.ID, 10)
	if err != nil || len(fires) != 1 || fires[0].RunID != first.admission.RunID {
		t.Fatalf("committed history: %+v %v", fires, err)
	}
}
