package cluster

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/discovery"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// countPaths wraps h and counts the requests whose path contains any of subs.
func countPaths(h http.Handler, n *atomic.Int32, subs ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, sub := range subs {
			if strings.Contains(r.URL.Path, sub) {
				n.Add(1)
				break
			}
		}
		h.ServeHTTP(w, r)
	})
}

// httpsOrigin serves the bare repositories under root as https://git.example.invalid/,
// for every git this process starts, the cache's clone included.
func httpsOrigin(t *testing.T, root string) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	body := "[url \"file://" + root + "/\"]\n\tinsteadOf = https://git.example.invalid/\n" +
		"[protocol \"file\"]\n\tallow = always\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
}

// gitOnlyPath leaves git on PATH and takes the go toolchain off it, so a
// runner that must compile the pipeline fails instead.
func gitOnlyPath(t *testing.T) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(git, filepath.Join(dir, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

// An off-cluster agent whose --gitcache is the controller's proxy claims a node
// of a team other than the operator's, gets the run's grant, and reads source,
// the binary cache and artifacts from the cache the controller announces,
// without one request through the controller's gitcache routes. It uploads the
// binary it compiled; a second runner of the same team, with no go toolchain,
// runs that binary from the cache. A runner of another team, also without a
// toolchain, cannot read it and fails the node.
func TestAnOffClusterAgentUsesTheAnnouncedCacheWithItsGrant(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: clones through a real cache and compiles a pipeline")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	for _, name := range []string{
		"SPARKWING_CONTROLLER_URL", "SPARKWING_LOGS_URL", "SPARKWING_CACHE_URL", "SPARKWING_GITCACHE_URL",
		authwire.CacheTokenEnv, authwire.CacheGrantEnv,
	} {
		t.Setenv(name, "")
	}
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv(authwire.CacheGrantKeyEnv, cacheGrantKey)
	discovery.ResetCache()
	t.Cleanup(discovery.ResetCache)

	origins := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	sha := makePrivateOrigin(t, filepath.Join(origins, "acme", "app.git"), marker)
	const repoURL = "https://git.example.invalid/acme/app.git"
	httpsOrigin(t, origins)

	var cacheGit atomic.Int32
	cacheSrv := newGrantCacheWrapped(t, func(h http.Handler) http.Handler { return countPaths(h, &cacheGit, "/git/") })
	q := neturl.Values{"name": {sourceurl.ClaimedRepoNameFromURL(repoURL)}, "repo": {repoURL}}
	if code, body := post(t, cacheSrv.URL+"/git/register?"+q.Encode(), operatorCacheToken); code != http.StatusOK ||
		!strings.Contains(body, `"cloned":true`) {
		t.Fatalf("the operator registering the public mirror = %d %s", code, body)
	}
	cacheGit.Store(0)

	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	now := time.Now().UTC()
	scopes := []string{
		controller.ScopeNodesClaim, controller.ScopeTriggersClaim,
		controller.ScopeRunsState, controller.ScopeSecretsRead, controller.ScopeLogsWrite,
	}
	team := func(slug string) *store.Tenant {
		t.Helper()
		if err := st.AsOperator().CreateTeam(ctx, store.Team(slug)); err != nil {
			t.Fatal(err)
		}
		tenant, err := st.ForTeam(ctx, store.Team(slug))
		if err != nil {
			t.Fatal(err)
		}
		return tenant
	}
	newRun := func(tenant *store.Tenant, runID string) {
		t.Helper()
		if err := tenant.CreateTriggerWithRun(ctx,
			store.Trigger{
				ID: runID, Pipeline: "hello", Status: "running", CreatedAt: now,
				RepoURL: repoURL, GitBranch: "main", GitSHA: sha,
			},
			store.Run{ID: runID, Pipeline: "hello", Status: "running", StartedAt: now},
		); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: "build", Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		if err := st.MarkNodeReady(ctx, runID, "build"); err != nil {
			t.Fatal(err)
		}
	}
	mint := func(tenant *store.Tenant, name string) string {
		t.Helper()
		token, _, err := tenant.CreateToken(ctx, name, store.TokenKindRunner, scopes, 0, now)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	teamA, teamB := team("team-a"), team("team-b")
	newRun(teamA, "run-a1")
	newRun(teamA, "run-a2")
	newRun(teamB, "run-b1")
	laptopA, podA, laptopB := mint(teamA, "laptop-a"), mint(teamA, "pod-a"), mint(teamB, "laptop-b")

	var proxied atomic.Int32
	// safety: built after the tokens exist, because a controller whose tokens
	// table is empty serves every request unauthenticated.
	ctrlSrv := httptest.NewServer(countPaths(controller.New(st, discardLogger()).
		WithCacheCredentials(cacheSrv.URL, operatorCacheToken).
		WithCachePodURL(cacheSrv.URL).
		EnableAuthFromStore().Handler(), &proxied, "/gitcache"))
	t.Cleanup(ctrlSrv.Close)
	// The counter sees the proxy routes, so a zero below is the agent's choice.
	if code, _ := post(t, ctrlSrv.URL+"/api/v1/runs/run-a1/gitcache/git/register", laptopA); code == 0 || proxied.Load() != 1 {
		t.Fatalf("the proxy counter saw %d requests, want the probe", proxied.Load())
	}
	proxied.Store(0)

	allow, err := sourceurl.ParseRepoAllowlist([]string{"git.example.invalid/acme/*"})
	if err != nil {
		t.Fatal(err)
	}
	execute := func(token, runID string) *store.Node {
		t.Helper()
		ctrl := client.NewWithToken(ctrlSrv.URL, nil, token)
		claimed, err := ctrl.ClaimNodeByID(ctx, runID, "build", "runner:"+runID, time.Minute, false)
		if err != nil {
			t.Fatalf("claim %s: %v", runID, err)
		}
		executePooledNode(ctx, ctrl, ctrlSrv.URL, "", ctrlSrv.URL+"/api/v1/gitcache", allow, token,
			claimed, claimed.ClaimedBy, time.Minute, time.Hour, "pool runner", discardLogger(), nil, nil)
		node, err := st.GetNode(ctx, runID, "build")
		if err != nil {
			t.Fatal(err)
		}
		return node
	}
	ran := func() string {
		t.Helper()
		got, _ := os.ReadFile(marker)
		return string(got)
	}

	if node := execute(laptopA, "run-a1"); node.Outcome == string(sparkwing.Failed) || ran() != "run-node run-a1 build" {
		t.Fatalf("team A's first node = outcome %q error %q, marker %q; want the pipeline run", node.Outcome, node.Error, ran())
	}
	if proxied.Load() != 0 {
		t.Fatalf("%d requests went through the controller's gitcache routes, want none", proxied.Load())
	}
	if cacheGit.Load() == 0 {
		t.Fatal("no source fetch reached the cache")
	}

	key := pipelineKey(t, filepath.Join(origins, "acme", "app.git"))
	grantA := orchestrator.RequestRunCacheGrant(ctx, ctrlSrv.URL, podA, "run-a2", discardLogger())
	grantB := orchestrator.RequestRunCacheGrant(ctx, ctrlSrv.URL, laptopB, "run-b1", discardLogger())
	if err := bincache.TryBinary(ctx, cacheSrv.URL, grantA, key, filepath.Join(t.TempDir(), "bin")); err != nil {
		t.Fatalf("team A's binary is not in the cache: %v", err)
	}
	if err := bincache.TryBinary(ctx, cacheSrv.URL, grantB, key, filepath.Join(t.TempDir(), "bin")); !errors.Is(err, bincache.ErrMiss) {
		t.Fatalf("team B reading team A's binary = %v, want a miss", err)
	}

	gitOnlyPath(t)
	t.Setenv("SPARKWING_HOME", t.TempDir())
	if node := execute(podA, "run-a2"); node.Outcome == string(sparkwing.Failed) || ran() != "run-node run-a2 build" {
		t.Fatalf("team A's second runner = outcome %q error %q, marker %q; want the cached binary run", node.Outcome, node.Error, ran())
	}

	t.Setenv("SPARKWING_HOME", t.TempDir())
	node := execute(laptopB, "run-b1")
	if node.Outcome != string(sparkwing.Failed) || !strings.Contains(node.Error, "go toolchain not on PATH") {
		t.Fatalf("team B's node = outcome %q error %q; want it failed to compile, having read no binary", node.Outcome, node.Error)
	}
	if ran() != "run-node run-a2 build" {
		t.Fatalf("team B's runner ran a binary: marker %q", ran())
	}
	if proxied.Load() != 0 {
		t.Fatalf("%d requests went through the controller's gitcache routes, want none", proxied.Load())
	}
}

func post(t *testing.T, url, bearer string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var b strings.Builder
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	b.Write(buf[:n])
	return resp.StatusCode, b.String()
}

// pipelineKey is the binary cache key of the pipeline at the bare origin's HEAD.
func pipelineKey(t *testing.T, bare string) string {
	t.Helper()
	work := filepath.Join(t.TempDir(), "checkout")
	if out, err := exec.Command("git", "clone", "--quiet", bare, work).CombinedOutput(); err != nil {
		t.Fatalf("clone %s: %v: %s", bare, err, out)
	}
	key, err := bincache.PipelineCacheKey(filepath.Join(work, ".sparkwing"))
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// The announced cache replaces only the controller's proxy or no cache, and
// only for a node that holds its grant.
func TestNodeCacheURLTakesTheAnnouncedCacheOnlyWithAGrant(t *testing.T) {
	const ctrl = "https://ctrl.example.dev"
	const announced = "https://cache.example.dev"
	for name, tc := range map[string]struct {
		flag, announced, grant, want string
	}{
		"proxy with a grant":                 {ctrl + "/api/v1/gitcache", announced, "swcg_x", announced},
		"no cache with a grant":              {"", announced, "swcg_x", announced},
		"proxy without a grant":              {ctrl + "/api/v1/gitcache", announced, "", ctrl + "/api/v1/gitcache"},
		"no cache without a grant":           {"", announced, "", ""},
		"in-cluster cache with a grant":      {"http://sparkwing-cache.sparkwing", announced, "swcg_x", "http://sparkwing-cache.sparkwing"},
		"proxy with a grant, none announced": {ctrl + "/api/v1/gitcache", "", "swcg_x", ctrl + "/api/v1/gitcache"},
	} {
		if got := nodeCacheURL(tc.flag, ctrl, tc.announced, tc.grant); got != tc.want {
			t.Errorf("%s: nodeCacheURL = %q, want %q", name, got, tc.want)
		}
	}
}
