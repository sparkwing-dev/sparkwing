package store

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestCronOperationsRemainInTheirTenantProperty(t *testing.T) {
	s := authorityStore(t)
	a, b := &Tenant{s: s, team: "a"}, &Tenant{s: s, team: "b"}
	random := rand.New(rand.NewPCG(323, 907))
	now := time.Unix(1700000000, 37).UTC()
	for i := range 32 {
		path := fmt.Sprintf("/repos/shared-%d-%d", i, random.Uint64())
		var expected []CronSchedule
		for _, tenant := range []*Tenant{a, b} {
			row, created, err := tenant.ArmCronSchedule(t.Context(), CronSchedule{
				ID: fmt.Sprintf("%s-%d", tenant.team, i), RepoPath: path,
				Pipeline: "nightly", Cron: "* * * * *", TZ: "UTC",
			}, now)
			if err != nil || !created {
				t.Fatalf("arm: %+v, %v, %v", row, created, err)
			}
			if err := tenant.ResolveCronDue(t.Context(), row.ID, now, nil, &CronFire{Outcome: CronOutcomeMissed, DueAt: now, DecidedAt: now}, now); err != nil {
				t.Fatal(err)
			}
			row, err = tenant.GetCronSchedule(t.Context(), row.ID)
			if err != nil {
				t.Fatal(err)
			}
			expected = append(expected, row)
		}
		for j, tenant := range []*Tenant{a, b} {
			foreign := expected[1-j]
			mutations := []func() error{
				func() error { return tenant.SetCronSchedulePaused(t.Context(), foreign.ID, true, now) },
				func() error { return tenant.SetCronScheduleDeclared(t.Context(), foreign.ID, false, now) },
				func() error { return tenant.SetCronScheduleNextDue(t.Context(), foreign.ID, &now, now) },
				func() error { return tenant.SetCronScheduleLock(t.Context(), foreign.ID, CronLock{Ref: "forged"}, now) },
				func() error { return tenant.SetCronOverride(t.Context(), foreign.ID, CronOverride{}, now) },
				func() error { return tenant.ClearCronOverride(t.Context(), foreign.ID, now) },
				func() error { return tenant.DeleteCronSchedule(t.Context(), foreign.ID) },
				func() error {
					return tenant.ResolveCronDue(t.Context(), foreign.ID, now, nil, &CronFire{Outcome: CronOutcomeMissed, DueAt: now, DecidedAt: now}, now)
				},
			}
			random.Shuffle(len(mutations), func(x, y int) { mutations[x], mutations[y] = mutations[y], mutations[x] })
			for _, mutate := range mutations {
				if err := mutate(); err == nil {
					t.Fatal("foreign schedule mutation succeeded")
				}
			}
			if _, err := tenant.GetCronSchedule(t.Context(), foreign.ID); err == nil {
				t.Fatal("foreign schedule was readable")
			}
			fires, err := tenant.ListCronFires(t.Context(), foreign.ID, 10)
			if err != nil || len(fires) != 0 {
				t.Fatalf("foreign history: %+v, %v", fires, err)
			}
			forged := foreign
			forged.RepoPath += "/forged"
			if own, created, err := tenant.ArmCronSchedule(t.Context(), forged, now); err != nil || !created || own.Team != tenant.team {
				t.Fatalf("arming a foreign id in this team: %+v, %v, %v", own, created, err)
			}
			if err := tenant.DeleteCronSchedule(t.Context(), foreign.ID); err != nil {
				t.Fatal(err)
			}
		}
		for j, tenant := range []*Tenant{a, b} {
			got, err := tenant.GetCronSchedule(t.Context(), expected[j].ID)
			if err != nil || !reflect.DeepEqual(got, expected[j]) {
				t.Fatalf("foreign mutation changed row: %+v -> %+v, %v", expected[j], got, err)
			}
			fires, err := tenant.ListCronFires(t.Context(), expected[j].ID, 10)
			if err != nil || len(fires) != 1 || fires[0].Outcome != CronOutcomeMissed {
				t.Fatalf("own history: %+v, %v", fires, err)
			}
			if n, err := tenant.DeleteCronSchedulesForRepo(t.Context(), path); err != nil || n != 1 {
				t.Fatalf("tenant deletion: %d, %v", n, err)
			}
		}
	}
}

func TestCronSchedulePersistsCanonicalRepository(t *testing.T) {
	tenant := &Tenant{s: authorityStore(t), team: "a"}
	now := time.Unix(1700000000, 37).UTC()
	for i, input := range []runspec.RepoInput{
		{RemoteURL: "/repos/work/../project"},
		{RemoteURL: "ssh://git@example.com:2222/Owner/Repo.git"},
		{GitHubRepositoryID: runspec.Optional[int64]{Set: true, Value: 487}, RemoteURL: "https://github.com/owner/project"},
	} {
		repository, err := runspec.CanonicalRepository(input)
		if err != nil {
			t.Fatal(err)
		}
		schedule := CronSchedule{ID: fmt.Sprint(i), RepoPath: input.RemoteURL, Pipeline: "nightly", Cron: "* * * * *", TZ: "UTC"}
		if input.GitHubRepositoryID.Set {
			schedule.GitHubInstallationID, schedule.GitHubRepositoryID = 42, input.GitHubRepositoryID.Value
		}
		if _, _, err := tenant.ArmCronSchedule(t.Context(), schedule, now); err != nil {
			t.Fatal(err)
		}
		var key string
		if err := tenant.s.queryRow(t.Context(), `SELECT repo_key FROM cron_schedules WHERE team=? AND id=?`, tenant.team, schedule.ID).Scan(&key); err != nil || key != string(repository.Key) {
			t.Fatalf("repository key: %q, %v; want %q", key, err, repository.Key)
		}
	}
}

func TestArmCronScheduleDerivesIdentityRatherThanCallerKeyProperty(t *testing.T) {
	property := func(value uint16) bool {
		s := authorityStore(t)
		tenant := &Tenant{s: s, team: "a"}
		row, _, err := tenant.ArmCronSchedule(t.Context(), CronSchedule{ID: "derived", RepoPath: "https://git.example.com/acme/repo.git", RepositoryKey: runspec.RepoKey(fmt.Sprintf("gh:%d", int(value)+1)), Pipeline: "ci", Cron: "* * * * *", TZ: "UTC"}, time.Now())
		return err == nil && row.RepositoryKey == "git:git.example.com/acme/repo"
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 12}); err != nil {
		t.Fatal(err)
	}
}

type scheduleIdentityPolicy struct {
	fakeIntakePolicy
	resolve func(context.Context, Team, string) (runspec.Repository, error)
}

func (p scheduleIdentityPolicy) ResolveScheduleRepository(ctx context.Context, team Team, remote string) (runspec.Repository, error) {
	return p.resolve(ctx, team, remote)
}

func TestArmCronScheduleUsesTrustedPolicyIdentityWithoutAppConsent(t *testing.T) {
	for _, refused := range []bool{false, true} {
		s := authorityStore(t)
		tenant := &Tenant{s: s, team: "a"}
		s.intakePolicy = scheduleIdentityPolicy{resolve: func(_ context.Context, team Team, remote string) (runspec.Repository, error) {
			if team != "a" || remote != "https://github.com/acme/repo.git" {
				t.Fatalf("lookup = %s %s", team, remote)
			}
			if refused {
				return runspec.Repository{}, errors.New("lookup failed")
			}
			return runspec.Repository{Key: "gh:701"}, nil
		}}
		row, _, err := tenant.ArmCronSchedule(t.Context(), CronSchedule{ID: "manual", RepoPath: "https://github.com/acme/repo.git", RepositoryKey: "gh:999", Pipeline: "ci", Cron: "* * * * *", TZ: "UTC"}, time.Now())
		if refused {
			if err == nil {
				t.Fatal("failed identity lookup armed a schedule")
			}
			rewriteSchemaCount(t, s.db, "cron_schedules", 0)
		} else if err != nil || row.RepositoryKey != "gh:701" || row.GitHubInstallationID != 0 || row.GitHubRepositoryID != 0 {
			t.Fatalf("manual schedule = %+v %v", row, err)
		}
	}
}
