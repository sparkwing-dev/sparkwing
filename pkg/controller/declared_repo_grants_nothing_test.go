package controller_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// Sequence A. Untrusted pipeline code holds an ordinary runner token and a
// live claim on its own run. It types a victim's repository onto that run and
// asks for the victim's credential. The read resolves against the pipeline the
// claim proves, so the typed repository buys nothing.
func TestDeclaredRepo_TypingAVictimRepositoryReadsNoSecretOfIts(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)
	ctx := context.Background()
	c := client.NewWithToken(f.url, nil, raw)

	seedSecret(t, f.store, "DEPLOY_KEY", "victim-key", "victim-deploy", false)
	seedSecret(t, f.store, "DEPLOY_KEY", "attacker-key", "attacker-build", false)
	seedRunNode(t, f.store, "run-attacker", "build")
	setRunPipeline(t, f.store, "run-attacker", "attacker-build")
	setRunDeclaredRepo(t, f.store, "run-attacker", "victim/private")
	if err := f.store.MarkNodeReady(ctx, "run-attacker", "build"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ClaimNode(ctx, "holder-1", nil, time.Minute, nil); err != nil {
		t.Fatalf("ClaimNode: %v", err)
	}

	sec, err := c.GetSecretForRun(ctx, "DEPLOY_KEY", "run-attacker")
	if err != nil {
		t.Fatalf("GetSecretForRun on the claimed run: %v", err)
	}
	if sec.Value != "attacker-key" {
		t.Fatalf("GetSecretForRun = %q, want attacker-key; the typed repository decided the read", sec.Value)
	}
	named, err := c.GetSecretForPipeline(ctx, "DEPLOY_KEY", "victim-deploy")
	if err != nil {
		t.Fatalf("GetSecretForPipeline: %v", err)
	}
	if named.Value == "victim-key" {
		t.Error("naming the victim pipeline in the query answered with its row; the hint is admin-only")
	}
}

// Sequence B. A retry of a victim's run produces a run carrying the victim's
// pipeline, which pipeline-scoped secrets alone would not stop. Retry is an
// operator action, so a runner token cannot reach it, and neither can a token
// that only carries the scope for starting its own work.
func TestDeclaredRepo_ARunnerCannotRetryAnotherPrincipalsRun(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)

	seedSecret(t, f.store, "DEPLOY_KEY", "victim-key", "victim-deploy", false)
	seedRunNode(t, f.store, "run-victim", "build")
	setRunPipeline(t, f.store, "run-victim", "victim-deploy")

	if got := f.do(t, raw, http.MethodPost, "/api/v1/runs/run-victim/retry", ""); got != http.StatusForbidden {
		t.Errorf("runner retry = %d, want 403", got)
	}
	if got := f.do(t, raw, http.MethodPost, "/api/v1/runs/run-victim/cancel", ""); got != http.StatusForbidden {
		t.Errorf("runner cancel = %d, want 403", got)
	}

	submitter, _, err := f.store.CreateToken("submitter", store.TokenKindService,
		[]string{controller.ScopeRunsWrite}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken submitter: %v", err)
	}
	if got := f.do(t, submitter, http.MethodPost, "/api/v1/runs/run-victim/retry", ""); got != http.StatusForbidden {
		t.Errorf("runs.write retry = %d, want 403; retry is an operator action", got)
	}

	operator, _, err := f.store.CreateToken("operator", store.TokenKindUser,
		[]string{controller.ScopeRunsControl}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken operator: %v", err)
	}
	if got := f.do(t, operator, http.MethodPost, "/api/v1/runs/run-victim/retry", ""); got != http.StatusAccepted {
		t.Errorf("operator retry = %d, want 202", got)
	}
}

// A runner token reports state on work it already holds and cannot start work
// of its own choosing, so it has no way to obtain a claim on a pipeline it was
// never given.
func TestDeclaredRepo_ARunnerCannotManufactureARunOfAnotherPipeline(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)

	body := `{"pipeline":"victim-deploy","repo":"victim/private"}`
	if got := f.do(t, raw, http.MethodPost, "/api/v1/triggers", body); got != http.StatusForbidden {
		t.Errorf("runner trigger submit = %d, want 403", got)
	}
}

// Sequence C. The Git cache holds clones the controller made with a credential
// of its own, and every team shares them. A trigger's GITHUB_REPOSITORY is
// whatever its submitter wrote, so outside the operator's team it selects
// nothing the cache serves.
func TestDeclaredRepo_ForgedTriggerEnvReadsNoCachedSource(t *testing.T) {
	f, _ := newScopedFixture(t, runnerScopes)
	ctx := context.Background()
	if err := f.store.AsOperator().CreateTeam(ctx, "attacker"); err != nil {
		t.Fatal(err)
	}
	tenant, err := f.store.ForTeam(ctx, "attacker")
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := tenant.CreateToken(ctx, "attacker-pool", store.TokenKindRunner, runnerScopes, 0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := tenant.CreateTriggerWithRun(ctx, store.Trigger{
		ID: "run-attacker", Pipeline: "attacker-build", Status: "running",
		TriggerEnv: map[string]string{"GITHUB_REPOSITORY": "victim/private"},
		CreatedAt:  time.Now().UTC(),
	}, store.Run{
		ID: "run-attacker", Pipeline: "attacker-build", Status: "running", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateNode(ctx, store.Node{
		RunID: "run-attacker", NodeID: "build", Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.MarkNodeReady(ctx, "run-attacker", "build"); err != nil {
		t.Fatal(err)
	}
	if claimed, err := client.NewWithToken(f.url, nil, raw).
		ClaimNode(ctx, "holder-1", nil, time.Minute, nil); err != nil || claimed == nil {
		t.Fatalf("ClaimNode = %+v, %v", claimed, err)
	}

	name := sourceurl.ClaimedRepoNameFromURL("git@github.com:victim/private.git")
	path := "/api/v1/runs/run-attacker/gitcache/git/" + name + "/info/refs?service=git-upload-pack"
	if got := f.do(t, raw, http.MethodGet, path, ""); got != http.StatusForbidden {
		t.Errorf("forged GITHUB_REPOSITORY cache read = %d, want 403", got)
	}
}

// The path an operator actually uses keeps working: a run a signed webhook
// delivery created resolves the secrets of the pipeline it runs.
func TestDeclaredRepo_AWebhookRunStillResolvesItsOwnSecrets(t *testing.T) {
	f, raw := newScopedFixture(t, runnerScopes)
	ctx := context.Background()
	c := client.NewWithToken(f.url, nil, raw)

	seedSecret(t, f.store, "DEPLOY_KEY", "web-key", "deploy-web", false)
	if err := f.store.PutGitHubWebhookBinding(ctx, store.GitHubWebhookBinding{
		Pipeline: "deploy-web", Repo: "acme/web", Secret: "hook-secret",
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateTrigger(ctx, store.Trigger{
		ID: "run-web", Pipeline: "deploy-web", Status: "running", Repo: "acme/web",
		RepoURL: "git@github.com:acme/web.git", WebhookDelivery: "delivery-1",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateRun(ctx, store.Run{
		ID: "run-web", Pipeline: "deploy-web", Status: "running",
		DeclaredRepo: "acme/web", RepoURL: "git@github.com:acme/web.git",
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateNode(ctx, store.Node{RunID: "run-web", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.MarkNodeReady(ctx, "run-web", "build"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ClaimNode(ctx, "holder-1", nil, time.Minute, nil); err != nil {
		t.Fatalf("ClaimNode: %v", err)
	}

	sec, err := c.GetSecretForRun(ctx, "DEPLOY_KEY", "run-web")
	if err != nil {
		t.Fatalf("GetSecretForRun on a webhook-originated run: %v", err)
	}
	if sec.Value != "web-key" {
		t.Errorf("GetSecretForRun = %q, want web-key", sec.Value)
	}
}

func setRunDeclaredRepo(t *testing.T, st *store.Store, runID, repo string) {
	t.Helper()
	if _, err := st.DB().ExecContext(context.Background(),
		`UPDATE runs SET declared_repo = ? WHERE id = ?`, repo, runID); err != nil {
		t.Fatal(err)
	}
}
