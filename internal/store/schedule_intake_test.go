package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func scheduleIntakeFixture(t *testing.T, suffix uint32) (*Tenant, CronSchedule, time.Time) {
	t.Helper()
	s := authorityStore(t)
	now := time.Unix(1700000000, 37).UTC()
	s.clock = func() time.Time { return now }
	tenant := &Tenant{s: s, team: "a"}
	tenant.s.SetTestClock(t, func() time.Time { return now })
	sched, _, err := tenant.ArmCronSchedule(t.Context(), CronSchedule{
		ID: fmt.Sprintf("schedule-%x", suffix), RepoPath: fmt.Sprintf("https://github.com/acme/repo-%x.git", suffix), GitHubInstallationID: 7, GitHubRepositoryID: 701,
		Pipeline: "nightly", Cron: "* * * * *", TZ: "UTC", Where: CronWhereController,
		GitBranch: "main", LockedRef: "0123456789abcdef0123456789abcdef01234567", ArmedBy: "owner@example.com",
		Args: map[string]string{"message": fmt.Sprintf("hello-%x", suffix)},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.intakePolicy = fakeIntakePolicy(func(ctx context.Context, resolver capability.AuthorityResolver, in IntakeRequest) (IntakePolicyFacts, error) {
		location, err := VerifiedScheduleIntake(ctx, resolver, in)
		return IntakePolicyFacts{SourceLocation: &location, LogRetentionClass: "r30", LogRetentionDays: 30}, err
	})
	rewriteSchemaExec(t, s.db, `UPDATE cron_schedules SET github_installation_id=7,github_repository_id=701`)
	rewriteSchemaExec(t, s.db, `INSERT INTO github_app_installations(installation_id,team,account_id,account_login,account_type,created_at,updated_at) VALUES (7,'a',1,'acme','Organization',1,1) ON CONFLICT DO NOTHING`)
	rewriteSchemaExec(t, s.db, `INSERT INTO github_app_automation(team,repo_key,repository_id,repository,installation_id,enabled_by,enabled_at) VALUES ('a',?,701,'acme/repo',7,'owner',1)`, sched.RepositoryKey)
	sched, err = tenant.GetCronSchedule(t.Context(), sched.ID)
	if err != nil {
		t.Fatal(err)
	}
	return tenant, sched, now
}

func TestScheduleAdmissionSealsReplaysAndRollsBack(t *testing.T) {
	tenant, sched, now := scheduleIntakeFixture(t, 1)
	first, err := tenant.AdmitScheduledRun(t.Context(), sched, now)
	if err != nil {
		t.Fatal(err)
	}
	in := first.Request.Request
	if first.Number != 1 || first.OwnerUnit == nil || first.Request.Hash == "" || in.Team != "a" || in.Origin != runspec.OriginSchedule || in.Git.App.Set || in.Git.DefaultBranch.Set || in.Source.Location.RemoteURL != sched.RepoPath || in.Git.Repository.Key != sched.RepositoryKey || in.Git.Repository.Display != "acme/repo-1" || in.Git.SHA != sched.LockedRef || in.Git.Ref != sched.GitBranch || !reflect.DeepEqual(in.RequestedArgs, sched.Args) {
		t.Fatalf("sealed schedule = %+v", first)
	}
	if replay, err := tenant.AdmitScheduledRun(t.Context(), sched, now); err != nil || !replay.Replayed || replay.RunID != first.RunID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	rewriteSchemaCount(t, tenant.s.db, "runs", 1)
	rewriteSchemaCount(t, tenant.s.db, "triggers", 1)
	rewriteSchemaExec(t, tenant.s.db, `CREATE TRIGGER fail_schedule_effect BEFORE INSERT ON run_effects BEGIN SELECT RAISE(ABORT,'refused'); END`)
	if got, err := tenant.AdmitScheduledRun(t.Context(), sched, now.Add(time.Nanosecond)); err == nil || got.RunID != "" {
		t.Fatalf("failed effect committed: %+v %v", got, err)
	}
	rewriteSchemaExec(t, tenant.s.db, `DROP TRIGGER fail_schedule_effect`)
	if next, err := tenant.AdmitScheduledRun(t.Context(), sched, now.Add(time.Nanosecond)); err != nil || next.Number != 2 || next.RunID == first.RunID {
		t.Fatalf("next instant=%+v err=%v", next, err)
	}
	if _, err := tenant.AdmitRun(t.Context(), IntakeRequest{Authority: first.Request.AuthorityRef, Request: in}); err == nil {
		t.Fatal("ordinary intake manufactured schedule authority")
	}

	rewriteSchemaExec(t, tenant.s.db, `UPDATE cron_schedules SET args=?`, `{"message":"changed"}`)
	current, err := tenant.GetCronSchedule(t.Context(), sched.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := tenant.AdmitScheduledRun(t.Context(), current, now); err == nil || replay.RunID != "" {
		t.Fatalf("changed effective declaration replayed: %+v %v", replay, err)
	}
	rewriteSchemaCount(t, tenant.s.db, "runs", 2)
}

func TestScheduleAdmissionRequiresCurrentTenantAndDeclarationProperty(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 2.9s measured in the short class; the full suite runs it")
	}
	property := func(seed uint32) bool {
		for mode := range 18 {
			if !t.Run(fmt.Sprintf("%d/%d", seed, mode), func(t *testing.T) {
				tenant, sched, now := scheduleIntakeFixture(t, seed)
				switch mode {
				case 0:
					tenant = &Tenant{s: tenant.s, team: "b"}
				case 1:
					sched.Team = "b"
				case 2:
					sched.RepoPath += "/forged"
				case 3:
					sched.RepositoryKey = "gh:999"
				case 4:
					sched.Pipeline += "-forged"
				case 5:
					sched.Args["message"] += "-forged"
				case 6:
					sched.LockedRef = "forged"
				case 7:
					sched.ArmedAt = sched.ArmedAt.Add(time.Nanosecond)
				case 8:
					rewriteSchemaExec(t, tenant.s.db, `DELETE FROM cron_schedules`)
				case 9:
					rewriteSchemaExec(t, tenant.s.db, `UPDATE cron_schedules SET declared=0`)
				case 10:
					tenant.s.intakePolicy = fakeIntakePolicy(func(ctx context.Context, r capability.AuthorityResolver, in IntakeRequest) (IntakePolicyFacts, error) {
						_, err := r.(*storeTx).execContext(ctx, `UPDATE cron_schedules SET git_branch='changed'`)
						location := in.Request.Source.Location
						return IntakePolicyFacts{SourceLocation: &location, LogRetentionClass: "r30", LogRetentionDays: 30}, err
					})
				case 11:
					rewriteSchemaExec(t, tenant.s.db, `INSERT INTO team_deletions(slug,requested_at,state) VALUES ('a',1,'pending')`)
				case 12:
					tenant.s.intakePolicy = fakeIntakePolicy(func(ctx context.Context, r capability.AuthorityResolver, in IntakeRequest) (IntakePolicyFacts, error) {
						_, err := r.(*storeTx).execContext(ctx, `INSERT INTO team_deletions(slug,requested_at,state) VALUES ('a',1,'pending')`)
						location := in.Request.Source.Location
						return IntakePolicyFacts{SourceLocation: &location, LogRetentionClass: "r30", LogRetentionDays: 30}, err
					})
				case 13:
					sched.Paused = !sched.Paused
				case 14:
					sched.GitBranch += "-forged"
				case 15:
					sched.ArmedBy += "-forged"
				case 16:
					sched.Where = CronWhereLocal
				case 17:
					sched.Override = &CronOverride{Args: map[string]string{"injected": "argument"}}

				}
				got, err := tenant.AdmitScheduledRun(t.Context(), sched, now)
				if err == nil || got.RunID != "" || got.OwnerUnit != nil {
					t.Fatalf("stale/foreign declaration admitted: %+v %v", got, err)
				}
				rewriteSchemaCount(t, tenant.s.db, "runs", 0)
				rewriteSchemaCount(t, tenant.s.db, "triggers", 0)
			}) {
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 6}); err != nil {
		t.Fatal(err)
	}
}

func TestScheduleAdmissionRechecksAppConsentProperty(t *testing.T) {
	property := func(suffix uint32) bool {
		for mode := range 4 {
			tenant, sched, now := scheduleIntakeFixture(t, suffix)
			rewriteSchemaExec(t, tenant.s.db, `UPDATE cron_schedules SET github_installation_id=7,github_repository_id=701,repo_key='gh:701',repo_path='https://github.com/acme/repo.git'`)
			rewriteSchemaExec(t, tenant.s.db, `INSERT INTO github_app_installations(installation_id,team,account_id,account_login,account_type,created_at,updated_at) VALUES (7,'a',1,'acme','Organization',1,1) ON CONFLICT DO NOTHING`)
			rewriteSchemaExec(t, tenant.s.db, `UPDATE github_app_automation SET repo_key='gh:701' WHERE team='a'`)
			sched, err := tenant.GetCronSchedule(t.Context(), sched.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tenant.AdmitScheduledRun(t.Context(), sched, now); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case 0:
				rewriteSchemaExec(t, tenant.s.db, `UPDATE github_app_installations SET suspended=1`)
			case 1:
				rewriteSchemaExec(t, tenant.s.db, `DELETE FROM github_app_automation`)
				rewriteSchemaExec(t, tenant.s.db, `UPDATE github_app_installations SET team='b'`)
			case 2:
				rewriteSchemaExec(t, tenant.s.db, `DELETE FROM github_app_automation`)
			case 3:
				tenant.s.intakePolicy = fakeIntakePolicy(func(ctx context.Context, r capability.AuthorityResolver, in IntakeRequest) (IntakePolicyFacts, error) {
					_, err := r.(*storeTx).execContext(ctx, `DELETE FROM github_app_automation`)
					location := in.Request.Source.Location
					return IntakePolicyFacts{SourceLocation: &location, LogRetentionClass: "r30", LogRetentionDays: 30}, err
				})
			}
			if got, err := tenant.AdmitScheduledRun(t.Context(), sched, now.Add(time.Second)); err == nil || got.RunID != "" {
				t.Logf("mode %d revoked App consent admitted=%+v err=%v", mode, got, err)
				return false
			}
			rewriteSchemaCount(t, tenant.s.db, "runs", 1)
			rewriteSchemaCount(t, tenant.s.db, "triggers", 1)
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 6}); err != nil {
		t.Fatal(err)
	}
}

func TestScheduleAdmissionCountsAllSchedulesAgainstArmedBy(t *testing.T) {
	tenant, first, now := scheduleIntakeFixture(t, 1)
	if _, err := tenant.s.AsOperator().SetComputeLimits(t.Context(), map[string]int64{ComputeLimitRunsPerHour: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.AdmitScheduledRun(t.Context(), first, now); err != nil {
		t.Fatal(err)
	}
	tenant.s.SetTestClock(t, func() time.Time { return now })
	second, _, err := tenant.ArmCronSchedule(t.Context(), CronSchedule{ID: "second", RepoPath: first.RepoPath, Pipeline: first.Pipeline, Name: "second", GitBranch: first.GitBranch, LockedRef: first.LockedRef, Cron: first.Cron, TZ: first.TZ, Where: first.Where, ArmedBy: first.ArmedBy, GitHubInstallationID: first.GitHubInstallationID, GitHubRepositoryID: first.GitHubRepositoryID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = tenant.AdmitScheduledRun(t.Context(), second, now)
	var refusal *ComputeLimitError
	if !errors.As(err, &refusal) || refusal.Principal != first.ArmedBy || refusal.Observed != 1 {
		t.Fatalf("second schedule bypassed armer's cap: %v", err)
	}
	rewriteSchemaCount(t, tenant.s.db, "runs", 1)
}

func TestScheduleTickRejectsPausedOrUnconsentedSchedule(t *testing.T) {
	for _, paused := range []bool{false, true} {
		t.Run(fmt.Sprint(paused), func(t *testing.T) {
			tenant, row, now := scheduleIntakeFixture(t, 90)
			if paused {
				rewriteSchemaExec(t, tenant.s.db, `UPDATE cron_schedules SET paused=1`)
			} else {
				rewriteSchemaExec(t, tenant.s.db, `UPDATE cron_schedules SET consent_json='',github_installation_id=0,github_repository_id=0`)
			}
			row, err := tenant.GetCronSchedule(t.Context(), row.ID)
			if err != nil {
				t.Fatal(err)
			}
			got, err := tenant.AdmitScheduledRun(t.Context(), row, now)
			if err == nil || got.RunID != "" {
				t.Fatalf("unconsented/paused schedule fired: %+v %v", got, err)
			}
			rewriteSchemaCount(t, tenant.s.db, "runs", 0)
		})
	}
}

// Run now is a manual run by the member who pressed it: it records that member,
// not the schedule's armer, and keeps the asserted identity existing rules give it.
func TestScheduleRunNowRecordsTheMemberWhoPressedIt(t *testing.T) {
	tenant, armer, body, _ := consentedScheduleFixture(t)
	ids, _, err := tenant.ReplaceAuthorizedSchedules(t.Context(), armer, body)
	if err != nil {
		t.Fatal(err)
	}
	presser := teamAuthorization(t, tenant.s, authorityMember(t, tenant.s, "presser", RoleEditor), tenant.team, "POST", "/api/v1/schedules/{schedule}/run")
	_, id, _, err := tenant.RunAuthorizedSchedule(t.Context(), presser, ids[0], "pressed")
	if err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	var creator string
	if err := tenant.s.db.QueryRow(`SELECT created_principal FROM runs WHERE id=?`, id).Scan(&creator); err != nil {
		t.Fatal(err)
	}
	if actor := run.Request.Request.Actor; actor.ID != "presser" || creator != "presser" {
		t.Fatalf("run now recorded actor %+v and creator %q, want the presser", actor, creator)
	}
	if p := run.Request.Provenance; p.Trust() != runspec.TrustAsserted || p.Writer() != runspec.PrincipalWriter("account", "presser") {
		t.Fatalf("run now provenance = %s %s", p.Trust(), p.Writer())
	}
}
