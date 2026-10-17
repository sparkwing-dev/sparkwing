package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type githubSourceFake struct {
	repositories []githubapp.Repository
	ids          []int64
	permissions  map[string]string
	calls        int
	before       func()
	token        githubapp.Token
	err          error
}

func (f *githubSourceFake) InstallationRepositories(_ context.Context, installation int64) ([]githubapp.Repository, error) {
	if installation != 7 {
		return nil, errors.New("wrong installation")
	}
	return f.repositories, nil
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
	run.Request.Request.Git.Ref, run.Request.Request.Git.SHA = "refs/heads/main", "sealed-sha"
	run.Request.Request.Git.App = runspec.Optional[runspec.AppFacts]{Set: true, Value: runspec.AppFacts{RepositoryID: 1, InstallationID: 7, VerifiedAt: s.clock(), Ref: "refs/heads/main", SHA: "sealed-sha"}}
	body, err := json.Marshal(run.Request)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=? WHERE id='run'`, body)
	rewriteSchemaExec(t, s.db, `INSERT INTO github_app_installations(installation_id,team,account_id,account_login,account_type,created_at,updated_at) VALUES (7,'a',70,'acme','Organization',1,1)`)
	rewriteSchemaExec(t, s.db, `INSERT INTO github_app_extra_repos(team,repository_id,repository,extra_repo_id,extra_repo,created_at) VALUES ('a',1,'acme/repo',?,'acme/approved',1)`, extra)
	rewriteSchemaExec(t, s.db, `DELETE FROM git_credential_machines`)
	provider := &githubSourceFake{repositories: []githubapp.Repository{{ID: 1, FullName: "acme/renamed"}, {ID: extra, FullName: "acme/renamed-extra"}, {ID: extra + 1, FullName: "acme/approved"}}, token: githubapp.Token{Token: "private-source-token", ExpiresAt: s.clock().Add(time.Hour)}}
	return tenant, auth, provider
}

func TestGitHubSourceReleaseCannotEscapeClaimOrConsentProperty(t *testing.T) {
	property := func(mode uint8, value uint16) bool {
		extra := int64(value) + 10
		tenant, auth, provider := githubSourceFixture(t, extra)
		s := tenant.s
		mode %= 14
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
			provider.repositories = provider.repositories[:1]
		case 9:
			node, err := tenant.ReadNode(t.Context(), NodeRef{"run", "job"})
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
		}
		token, err := tenant.ReleaseGitHubSourceCredential(t.Context(), auth, provider)
		var count int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM git_credential_releases`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if mode != 0 {
			return err != nil && token.Token == "" && count == 0
		}
		return err == nil && token.Token == provider.token.Token && count == 1 && provider.calls == 1 && reflect.DeepEqual(provider.ids, []int64{1, extra}) && reflect.DeepEqual(provider.permissions, map[string]string{"contents": "read"})
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 96}); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubSourceReleaseAuditPrecedesReturn(t *testing.T) {
	tenant, auth, provider := githubSourceFixture(t, 2)
	token, err := tenant.ReleaseGitHubSourceCredential(t.Context(), auth, provider)
	if err != nil || token.Token == "" {
		t.Fatalf("release: %v", err)
	}
	var team, repo, run, node, prefix, credential string
	var generation, at int64
	err = tenant.s.db.QueryRow(`SELECT team,repository_key,run_id,node_id,capability_prefix,credential_id,generation,released_at FROM git_credential_releases`).Scan(&team, &repo, &run, &node, &prefix, &credential, &generation, &at)
	if err != nil || team != "a" || repo != "gh:1" || run != "run" || node != "job" || prefix != auth.Principal.Capability.Prefix || credential != "github_app:7" || generation != auth.Binding.Generation || at != tenant.s.clock().UnixNano() {
		t.Fatalf("audit: %s %s %s %s %s %s %d %d %v", team, repo, run, node, prefix, credential, generation, at, err)
	}
}
