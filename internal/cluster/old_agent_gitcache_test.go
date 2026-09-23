package cluster

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// oldAgentRegister sends the register request of a runner built before cache
// grants: a POST to the controller's claim-bound proxy carrying only its
// runner token.
func oldAgentRegister(t *testing.T, ctrlURL, runID, token, repoURL string) (int, string) {
	t.Helper()
	q := neturl.Values{}
	q.Set("name", sourceurl.ClaimedRepoNameFromURL(repoURL))
	q.Set("repo", repoURL)
	req, err := http.NewRequest(http.MethodPost,
		ctrlURL+"/api/v1/runs/"+neturl.PathEscape(runID)+"/gitcache/git/register?"+q.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, strings.TrimSpace(string(body))
}

// An agent built before cache grants fetches a CLI-submitted operator run's
// source through the controller's proxy with its runner token alone.
func TestOldAgentFetchesAnOperatorRunThroughTheControllerProxy(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: clones over a stand-in SSH transport")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv(authwire.CacheTokenEnv, "")
	t.Setenv(authwire.CacheGrantEnv, "")
	t.Setenv(authwire.CacheGrantKeyEnv, cacheGrantKey)

	origins := t.TempDir()
	sha := makePrivateOrigin(t, filepath.Join(origins, "acme", "private.git"), filepath.Join(t.TempDir(), "unused"))
	const repoURL = "ssh://git@git.example.invalid/acme/private.git"
	standInSSH(t, origins)

	cacheSrv := newGrantCache(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	now := time.Now().UTC()
	newRun := func(tenant *store.Tenant, runID string) {
		t.Helper()
		// A CLI submission: no webhook delivery, the source named by the
		// submitter's checkout.
		if err := tenant.CreateTriggerWithRun(ctx,
			store.Trigger{
				ID: runID, Pipeline: "hello", Status: "running", CreatedAt: now,
				RepoURL: repoURL, GitBranch: "main", GitSHA: sha,
			},
			store.Run{ID: runID, Pipeline: "hello", Status: "running", StartedAt: now},
		); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: "hello", Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		if err := st.MarkNodeReady(ctx, runID, "hello"); err != nil {
			t.Fatal(err)
		}
	}
	// The scopes prod's agent:moonborn token carries.
	scopes := []string{
		controller.ScopeNodesClaim, controller.ScopeTriggersClaim,
		controller.ScopeRunsState, controller.ScopeSecretsRead, controller.ScopeLogsWrite,
	}
	mint := func(tenant *store.Tenant, name string) string {
		t.Helper()
		token, _, err := tenant.CreateToken(ctx, name, store.TokenKindRunner, scopes, 0, now)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}

	operator, err := st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	const runID = "run-cli-hello"
	newRun(operator, runID)
	agent := mint(operator, "agent:moonborn")
	stranger := mint(operator, "agent:other")
	if err := st.AsOperator().CreateTeam(ctx, "team-a"); err != nil {
		t.Fatal(err)
	}
	teamA, err := st.ForTeam(ctx, "team-a")
	if err != nil {
		t.Fatal(err)
	}
	newRun(teamA, "run-team-a")
	foreign := mint(teamA, "team-a-runner")

	// safety: built after the tokens exist, because a controller whose tokens
	// table is empty serves every request unauthenticated.
	ctrlSrv := httptest.NewServer(controller.New(st, discardLogger()).
		WithCacheCredentials(cacheSrv.URL, operatorCacheToken).EnableAuthFromStore().Handler())
	t.Cleanup(ctrlSrv.Close)
	claim := func(token, runID, holder string) {
		t.Helper()
		if _, err := client.NewWithToken(ctrlSrv.URL, nil, token).
			ClaimNodeByID(ctx, runID, "hello", holder, time.Minute, false); err != nil {
			t.Fatalf("claim %s: %v", runID, err)
		}
	}
	claim(agent, runID, "agent:moonborn:1")
	claim(foreign, "run-team-a", "agent:team-a:1")

	for name, tc := range map[string]struct {
		runID, token string
		want         int
	}{
		"another team's runner on the operator's run": {runID, foreign, http.StatusNotFound},
		"an operator runner with no claim on the run": {runID, stranger, http.StatusForbidden},
		"another team's runner on its own run":        {"run-team-a", foreign, http.StatusForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			if got, body := oldAgentRegister(t, ctrlSrv.URL, tc.runID, tc.token, repoURL); got != tc.want {
				t.Fatalf("register = %d %s, want %d", got, body, tc.want)
			}
		})
	}
	t.Run("a repository that is not the run's source", func(t *testing.T) {
		const other = "ssh://git@git.example.invalid/acme/other.git"
		if got, body := oldAgentRegister(t, ctrlSrv.URL, runID, agent, other); got != http.StatusForbidden {
			t.Fatalf("register = %d %s, want 403", got, body)
		}
	})

	if got, body := oldAgentRegister(t, ctrlSrv.URL, runID, agent, repoURL); got != http.StatusOK {
		t.Fatalf("the old agent's register = %d %s, want 200", got, body)
	}
	// The old agent then fetches the run's commit through the same proxy,
	// its runner token set as the proxy origin's extraHeader.
	gcURL := ctrlSrv.URL + "/api/v1/runs/" + runID + "/gitcache"
	dest := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dest
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0",
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http."+gcURL+"/.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Bearer "+agent)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	git("init", "--quiet")
	git("remote", "add", "origin", gcURL+"/git/"+sourceurl.ClaimedRepoNameFromURL(repoURL))
	git("fetch", "--depth", "1", "origin", sha)
	git("checkout", "--quiet", "FETCH_HEAD")
	if _, err := os.Stat(filepath.Join(dest, ".sparkwing", "main.go")); err != nil {
		t.Fatalf("the fetched commit has no pipeline: %v", err)
	}
}
