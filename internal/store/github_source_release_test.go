package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

type githubSourceFake struct {
	repositories []githubapp.Repository
	ids          []int64
	permissions  map[string]string
	calls        int
	lists        int
	listErr      error
	before       func()
	token        githubapp.Token
	err          error
}

func (f *githubSourceFake) InstallationRepositories(_ context.Context, installation int64) ([]githubapp.Repository, error) {
	if installation != 7 {
		return nil, errors.New("wrong installation")
	}
	f.lists++
	return f.repositories, f.listErr
}

func (f *githubSourceFake) InstallationTokenByIDs(_ context.Context, installation int64, ids []int64, permissions map[string]string) (githubapp.Token, error) {
	if installation != 7 {
		return githubapp.Token{}, errors.New("wrong installation")
	}
	f.calls++
	f.ids, f.permissions = append([]int64{}, ids...), permissions
	if f.before != nil {
		f.before()
	}
	return f.token, f.err
}

func githubSourceFixture(t *testing.T, extra int64) (*Tenant, capability.Authorized, *githubSourceFake) {
	t.Helper()
	tenant, auth, _ := sourceReleaseFixture(t)
	s := tenant.s
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	app := runspec.AppFacts{Event: "push", RepositoryID: 1, InstallationID: 7, VerifiedAt: s.clock(), Ref: "refs/heads/main", SHA: fixtureFeatureSHA, DeliveryKey: "push-delivery", DefaultBranch: "main", DefaultBranchPush: true}
	run.Request.Request.Git.Ref, run.Request.Request.Git.SHA = app.Ref, app.SHA
	run.Request.Request.Git.App = runspec.Optional[runspec.AppFacts]{Set: true, Value: app}
	// A verified push to the default branch is the one provenance the owner's extra repositories reach.
	run.Request.Provenance, err = runspec.AppProvenance(app, runspec.Optional[runspec.PullRequest]{}, run.Pipeline)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(run.Request)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=? WHERE id='run'`, body)
	rewriteSchemaExec(t, s.db, `INSERT INTO github_app_installations(installation_id,team,account_id,account_login,account_type,created_at,updated_at) VALUES (7,'a',70,'acme','Organization',1,1)`)
	rewriteSchemaExec(t, s.db, `INSERT INTO github_app_extra_repos(team,repository_id,repository,extra_repo_id,extra_repo,created_at) VALUES ('a',1,'acme/repo',?,'acme/approved',1)`, extra)
	provider := &githubSourceFake{repositories: []githubapp.Repository{{ID: 1, FullName: "acme/renamed"}, {ID: extra, FullName: "acme/renamed-extra"}, {ID: extra + 1, FullName: "acme/approved"}}, token: githubapp.Token{Token: "private-source-token", ExpiresAt: s.clock().Add(time.Hour)}}
	return tenant, auth, provider
}

func TestGitHubSourceReleaseCannotEscapeClaimOrConsentProperty(t *testing.T) {
	property := func(mode uint8, value uint16) bool {
		extra := int64(value) + 10
		tenant, auth, provider := githubSourceFixture(t, extra)
		s := tenant.s
		approved := fmt.Sprintf("acme/approved-%d", value)
		rewriteSchemaExec(t, s.db, `UPDATE github_app_extra_repos SET extra_repo=?`, approved)
		mode %= 16
		switch mode {
		case 1:
			auth.Target.Team = "b"
		case 2:
			auth.Target.RunID = "foreign"
		case 3:
			rewriteSchemaExec(t, s.db, `UPDATE capabilities SET revoked_at=1 WHERE prefix='runner'`)
		case 4:
			rewriteSchemaExec(t, s.db, `UPDATE github_app_installations SET suspended=1`)
		case 5:
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET generation=generation+1`)
		case 6:
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET started_at=1`)
		case 7:
			rewriteSchemaExec(t, s.db, `UPDATE runs SET cancel_requested_at=1`)
		case 8:
			provider.err = githubapp.ErrNotInstalled
		case 9:
			node, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: "run", NodeID: "job"})
			if err != nil {
				t.Fatal(err)
			}
			provider.before = func() { s.clock = func() time.Time { return *node.LeaseExpiresAt } }
		case 10:
			rewriteSchemaExec(t, s.db, `CREATE TRIGGER fail_source_audit BEFORE INSERT ON git_credential_releases BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
		case 11:
			provider.err = errors.New("provider refused")
		case 12:
			rewriteSchemaExec(t, s.db, `UPDATE capabilities SET grants=? WHERE prefix=?`, []byte(`[]`), auth.Principal.Capability.Prefix)
		case 13:
			rewriteSchemaExec(t, s.db, `UPDATE capabilities SET expires_at=1 WHERE prefix='runner'`)
		case 14:
			provider.before = func() {
				rewriteSchemaExec(t, s.db, `UPDATE github_app_extra_repos SET extra_repo=?`, approved+"-changed")
			}
		case 15:
			setMachineOrigin(t, s, "runner", &CapabilityOrigin{Personal: true, Kind: capability.ProvisionMachine, RegistrationID: "runner:a:runner"})
		}
		token, err := tenant.ReleaseGitHubSourceCredential(t.Context(), auth, provider)
		var count int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM git_credential_releases`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if mode != 0 {
			return err != nil && token.Secret == "" && len(token.ExtraRepositories) == 0 && count == 0
		}
		return err == nil && token.Kind == "github_app" && token.Host == "github.com" && reflect.DeepEqual(token.ExtraRepositories, []string{approved}) && token.Secret == provider.token.Token && count == 1 && provider.calls == 1 && reflect.DeepEqual(provider.ids, []int64{1, extra}) && reflect.DeepEqual(provider.permissions, map[string]string{"contents": "read"})
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 96}); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubSourceReleaseAuditPrecedesReturn(t *testing.T) {
	tenant, auth, provider := githubSourceFixture(t, 2)
	token, err := tenant.ReleaseGitHubSourceCredential(t.Context(), auth, provider)
	if err != nil || token.Secret == "" || !reflect.DeepEqual(token.ExtraRepositories, []string{"acme/approved"}) {
		t.Fatalf("release: %v", err)
	}
	var team, repo, run, node, prefix, credential string
	var generation, at int64
	err = tenant.s.db.QueryRow(`SELECT team,repository_key,run_id,node_id,capability_prefix,credential_id,generation,released_at FROM git_credential_releases`).Scan(&team, &repo, &run, &node, &prefix, &credential, &generation, &at)
	if err != nil || team != "a" || repo != "gh:1" || run != "run" || node != "job" || prefix != auth.Principal.Capability.Prefix || credential != "github_app:7" || generation != auth.Binding.Generation || at != tenant.s.clock().UnixNano() {
		t.Fatalf("audit: %s %s %s %s %s %s %d %d %v", team, repo, run, node, prefix, credential, generation, at, err)
	}
}

func TestGitHubScheduledSourceCannotEscapeConsentProperty(t *testing.T) {
	property := func(value uint16) bool {
		for mode := range 8 {
			extra := int64(value) + 10
			tenant, auth, provider := githubSourceFixture(t, extra)
			run, err := tenant.ReadRun(t.Context(), "run")
			if err != nil {
				t.Fatal(err)
			}
			sched, _, err := tenant.ArmCronSchedule(t.Context(), CronSchedule{
				ID: "app-schedule", RepoPath: "https://github.com/acme/repo.git", GitHubInstallationID: 7, GitHubRepositoryID: 1,
				Pipeline: run.Pipeline, Cron: "* * * * *", TZ: "UTC", Where: CronWhereController,
			})
			if err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, tenant.s.db, `INSERT INTO github_app_automation(team,repo_key,repository_id,repository,installation_id,enabled_by,enabled_at) VALUES ('a','gh:1',1,'acme/repo',7,'owner',1)`)
			run.Request.AuthorityRef = capability.AuthorityRef{Kind: "schedule", ID: sched.ID}
			run.Request.Request.Pipeline = run.Pipeline
			run.Request.Request.Origin = runspec.OriginSchedule
			run.Request.Request.Git.App = runspec.Optional[runspec.AppFacts]{}
			run.Request.Provenance, err = runspec.ScheduleProvenance(sched.ID, 1, "main", fixtureFeatureSHA, run.Pipeline)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case 1:
				run.Request.AuthorityRef.ID += "-foreign"
			case 2:
				run.Request.Request.Origin = runspec.OriginTrigger
			case 3:
				rewriteSchemaExec(t, tenant.s.db, `UPDATE github_app_automation SET team='b'`)
			case 4:
				rewriteSchemaExec(t, tenant.s.db, `DELETE FROM github_app_automation`)
			case 5:
				rewriteSchemaExec(t, tenant.s.db, `UPDATE cron_schedules SET github_repository_id=999,repo_key='gh:999'`)
			case 6:
				rewriteSchemaExec(t, tenant.s.db, `UPDATE cron_schedules SET team='b'`)
			case 7:
				rewriteSchemaExec(t, tenant.s.db, `UPDATE github_app_installations SET suspended=1`)
			}
			body, err := json.Marshal(run.Request)
			if err != nil {
				t.Fatal(err)
			}
			rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET request_json=? WHERE id='run'`, body)
			token, err := tenant.ReleaseGitHubSourceCredential(t.Context(), auth, provider)
			var audits int
			if err := tenant.s.db.QueryRow(`SELECT COUNT(*) FROM git_credential_releases`).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			if mode == 0 {
				// The owner's extra repositories reach only a verified push to the default branch.
				if err != nil || token.Secret == "" || audits != 1 || !reflect.DeepEqual(provider.ids, []int64{1}) || !reflect.DeepEqual(provider.permissions, map[string]string{"contents": "read"}) {
					t.Logf("scheduled release=%+v ids=%v err=%v", token, provider.ids, err)
					return false
				}
			} else if err == nil || token.Secret != "" || provider.calls != 0 || audits != 0 {
				t.Logf("mode=%d refused release=%+v err=%v calls=%d", mode, token, err, provider.calls)
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 6}); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubSourceReleaseRequiresMatchingSameTeamMachineProperty(t *testing.T) {
	property := func(value uint16) bool {
		for mode := range 3 {
			tenant, auth, provider := githubSourceFixture(t, int64(value)+10)
			switch mode {
			case 0:
				tenant.s.claimProfiles = claimProfileFake{profile: match.Profile{Location: "remote", Accept: deniedSource(t)}}
			case 1:
				rewriteSchemaExec(t, tenant.s.db, `UPDATE capabilities SET subject_kind='service' WHERE prefix='runner'`)
			case 2:
				rewriteSchemaExec(t, tenant.s.db, `UPDATE capabilities SET team='b' WHERE prefix='runner'`)
			}
			token, err := tenant.ReleaseGitHubSourceCredential(t.Context(), auth, provider)
			if err == nil || token.Secret != "" || provider.calls != 0 {
				t.Logf("mode=%d token=%+v calls=%d err=%v", mode, token, provider.calls, err)
				return false
			}
			rewriteSchemaCount(t, tenant.s.db, "git_credential_releases", 0)
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 8}); err != nil {
		t.Fatal(err)
	}
}

func deniedSource(t *testing.T) match.RepoFilter {
	t.Helper()
	filter, err := match.ParseAccept([]string{"github.com/foreign/*"})
	if err != nil {
		t.Fatal(err)
	}
	return filter
}

func TestGitHubSourceReleaseMintsOutsideTransactionsAndRechecksProperty(t *testing.T) {
	property := func(value uint16) bool {
		for mode := range 6 {
			tenant, auth, provider := githubSourceFixture(t, int64(value)+10)
			if mode == 5 {
				run, err := tenant.ReadRun(t.Context(), "run")
				if err != nil {
					t.Fatal(err)
				}
				run.Request.Request.Git.App = runspec.Optional[runspec.AppFacts]{}
				body, err := json.Marshal(run.Request)
				if err != nil {
					t.Fatal(err)
				}
				rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET request_json=? WHERE id='run'`, body)
				rewriteSchemaExec(t, tenant.s.db, `INSERT INTO github_app_triggers(team,repository_id,pipeline,repository,installation_id,created_at,repo_key) VALUES ('a',1,'p','acme/repo',7,1,'gh:1')`)
			}
			provider.before = func() {
				if n := tenant.s.db.Stats().InUse; n != 0 {
					t.Errorf("GitHub called with %d database connections held", n)
					return
				}
				switch mode {
				case 1:
					rewriteSchemaExec(t, tenant.s.db, `UPDATE github_app_installations SET suspended=1`)
				case 2:
					rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET generation=generation+1`)
				case 3:
					rewriteSchemaExec(t, tenant.s.db, `DELETE FROM github_app_extra_repos`)
				case 4:
					rewriteSchemaExec(t, tenant.s.db, `UPDATE capabilities SET revoked_at=1 WHERE prefix='runner'`)
				case 5:
					rewriteSchemaExec(t, tenant.s.db, `DELETE FROM github_app_triggers`)
				}
			}
			provider.listErr = githubapp.ErrTooManyRepositories
			token, err := tenant.ReleaseGitHubSourceCredential(t.Context(), auth, provider)
			if provider.lists != 0 {
				t.Errorf("enumerated repository coverage %d times", provider.lists)
				return false
			}
			if mode == 0 {
				if err != nil || token.Secret == "" {
					t.Logf("scoped mint beyond listing: %v", err)
					return false
				}
				rewriteSchemaCount(t, tenant.s.db, "git_credential_releases", 1)
			} else {
				if err == nil || token.Secret != "" {
					t.Logf("mode=%d token=%+v err=%v", mode, token, err)
					return false
				}
				rewriteSchemaCount(t, tenant.s.db, "git_credential_releases", 0)
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 6}); err != nil {
		t.Fatal(err)
	}
}
