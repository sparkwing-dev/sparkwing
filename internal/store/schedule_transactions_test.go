package store

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type scheduleFixturePolicy struct{ IntakePolicy }

func (p scheduleFixturePolicy) ResolveScheduleSource(_ context.Context, row CronSchedule, request runspec.Request) (runspec.Request, error) {
	repository, err := runspec.CanonicalRepository(runspec.RepoInput{RemoteURL: row.RepoPath})
	request.Version = 1
	request.Git = runspec.Git{Repository: repository, Ref: row.GitBranch, SHA: row.LockedRef}
	request.Source.Ref = request.Git.SHA
	request.Source.Location = runspec.SourceLocation{RemoteURL: row.RepoPath, PipelineDir: ".sparkwing"}
	return request, err
}

func consentedScheduleFixture(t *testing.T) (*Tenant, capability.Authorized, controllerwire.ScheduleRepoRequest, *time.Time) {
	tenant, _, now := intakeFixture(t)
	tenant.s.SetIntakePolicy(scheduleFixturePolicy{fakeIntakePolicy(func(_ context.Context, _ capability.AuthorityResolver, in IntakeRequest) (IntakePolicyFacts, error) {
		return IntakePolicyFacts{Options: &in.Request.Options, SourceLocation: &in.Request.Source.Location, LogRetentionClass: "r30", LogRetentionDays: 30}, nil
	})})
	actor := authorityMember(t, tenant.s, "schedule-owner", RoleEditor)
	a := teamAuthorization(t, tenant.s, actor, tenant.team, "PUT", "/api/v1/schedules/repos")
	body := controllerwire.ScheduleRepoRequest{Mode: controllerwire.ScheduleReplace, RepoURL: "https://gitlab.com/acme/repo.git", Branch: "main", SHA: "0123456789abcdef0123456789abcdef01234567", Schedules: []controllerwire.ScheduleDeclaration{{Pipeline: "nightly", Cron: "* * * * *"}}}
	return tenant, a, body, now
}

func TestScheduledIntakeCommitsOneSealedRunAndHistoryPerDueInstant(t *testing.T) {
	tenant, a, body, now := consentedScheduleFixture(t)
	ids, _, err := tenant.ReplaceAuthorizedSchedules(t.Context(), a, body)
	if err != nil {
		t.Fatal(err)
	}
	row, err := tenant.GetCronSchedule(t.Context(), ids[0])
	if err != nil {
		t.Fatal(err)
	}
	due := *row.NextDueAt
	if _, err := tenant.AdmitScheduledRun(t.Context(), row, due); err == nil {
		t.Fatal("future instant admitted")
	}
	*now = due
	var results [2]AdmissionResult
	var failures [2]error
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() { defer wg.Done(); results[i], failures[i] = tenant.AdmitScheduledRun(t.Context(), row, due) }()
	}
	wg.Wait()
	for _, err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if results[0].RunID == "" || results[0].RunID != results[1].RunID || results[0].Replayed == results[1].Replayed || !reflect.DeepEqual(results[0].Request, results[1].Request) {
		t.Fatalf("concurrent replay diverged: %+v", results)
	}
	first := results[0]
	if first.Request.Hash == "" || first.Request.Request.Origin != runspec.OriginSchedule || first.Request.Request.Actor.ID != "schedule-owner" || first.Request.Request.Git.App.Set || len(first.Request.AllowedPools) != 0 || !first.Request.AdmittedAt.Equal(due) {
		t.Fatalf("unsealed or untrusted schedule intake: %+v", first)
	}
	fires, err := tenant.ListCronFires(t.Context(), row.ID, 10)
	if err != nil || len(fires) != 1 || fires[0].RunID != first.RunID || !fires[0].DueAt.Equal(due) {
		t.Fatalf("history: %+v %v", fires, err)
	}
	rewriteSchemaCount(t, tenant.s.db, "runs", 1)
	rewriteSchemaCount(t, tenant.s.db, "triggers", 1)
}

func TestScheduleConsentAndReplayRequireCurrentAuthorityProperty(t *testing.T) {
	property := func(variant uint8, seed uint32) bool {
		tenant, a, body, now := consentedScheduleFixture(t)
		body.Schedules[0].Args = map[string]string{"input": fmt.Sprintf("value-%x", seed)}
		ids, _, err := tenant.ReplaceAuthorizedSchedules(t.Context(), a, body)
		if err != nil {
			t.Fatal(err)
		}
		row, err := tenant.GetCronSchedule(t.Context(), ids[0])
		if err != nil {
			t.Fatal(err)
		}
		*now = *row.NextDueAt
		admitted, err := tenant.AdmitScheduledRun(t.Context(), row, *now)
		if err != nil {
			t.Fatal(err)
		}
		switch variant % 4 {
		case 0:
			rewriteSchemaExec(t, tenant.s.db, `DELETE FROM memberships WHERE team=? AND account_id=?`, tenant.team, a.Principal.Capability.Subject.ID)
		case 1:
			if err := tenant.s.inTx(t.Context(), func(tx *storeTx) error { return revokeAuthorityTx(t.Context(), tx, a.Principal.Authority) }); err != nil {
				t.Fatal(err)
			}
		case 2:
			if _, err := tenant.PauseAuthorizedSchedule(t.Context(), a, row.ID, true); err != nil {
				t.Fatal(err)
			}
		case 3:
			body.Schedules = nil
			if _, _, err := tenant.ReplaceAuthorizedSchedules(t.Context(), a, body); err != nil {
				t.Fatal(err)
			}
		}
		_, replayErr := tenant.AdmitScheduledRun(t.Context(), row, *now)
		*now = now.Add(time.Minute)
		_, nextErr := tenant.AdmitScheduledRun(t.Context(), row, *now)
		rewriteSchemaCount(t, tenant.s.db, "runs", 1)
		stored, err := tenant.ReadRun(t.Context(), admitted.RunID)
		return replayErr != nil && nextErr != nil && err == nil && reflect.DeepEqual(stored.Request, admitted.Request)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 24}); err != nil {
		t.Fatal(err)
	}
}

func TestScheduleReplacementCapsAndOverrideRollback(t *testing.T) {
	tenant, a, body, _ := consentedScheduleFixture(t)
	ids, _, err := tenant.ReplaceAuthorizedSchedules(t.Context(), a, body)
	if err != nil {
		t.Fatal(err)
	}
	paused, err := tenant.PauseAuthorizedSchedule(t.Context(), a, ids[0], true)
	if err != nil {
		t.Fatal(err)
	}
	cron := "*/5 * * * *"
	if _, err := tenant.OverrideAuthorizedSchedule(t.Context(), a, paused, &controllerwire.ScheduleOverride{Cron: &cron}); err != nil {
		t.Fatal(err)
	}
	before, err := tenant.GetCronSchedule(t.Context(), ids[0])
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		body.Schedules = append(body.Schedules, controllerwire.ScheduleDeclaration{Pipeline: fmt.Sprintf("extra-%d", i), Cron: "* * * * *"})
	}
	if _, _, err := tenant.ReplaceAuthorizedSchedules(t.Context(), a, body); err == nil {
		t.Fatal("schedule cap accepted")
	}
	after, err := tenant.GetCronSchedule(t.Context(), ids[0])
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("failed replacement changed row: %v", err)
	}
	rows, err := tenant.ListCronSchedules(t.Context())
	if err != nil || len(rows) != 1 {
		t.Fatalf("partial replacement: %d %v", len(rows), err)
	}
	body.Schedules = body.Schedules[:1]
	if _, _, err := tenant.ReplaceAuthorizedSchedules(t.Context(), a, body); err != nil {
		t.Fatal(err)
	}
	after, err = tenant.GetCronSchedule(t.Context(), ids[0])
	if err != nil || !after.Paused || after.Override == nil || after.Override.Cron != cron {
		t.Fatalf("rearm lost edits: %+v %v", after, err)
	}
}

type scheduleResolutionRace struct {
	scheduleFixturePolicy
	before func()
}

func (p scheduleResolutionRace) ResolveScheduleSource(ctx context.Context, row CronSchedule, request runspec.Request) (runspec.Request, error) {
	p.before()
	return p.scheduleFixturePolicy.ResolveScheduleSource(ctx, row, request)
}

func TestScheduledSourceResolutionCannotOutliveConsentProperty(t *testing.T) {
	property := func(variant uint8) bool {
		tenant, a, body, now := consentedScheduleFixture(t)
		ids, _, err := tenant.ReplaceAuthorizedSchedules(t.Context(), a, body)
		if err != nil {
			t.Fatal(err)
		}
		row, err := tenant.GetCronSchedule(t.Context(), ids[0])
		if err != nil {
			t.Fatal(err)
		}
		*now = *row.NextDueAt
		base := tenant.s.intakePolicy.(scheduleFixturePolicy)
		tenant.s.SetIntakePolicy(scheduleResolutionRace{scheduleFixturePolicy: base, before: func() {
			switch variant % 3 {
			case 0:
				if _, err := tenant.PauseAuthorizedSchedule(t.Context(), a, row.ID, true); err != nil {
					t.Fatal(err)
				}
			case 1:
				if err := tenant.s.inTx(t.Context(), func(tx *storeTx) error { return revokeAuthorityTx(t.Context(), tx, a.Principal.Authority) }); err != nil {
					t.Fatal(err)
				}
			case 2:
				rewriteSchemaExec(t, tenant.s.db, `UPDATE cron_schedules SET declared=0 WHERE team=? AND id=?`, tenant.team, row.ID)
			}
		}})
		_, err = tenant.AdmitScheduledRun(t.Context(), row, *now)
		rewriteSchemaCount(t, tenant.s.db, "runs", 0)
		rewriteSchemaCount(t, tenant.s.db, "cron_fires", 0)
		return err != nil
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 18}); err != nil {
		t.Fatal(err)
	}
}

func TestScheduleOverridePreservesExplicitEmptyValues(t *testing.T) {
	tenant, a, body, _ := consentedScheduleFixture(t)
	body.Schedules[0].TZ = "UTC"
	body.Schedules[0].Args = map[string]string{"flag": "declared"}
	ids, _, err := tenant.ReplaceAuthorizedSchedules(t.Context(), a, body)
	if err != nil {
		t.Fatal(err)
	}
	empty, zero := "", "0s"
	args := map[string]string{}
	if _, err := tenant.OverrideAuthorizedSchedule(t.Context(), a, ids[0], &controllerwire.ScheduleOverride{TZ: &empty, CatchUp: &zero, Args: &args}); err != nil {
		t.Fatal(err)
	}
	row, err := tenant.GetCronSchedule(t.Context(), ids[0])
	if err != nil {
		t.Fatal(err)
	}
	effective := row.Effective()
	if row.Override == nil || !row.Override.TZSet || row.Override.CatchUp == nil || row.Override.Args == nil || effective.TZ != "" || effective.CatchUp != 0 || len(effective.Args) != 0 {
		t.Fatalf("explicit empties were lost: %+v", row.Override)
	}
}

func TestScheduleLocalSourceRequiresPersistedPeerAuthorityProperty(t *testing.T) {
	property := func(seed uint32) bool {
		tenant, a, _, _ := consentedScheduleFixture(t)
		root := fmt.Sprintf("/checkout-%08x", seed)
		a.Principal.Capability.Subject.Kind = capability.SubjectLocal
		row := CronSchedule{ID: "forged", RepoPath: root, Pipeline: "nightly", Name: "default", Cron: "* * * * *", Overlap: CronOverlapSkip, Where: CronWhereLocal}
		repository, err := runspec.CanonicalRepository(runspec.RepoInput{AbsolutePath: root})
		if err != nil {
			t.Fatal(err)
		}
		request := runspec.Request{Git: runspec.Git{Repository: repository}, Source: runspec.Source{Location: runspec.SourceLocation{LocalCheckout: root}}}
		_, _, err = tenant.replaceConsentedSchedules(t.Context(), a, root, []CronSchedule{row}, []runspec.Request{request}, true)
		rewriteSchemaCount(t, tenant.s.db, "cron_schedules", 0)
		return err != nil
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 12}); err != nil {
		t.Fatal(err)
	}
}

func TestScheduleRunNowPreservesCadenceWhilePaused(t *testing.T) {
	tenant, a, body, now := consentedScheduleFixture(t)
	ids, _, err := tenant.ReplaceAuthorizedSchedules(t.Context(), a, body)
	if err != nil {
		t.Fatal(err)
	}
	id := ids[0]
	if _, err := tenant.PauseAuthorizedSchedule(t.Context(), a, id, true); err != nil {
		t.Fatal(err)
	}
	before, err := tenant.GetCronSchedule(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	*now = before.NextDueAt.Add(time.Second)
	_, run, replayed, err := tenant.RunAuthorizedSchedule(t.Context(), a, id, "manual")
	if err != nil || run == "" || replayed {
		t.Fatalf("run paused schedule: %q %t %v", run, replayed, err)
	}
	_, replay, replayed, err := tenant.RunAuthorizedSchedule(t.Context(), a, id, "manual")
	if err != nil || replay != run || !replayed {
		t.Fatalf("manual replay: %q %t %v", replay, replayed, err)
	}
	after, err := tenant.GetCronSchedule(t.Context(), id)
	if err != nil || !after.Paused || !after.CursorAt.Equal(before.CursorAt) || !after.NextDueAt.Equal(*before.NextDueAt) || after.LastRunID != run {
		t.Fatalf("manual run changed cadence: %+v -> %+v: %v", before, after, err)
	}
	fires, err := tenant.ListCronFires(t.Context(), id, 10)
	if err != nil || len(fires) != 1 || fires[0].RunID != run || fires[0].Detail != "run now" {
		t.Fatalf("manual history: %+v %v", fires, err)
	}
	if _, err := tenant.PauseAuthorizedSchedule(t.Context(), a, id, false); err != nil {
		t.Fatal(err)
	}
	resumed, err := tenant.GetCronSchedule(t.Context(), id)
	if err != nil || resumed.Paused || !resumed.CursorAt.Equal(*now) || !resumed.NextDueAt.After(*now) {
		t.Fatalf("resume retained paused due instants: %+v %v", resumed, err)
	}
	if _, err := tenant.AdmitScheduledRun(t.Context(), resumed, *before.NextDueAt); err == nil {
		t.Fatal("resume replayed a paused due instant")
	}
}

type scheduleFollowPolicy struct {
	scheduleFixturePolicy
	revision *string
}

func (p scheduleFollowPolicy) ResolveScheduleSource(ctx context.Context, row CronSchedule, request runspec.Request) (runspec.Request, error) {
	request, err := p.scheduleFixturePolicy.ResolveScheduleSource(ctx, row, request)
	request.Git.SHA = *p.revision
	request.Source.Ref = request.Git.SHA
	return request, err
}

func TestScheduleReplayKeepsFirstSealAfterFollowedRevisionMoves(t *testing.T) {
	tenant, a, body, now := consentedScheduleFixture(t)
	revision := "0123456789abcdef0123456789abcdef01234567"
	tenant.s.SetIntakePolicy(scheduleFollowPolicy{scheduleFixturePolicy: tenant.s.intakePolicy.(scheduleFixturePolicy), revision: &revision})
	body.Follow = true
	ids, _, err := tenant.ReplaceAuthorizedSchedules(t.Context(), a, body)
	if err != nil {
		t.Fatal(err)
	}
	row, err := tenant.GetCronSchedule(t.Context(), ids[0])
	if err != nil {
		t.Fatal(err)
	}
	*now = *row.NextDueAt
	first, err := tenant.AdmitScheduledRun(t.Context(), row, *now)
	if err != nil {
		t.Fatal(err)
	}
	revision = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	replay, err := tenant.AdmitScheduledRun(t.Context(), row, *now)
	if err != nil || !replay.Replayed || !reflect.DeepEqual(first.Request, replay.Request) {
		t.Fatalf("followed revision changed replay: %+v %v", replay, err)
	}
	_, manual, replayed, err := tenant.RunAuthorizedSchedule(t.Context(), a, row.ID, "manual-follow")
	if err != nil || replayed {
		t.Fatalf("manual admission: %s %v", manual, err)
	}
	revision = "cccccccccccccccccccccccccccccccccccccccc"
	_, again, replayed, err := tenant.RunAuthorizedSchedule(t.Context(), a, row.ID, "manual-follow")
	if err != nil || !replayed || manual != again {
		t.Fatalf("manual followed replay: %s %t %v", again, replayed, err)
	}
	rewriteSchemaCount(t, tenant.s.db, "runs", 2)
}

func TestScheduleReplayStaysWithScheduleWhenArmerChanges(t *testing.T) {
	tenant, a, body, now := consentedScheduleFixture(t)
	ids, _, err := tenant.ReplaceAuthorizedSchedules(t.Context(), a, body)
	if err != nil {
		t.Fatal(err)
	}
	row, err := tenant.GetCronSchedule(t.Context(), ids[0])
	if err != nil {
		t.Fatal(err)
	}
	*now = *row.NextDueAt
	first, err := tenant.AdmitScheduledRun(t.Context(), row, *now)
	if err != nil {
		t.Fatal(err)
	}
	actor := authorityMember(t, tenant.s, "replacement-owner", RoleEditor)
	replacement := teamAuthorization(t, tenant.s, actor, tenant.team, "PUT", "/api/v1/schedules/repos")
	if _, _, err := tenant.ReplaceAuthorizedSchedules(t.Context(), replacement, body); err != nil {
		t.Fatal(err)
	}
	row, err = tenant.GetCronSchedule(t.Context(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := tenant.AdmitScheduledRun(t.Context(), row, *now)
	if err != nil || !replay.Replayed || !reflect.DeepEqual(first.Request, replay.Request) {
		t.Fatalf("armer changed schedule replay: %s %t %v", replay.RunID, replay.Replayed, err)
	}
	rewriteSchemaCount(t, tenant.s.db, "runs", 1)
}

func TestScheduleRepositoryCapCommitsOnlyTenRepositories(t *testing.T) {
	tenant, a, body, _ := consentedScheduleFixture(t)
	for i := range 10 {
		body.RepoURL = fmt.Sprintf("https://gitlab.com/acme/repo-%d.git", i)
		if _, _, err := tenant.ReplaceAuthorizedSchedules(t.Context(), a, body); err != nil {
			t.Fatal(err)
		}
	}
	body.RepoURL = "https://gitlab.com/acme/eleventh.git"
	if _, _, err := tenant.ReplaceAuthorizedSchedules(t.Context(), a, body); err == nil {
		t.Fatal("eleventh repository committed")
	}
	rows, err := tenant.ListCronSchedules(t.Context())
	if err != nil || len(rows) != 10 {
		t.Fatalf("cap changed declarations: %d %v", len(rows), err)
	}
	body.RepoURL = "https://gitlab.com/acme/repo-3.git"
	if _, _, err := tenant.ReplaceAuthorizedSchedules(t.Context(), a, body); err != nil {
		t.Fatalf("replacement at cap: %v", err)
	}
}
