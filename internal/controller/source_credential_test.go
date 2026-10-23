package controller_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp/githubapptest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type sourceAnswer struct {
	Kind       string `json:"kind"`
	Host       string `json:"host"`
	Secret     string `json:"secret"`
	Username   string `json:"username"`
	KnownHosts string `json:"known_hosts"`
	Token      string `json:"-"`
	Error      string `json:"-"`
	Message    string `json:"-"`
}

func (s *sourceAnswer) UnmarshalJSON(body []byte) error {
	type response sourceAnswer
	var out struct {
		response
		Failure *apierr.Error `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return err
	}
	*s = sourceAnswer(out.response)
	s.Token = s.Secret
	if out.Failure != nil {
		s.Error, s.Message = string(out.Failure.Code), out.Failure.Message
	}
	return nil
}

func (f *appFixture) sourceCredential(auth, runID string) (sourceAnswer, int) {
	f.t.Helper()
	var out sourceAnswer
	code := f.call("POST", "/api/v1/runs/"+runID+"/source-credential", auth, nil, &out)
	return out, code
}

func (f *appFixture) createRun(owner signedIn, runID, repoURL string, repoID int64) {
	f.t.Helper()
	key := githubFixtureKey(repoID)
	if repoID == 0 {
		key = map[string]string{"https://github.com/acme/widgets.git": "gh:701", "https://github.com/acme/plans.git": "gh:702"}[repoURL]
		if key == "" {
			key = "host:" + strings.TrimPrefix(strings.TrimSuffix(repoURL, ".git"), "https://")
		}
	}
	seedSourceRun(f.t, f.store, owner.team, runID, repoURL, key, "refs/heads/main", headSHA, repoID, runspec.DispatchHost)
}

func (f *appFixture) runWork(owner signedIn, runID, repoURL string) (auth, prefix string) {
	bearer, _, prefix := f.claimRunNode(owner, runID, repoURL, 0)
	return bearer, prefix
}

func (f *appFixture) nodeClaim(owner signedIn, runID, repoURL string, repoID int64) (auth, prefix string) {
	_, claim, prefix := f.claimRunNode(owner, runID, repoURL, repoID)
	return claim, prefix
}

func (f *appFixture) claimRunNode(owner signedIn, runID, repoURL string, repoID int64) (bearer, claim, prefix string) {
	f.t.Helper()
	return f.claimRunNodeOn(owner, runID, repoURL, repoID, false)
}

func (f *appFixture) claimRunNodeOn(owner signedIn, runID, repoURL string, repoID int64, personal bool) (bearer, claim, prefix string) {
	f.t.Helper()
	ctx := context.Background()
	f.createRun(owner, runID, repoURL, repoID)
	if personal {
		if _, err := teststore.SQL(f.t, f.store).ExecContext(ctx, `UPDATE runs SET created_principal=?,created_subject_kind='account' WHERE team=? AND id=?`, owner.id, owner.team, runID); err != nil {
			f.t.Fatal(err)
		}
	}
	if err := teststore.SeedNode(f.store, ctx, store.Node{RunID: runID, NodeID: "compile", Status: "pending"}); err != nil {
		f.t.Fatal(err)
	}
	if err := teststore.SeedNodeReady(f.store, ctx, runID, "compile"); err != nil {
		f.t.Fatal(err)
	}
	bearer, prefix = f.enrollRunner(owner, "r-"+runID, personal)
	var n controllerwire.ClaimResponse
	if code := f.call("POST", "/api/v1/runners/claim", bearer, githubClaimBody(capability.UnitNode, runID, "compile"), &n); code != http.StatusOK || n.Claim == nil || n.Claim.RunID != runID || n.Claim.Capability == "" {
		f.t.Fatalf("claim %s = %d %+v", runID, code, n)
	}
	if n.Claim.Request.Request.Git.SHA != headSHA || n.Claim.Request.Request.Source.Location.RemoteURL != repoURL {
		f.t.Fatalf("claim changed sealed checkout: %+v", n.Claim.Request.Request.Git)
	}
	return bearer, "Bearer " + n.Claim.Capability, prefix
}

func (f *appFixture) teamRunner(owner signedIn, name string) (auth, prefix string) {
	f.t.Helper()
	return f.enrollRunner(owner, name, false)
}

func (f *appFixture) enrollRunner(owner signedIn, name string, personal bool) (auth, prefix string) {
	f.t.Helper()
	var m struct {
		Capability string `json:"capability"`
		Prefix     string `json:"prefix"`
	}
	if code := f.call("POST", "/api/v1/runners", owner.auth,
		map[string]any{"name": name, "repos": []string{"github.com/*/*", "gitlab.example.com/*/*", "git.example.invalid/*/*"}, "personal": personal}, &m); code != http.StatusCreated {
		f.t.Fatalf("mint runner = %d", code)
	}
	return "Bearer " + m.Capability, m.Prefix
}

func (f *appFixture) setExtraRepos(who signedIn, repo string, extras []string) (int, map[string]any) {
	f.t.Helper()
	var out map[string]any
	code := f.call("PUT", "/api/v1/team/github-app/extra-repos", who.auth,
		map[string]any{"repository": repo, "extra_repos": extras}, &out)
	return code, out
}

func TestSourceCredential_AppScheduleUsesLiveAutomationConsent(t *testing.T) {
	f := newAppFixture(t)
	owner := f.ghUser(501, "olga")
	f.connect(owner, 501, 7, acmeAdmin)
	f.app.SetCommit("acme/widgets", "main", headSHA)
	f.app.SetFile("acme/widgets", headSHA, ".sparkwing/sparkwing.yaml", []byte(automationConfig))
	enableAutomation(t, f, owner)
	tenant := teststore.ForTeam(t, f.store, store.Team(owner.team))
	now := time.Now().UTC()
	sched, _, err := tenant.ArmCronSchedule(t.Context(), store.CronSchedule{
		ID: "app-nightly", RepoPath: "https://github.com/acme/widgets.git", GitHubInstallationID: 7, GitHubRepositoryID: 701,
		Pipeline: "nightly", Cron: "* * * * *", TZ: "UTC", Where: store.CronWhereController, GitBranch: "main", LockedRef: headSHA,
	})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := tenant.AdmitScheduledRun(t.Context(), sched, now)
	if err != nil {
		t.Fatal(err)
	}
	machine, _ := f.teamRunner(owner, "schedule-host")
	var award controllerwire.ClaimResponse
	if code := f.call("POST", "/api/v1/runners/claim", machine, githubClaimBody(capability.UnitPlan, admitted.RunID, ""), &award); code != http.StatusOK || award.Claim == nil || award.Claim.Request.Request.Git.App.Set || award.Claim.Request.Request.Git.DefaultBranch.Set {
		t.Fatalf("schedule award=%+v status=%d", award, code)
	}
	claim := "Bearer " + award.Claim.Capability
	if got, code := f.sourceCredential(claim, admitted.RunID); code != http.StatusOK || got.Secret == "" || !f.app.TokenCovers(got.Secret, "acme/widgets") || f.app.TokenCovers(got.Secret, "acme/plans") {
		t.Fatalf("scheduled source=%+v status=%d", got, code)
	}
	if code := f.call("DELETE", "/api/v1/team/github-app/automation?repository_id=701", owner.auth, nil, nil); code != http.StatusNoContent {
		t.Fatalf("revoke automation=%d", code)
	}
	if got, code := f.sourceCredential(claim, admitted.RunID); code != http.StatusNotFound || got.Secret != "" {
		t.Fatalf("revoked schedule source=%+v status=%d", got, code)
	}
}

// A run the App started gets a token that reads its repository, named by the
// GitHub ID the webhook recorded, and nothing else of the installation.
func TestSourceCredential_AppTokenReadsTheVerifiedRepository(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	node, _ := f.nodeClaim(olga, "run-widgets", "git@github.com:acme/widgets.git", 701)
	before := len(f.app.Minted())
	got, code := f.sourceCredential(node, "run-widgets")
	if code != http.StatusOK || got.Kind != "github_app" || got.Host != "github.com" ||
		got.Secret == "" {
		t.Fatalf("source credential = %d %+v, want the App token for acme/widgets", code, got)
	}
	minted := slices.DeleteFunc(f.app.Minted()[before:], func(token githubapptest.MintedToken) bool {
		if token.Permissions["metadata"] != "read" {
			return false
		}
		if len(token.Permissions) != 1 {
			t.Fatalf("metadata lookup requested content access: %+v", token)
		}
		return true
	})
	if len(minted) != 1 || !slices.Equal(minted[0].RepositoryIDs, []int64{701}) || len(minted[0].Repositories) != 0 ||
		len(minted[0].Permissions) != 1 || minted[0].Permissions["contents"] != "read" {
		t.Fatalf("minted = %+v, want one read-only token for 701 by id", minted)
	}
	if !f.app.TokenCovers(got.Token, "acme/widgets") || f.app.TokenCovers(got.Token, "acme/plans") {
		t.Fatal("the App token does not read exactly the run's repository")
	}
}

// A repository a run names by hand gets an App token only once the team
// subscribed it, so an editor who types a private repository of the
// installation into a run reaches nothing it could not already trigger.
func TestSourceCredential_TypedRepositoryNeedsASubscription(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	node, _ := f.nodeClaim(olga, "run-plans", "https://github.com/acme/plans.git", 0)
	before := len(f.app.Minted())
	got, code := f.sourceCredential(node, "run-plans")
	if code != http.StatusNotFound || got.Error != "not_found" || got.Token != "" ||
		!strings.Contains(got.Message, "subscribes it") {
		t.Fatalf("an unsubscribed private repository = %d %+v, want not_found naming the subscription", code, got)
	}
	if n := len(f.app.Minted()) - before; n != 0 {
		t.Fatalf("the refusal minted %d tokens", n)
	}
	if code := f.subscribe(olga, "acme/plans", "build", nil); code != http.StatusOK {
		t.Fatalf("subscribe = %d", code)
	}
	got, code = f.sourceCredential(node, "run-plans")
	if code != http.StatusOK || got.Kind != "github_app" || !f.app.TokenCovers(got.Token, "acme/plans") ||
		f.app.TokenCovers(got.Token, "acme/widgets") {
		t.Fatalf("a subscribed repository = %d %+v, want the App token for acme/plans alone", code, got)
	}
	minted := f.app.Minted()
	if last := minted[len(minted)-1]; !slices.Equal(last.RepositoryIDs, []int64{702}) || last.Permissions["contents"] != "read" {
		t.Fatalf("minted = %+v, want the subscription's repository id", last)
	}
}

// With nothing that covers the run, the route answers the code a runner reads
// as "no credential", and a message naming what the team can do about it.
func TestSourceCredential_NamesTheRemedy(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	cases := map[string]struct {
		repo string
		id   int64
		want string
	}{
		"uncovered github": {"https://github.com/someone/else.git", 999, "install the team's GitHub App on someone/else"},
		"typed github":     {"https://github.com/acme/widgets.git", 0, "subscribes it"},
		"another host":     {"https://gitlab.example.com/acme/tools.git", 0, "store a git credential for gitlab.example.com"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			runID := "run-" + strings.ReplaceAll(name, " ", "-")
			node, _ := f.nodeClaim(olga, runID, tc.repo, tc.id)
			got, code := f.sourceCredential(node, runID)
			if name == "uncovered github" {
				if code != http.StatusConflict || got.Error != "conflict" || got.Secret != "" || !strings.Contains(got.Message, "someone/else") {
					t.Fatalf("uncovered mint = %d %+v", code, got)
				}
				return
			}
			if code != http.StatusNotFound || got.Error != "not_found" || !strings.Contains(got.Message, tc.want) {
				t.Fatalf("source credential = %d %+v, want not_found naming %q", code, got, tc.want)
			}
			if !strings.Contains(got.Message, "Git credentials") {
				t.Fatalf("the remedy %q does not name the team's git credentials", got.Message)
			}
		})
	}
}

// Only the run's own claim token is answered: the machine's bearer, even
// while it holds the claim, gets the claim-required refusal, and another
// team's claim token is refused.
func TestSourceCredential_NeedsTheRunsOwnClaimToken(t *testing.T) {
	f := newAppFixture(t)
	olga, bob := f.ghUser(501, "olga"), f.ghUser(502, "bob")
	f.connect(olga, 501, 7, acmeAdmin)
	f.createRun(olga, "run-widgets", "https://github.com/acme/widgets.git", 701)
	if err := teststore.SeedNode(f.store, context.Background(), store.Node{RunID: "run-widgets", NodeID: "compile", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := teststore.SeedNodeReady(f.store, context.Background(), "run-widgets", "compile"); err != nil {
		t.Fatal(err)
	}
	machine, _ := f.teamRunner(olga, "holder")
	if code := f.call("POST", "/api/v1/runners/claim", machine,
		githubClaimBody(capability.UnitNode, "run-widgets", "compile"), nil); code != http.StatusOK {
		t.Fatalf("claim = %d", code)
	}
	idle, _ := f.teamRunner(olga, "idle")
	for name, auth := range map[string]string{"the claim's machine": machine, "an idle machine": idle, "the owner": olga.auth} {
		if got, code := f.sourceCredential(auth, "run-widgets"); code != http.StatusForbidden || got.Error != "forbidden" || got.Token != "" {
			t.Fatalf("%s = %d %+v, want 403 forbidden", name, code, got)
		}
	}
	bobs, _ := f.nodeClaim(bob, "run-bob", "https://github.com/bob/tools.git", 801)
	// safety: another team's run answers exactly as a missing run does.
	if got, code := f.sourceCredential(bobs, "run-widgets"); code != http.StatusNotFound || got.Token != "" || got.Error != "not_found" {
		t.Fatalf("another team's claim token = %d %+v, want the missing-run 404", code, got)
	}
}

func TestSourceCredential_AppTokenReadsOnlyTheOwnersExtraRepos(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	node, _ := f.nodeClaim(olga, "run-widgets", "https://github.com/acme/widgets.git", 701)

	var asked sourceAnswer
	code := f.call("POST", "/api/v1/runs/run-widgets/source-credential", node, map[string]any{"extra_repos": []string{"acme/plans"}}, &asked)
	if code != http.StatusBadRequest || asked.Secret != "" {
		t.Fatalf("runner-selected extras = %d %+v", code, asked)
	}
	asked, code = f.sourceCredential(node, "run-widgets")
	if code != http.StatusOK || f.app.TokenCovers(asked.Token, "acme/plans") || !f.app.TokenCovers(asked.Token, "acme/widgets") {
		t.Fatalf("source-only token = %d %+v", code, asked)
	}

	eddie := f.user("e", "eddie@example.com")
	f.join(olga, eddie, "eddie@example.com", "editor")
	eddie.team = olga.team
	if code, _ := f.setExtraRepos(eddie, "acme/widgets", []string{"acme/plans"}); code != http.StatusForbidden {
		t.Fatalf("an editor sets the list = %d, want 403", code)
	}
	if code, _ := f.setExtraRepos(olga, "acme/widgets", []string{"acme/secret"}); code != http.StatusBadRequest {
		t.Fatalf("a repository the installation does not cover = %d, want 400", code)
	}
	if code, _ := f.setExtraRepos(olga, "acme/widgets", []string{"bob/tools"}); code != http.StatusBadRequest {
		t.Fatalf("a repository of another owner = %d, want 400", code)
	}
	tooMany := make([]string, 11)
	for i := range tooMany {
		tooMany[i] = "acme/r" + string(rune('a'+i))
	}
	if code, _ := f.setExtraRepos(olga, "acme/widgets", tooMany); code != http.StatusBadRequest {
		t.Fatalf("eleven extras = %d, want 400", code)
	}
	if code, out := f.setExtraRepos(olga, "acme/widgets", []string{"acme/plans"}); code != http.StatusOK {
		t.Fatalf("the owner sets the list = %d %v", code, out)
	}
	var list struct {
		ExtraRepos []struct {
			Repository string   `json:"repository"`
			ExtraRepos []string `json:"extra_repos"`
		} `json:"extra_repos"`
	}
	if code := f.call("GET", "/api/v1/team/github-app/extra-repos", eddie.auth, nil, &list); code != http.StatusOK ||
		len(list.ExtraRepos) != 1 || list.ExtraRepos[0].Repository != "acme/widgets" || len(list.ExtraRepos[0].ExtraRepos) != 1 {
		t.Fatalf("a member reads the list = %d %+v", code, list)
	}

	got, code := f.sourceCredential(node, "run-widgets")
	if code != http.StatusOK || got.Kind != "github_app" || got.Secret == "" {
		t.Fatalf("source credential once listed = %d %+v", code, got)
	}
	if !f.app.TokenCovers(got.Token, "acme/widgets") || !f.app.TokenCovers(got.Token, "acme/plans") {
		t.Fatal("the token does not read both the run's repository and the listed extra")
	}
	minted := f.app.Minted()
	last := minted[len(minted)-1]
	if !slices.Equal(last.RepositoryIDs, []int64{701, 702}) || len(last.Repositories) != 0 || len(last.Permissions) != 1 || last.Permissions["contents"] != "read" {
		t.Fatalf("owner-approved source token widened: %+v", last)
	}

	f.app.SetRepos(7, githubapptest.Repo{ID: 701, FullName: "acme/widgets"})
	_, url := f.replica()
	f.url = url
	if got, code := f.sourceCredential(node, "run-widgets"); code != http.StatusConflict || !strings.Contains(got.Message, "acme/plans") {
		t.Fatalf("a listed repository the installation stopped covering = %d %+v, want 409 naming it", code, got)
	}
}

func TestSourceCredential_ExtraConsentStaysWithSourceIDProperty(t *testing.T) {
	property := func(value uint16) bool {
		f := newAppFixture(t)
		owner := f.ghUser(501, "olga")
		f.connect(owner, 501, 7, acmeAdmin)
		if code, out := f.setExtraRepos(owner, "acme/widgets", []string{"acme/plans"}); code != http.StatusOK {
			t.Fatalf("approval = %d %v", code, out)
		}
		replacement := int64(value) + 1000
		renamed := fmt.Sprintf("acme/widgets-%d", value)
		f.app.SetRepos(7, githubapptest.Repo{ID: 701, FullName: renamed}, githubapptest.Repo{ID: replacement, FullName: "acme/widgets"}, githubapptest.Repo{ID: 702, FullName: "acme/plans"})
		other, _ := f.nodeClaim(owner, "replacement", "https://github.com/acme/widgets.git", replacement)
		got, code := f.sourceCredential(other, "replacement")
		if code != http.StatusOK || !f.app.TokenCovers(got.Token, "acme/widgets") || f.app.TokenCovers(got.Token, "acme/plans") {
			return false
		}
		original, _ := f.nodeClaim(owner, "original", "https://github.com/"+renamed+".git", 701)
		got, code = f.sourceCredential(original, "original")
		return code == http.StatusOK && f.app.TokenCovers(got.Token, renamed) && f.app.TokenCovers(got.Token, "acme/plans") && !f.app.TokenCovers(got.Token, "acme/widgets")
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 12}); err != nil {
		t.Fatal(err)
	}
}

func TestSourceCredential_RenamedRepositoryClearsExtraConsentProperty(t *testing.T) {
	property := func(value uint16) bool {
		f := newAppFixture(t)
		owner := f.ghUser(501, "olga")
		f.connect(owner, 501, 7, acmeAdmin)
		if code, out := f.setExtraRepos(owner, "acme/widgets", []string{"acme/plans"}); code != http.StatusOK {
			t.Fatalf("approval = %d %v", code, out)
		}
		renamed := fmt.Sprintf("acme/widgets-%d", value)
		f.app.SetRepos(7, githubapptest.Repo{ID: 701, FullName: renamed}, githubapptest.Repo{ID: int64(value) + 1000, FullName: "acme/widgets"}, githubapptest.Repo{ID: 702, FullName: "acme/plans"})
		if code, out := f.setExtraRepos(owner, renamed, nil); code != http.StatusOK {
			t.Fatalf("clear = %d %v", code, out)
		}
		original, _ := f.nodeClaim(owner, "original", "https://github.com/"+renamed+".git", 701)
		got, code := f.sourceCredential(original, "original")
		return code == http.StatusOK && f.app.TokenCovers(got.Token, renamed) && !f.app.TokenCovers(got.Token, "acme/plans") && !f.app.TokenCovers(got.Token, "acme/widgets")
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 8}); err != nil {
		t.Fatal(err)
	}
}

func TestSourceCredential_AppSourceBeyondRepositoryListingLimit(t *testing.T) {
	f := newAppFixture(t)
	owner := f.ghUser(501, "olga")
	f.connect(owner, 501, 7, acmeAdmin)
	repos := make([]githubapptest.Repo, 1001)
	for i := range repos {
		repos[i] = githubapptest.Repo{ID: int64(1000 + i), FullName: fmt.Sprintf("acme/repo-%d", i)}
	}
	f.app.SetRepos(7, repos...)
	node, _ := f.nodeClaim(owner, "large-installation", "https://github.com/acme/repo-1000.git", 2000)
	got, code := f.sourceCredential(node, "large-installation")
	if code != http.StatusOK || !f.app.TokenCovers(got.Token, "acme/repo-1000") || f.app.TokenCovers(got.Token, "acme/repo-0") {
		t.Fatalf("source past listing limit = %d %+v", code, got)
	}
}
