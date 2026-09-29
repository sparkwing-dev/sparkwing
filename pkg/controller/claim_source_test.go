package controller_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (f *appFixture) launchedRun(who signedIn, runID, owner, name string) string {
	f.t.Helper()
	ctx := store.WithoutCreditMetering(context.Background())
	now := time.Now()
	if _, err := f.store.DB().ExecContext(ctx, `INSERT INTO repos (team, repo, dispatch, updated_at) VALUES (?, ?, ?, ?)
ON CONFLICT DO NOTHING`, who.team, store.RepoKey(owner, name), string(store.RepoDispatchController), now.UnixNano()); err != nil {
		f.t.Fatal(err)
	}
	tn, err := f.store.ForTeam(ctx, store.Team(who.team))
	if err != nil {
		f.t.Fatal(err)
	}
	if err := tn.CreateTriggerWithRun(ctx, store.Trigger{
		ID: runID, Pipeline: "build", GithubOwner: owner, GithubRepo: name, Repo: owner + "/" + name,
		GitBranch: "main", GitSHA: headSHA, CreatedAt: now,
	}, store.Run{
		ID: runID, Pipeline: "build", Status: "pending", GithubOwner: owner, GithubRepo: name,
		DeclaredRepo: owner + "/" + name, CreatedAt: now, StartedAt: now,
	}); err != nil {
		f.t.Fatal(err)
	}
	c, err := f.store.ClaimLaunch(ctx, store.ClaimIdentity{Principal: "launcher", TokenPrefix: "swr_launch"},
		store.LaunchClaimRequest{
			HolderID: "launcher:" + runID, Lease: time.Minute, Deadline: time.Hour,
			RunID: runID, NodeID: store.PlanNodeID,
		}, time.Now())
	if err != nil || c == nil {
		f.t.Fatalf("launch claim %s: %+v %v", runID, c, err)
	}
	return c.Token
}

func (f *appFixture) get(path, token string) (int, []byte) {
	f.t.Helper()
	req, err := http.NewRequest(http.MethodGet, f.url+path, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	return resp.StatusCode, body
}

type sourceCall struct {
	repoURL, sha, branch string
	cred                 bincache.DirectCredential
	opts                 bincache.SourceOptions
}

// The claim's run is checked out from its own recorded repository and commit,
// with the team's App token read-only on that repository, and served with its
// .git. Another run, a malformed ask, and a claim cancelled while its fetch
// ran are all refused.
func TestRunSource_ServesTheClaimsOwnCheckout(t *testing.T) {
	var calls []sourceCall
	var during func()
	controller.StubTrustedFetch(t, func(_ context.Context, repoURL, sha, branch, dest string, cred bincache.DirectCredential, o bincache.SourceOptions) error {
		calls = append(calls, sourceCall{repoURL, sha, branch, cred, o})
		if during != nil {
			during()
		}
		if err := os.MkdirAll(filepath.Join(dest, ".git"), 0o755); err != nil {
			return err
		}
		return errors.Join(os.WriteFile(filepath.Join(dest, ".git", "HEAD"), []byte(sha+"\n"), 0o644),
			os.WriteFile(filepath.Join(dest, "main.go"), []byte("package main\n"), 0o644))
	}, nil, nil)
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	tok := f.launchedRun(olga, "run-src", "acme", "widgets")

	code, body := f.get("/api/v1/runs/run-src/source?depth=0&tags=1&submodules=1", tok)
	if code != http.StatusOK {
		t.Fatalf("source = %d %s", code, body)
	}
	files := untar(t, body)
	if files[".git/HEAD"] != headSHA+"\n" || files["main.go"] != "package main\n" {
		t.Fatalf("tarball = %v", files)
	}
	c := calls[0]
	if c.repoURL != "https://github.com/acme/widgets.git" || c.sha != headSHA || c.branch != "main" ||
		c.opts != (bincache.SourceOptions{Depth: 0, Tags: true, Submodules: true}) {
		t.Fatalf("checkout = %+v", c)
	}
	if c.cred.Kind != bincache.CredentialGitHubApp || !f.app.TokenCovers(c.cred.Secret, "acme/widgets") ||
		f.app.TokenCovers(c.cred.Secret, "acme/plans") {
		t.Fatalf("credential = %+v, want the App token for acme/widgets alone", c.cred)
	}
	if code, _ := f.get("/api/v1/runs/run-src/source", tok); code != http.StatusOK || calls[1].opts.Depth != 1 {
		t.Fatalf("default ask = %d %+v, want depth 1", code, calls[1].opts)
	}

	f.launchedRun(olga, "run-sibling", "acme", "widgets")
	if code, body := f.get("/api/v1/runs/run-sibling/source", tok); code != http.StatusForbidden || !bytes.Contains(body, []byte("claim_mismatch")) {
		t.Fatalf("a sibling run's source = %d %s, want 403 claim_mismatch", code, body)
	}
	if code, _ := f.get("/api/v1/runs/run-src/source?depth=-1", tok); code != http.StatusBadRequest {
		t.Fatalf("negative depth = %d, want 400", code)
	}
	during = func() {
		if err := f.store.RequestCancel(context.Background(), "run-src"); err != nil {
			t.Error(err)
		}
	}
	code, body = f.get("/api/v1/runs/run-src/source", tok)
	if code != http.StatusForbidden || bytes.Contains(body, []byte("package main")) {
		t.Fatalf("cancelled during the fetch = %d %q, want 403 and no tree", code, body)
	}
	fetched := len(calls)
	if code, _ := f.get("/api/v1/runs/run-src/source", tok); code != http.StatusForbidden || len(calls) != fetched {
		t.Fatalf("after cancel = %d with %d fetches, want 403 before any fetch", code, len(calls)-fetched)
	}
}

// The module proxy serves a module only from a repository a team owner
// listed for the run's repository, and builds its files from the commit the
// version names.
func TestRunGoProxy_ServesOnlyTheOwnersListedModules(t *testing.T) {
	repo, commit := moduleRepo(t)
	var fetches []string
	var creds []bincache.DirectCredential
	controller.StubTrustedFetch(t, nil,
		func(_ context.Context, repoURL, rev, _ string, cred bincache.DirectCredential) (bincache.ModuleCommit, error) {
			fetches = append(fetches, repoURL+" "+rev)
			creds = append(creds, cred)
			return bincache.ModuleCommit{Dir: repo, Commit: commit, Time: time.Unix(1700000000, 0).UTC()}, nil
		},
		func(_ context.Context, repoURL, prefix, _ string, _ bincache.DirectCredential) ([]string, error) {
			fetches = append(fetches, "list "+repoURL+" "+prefix)
			return []string{"v1.2.3", "v2.0.0", "v1.3.0-rc.1", "vbad"}, nil
		})
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	f.connect(olga, 501, 7, acmeAdmin)
	tok := f.launchedRun(olga, "run-mod", "acme", "widgets")
	const base = "/api/v1/runs/run-mod/goproxy/github.com/acme/plans/@v/"

	if code, _ := f.get(base+"v1.2.3.mod", tok); code != http.StatusNotFound || len(fetches) != 0 {
		t.Fatalf("an unlisted module = %d after %v, want 404 and no fetch", code, fetches)
	}
	if code, out := f.setExtraRepos(olga, "acme/widgets", []string{"acme/plans"}); code != http.StatusOK {
		t.Fatalf("extra repos = %d %v", code, out)
	}
	code, body := f.get(base+"v1.2.3.mod", tok)
	if code != http.StatusOK || string(body) != "module github.com/acme/plans\n\ngo 1.22\n" {
		t.Fatalf(".mod = %d %q", code, body)
	}
	if fetches[0] != "https://github.com/acme/plans.git refs/tags/v1.2.3" || !f.app.TokenCovers(creds[0].Secret, "acme/plans") {
		t.Fatalf("fetch = %v with %+v", fetches, creds[0])
	}
	// safety: a build reads a go.mod per version in its graph, so repeated asks are not a loop to refuse.
	minted := len(f.app.Minted())
	for range 12 {
		if code, _ := f.get(base+"v1.2.3.mod", tok); code != http.StatusOK {
			t.Fatalf("a repeated .mod = %d", code)
		}
	}
	if n := len(f.app.Minted()) - minted; n != 0 {
		t.Fatalf("repeated asks minted %d tokens, want the one reused", n)
	}
	if code, body := f.get(base+"v1.2.3.info", tok); code != http.StatusOK ||
		!strings.Contains(string(body), `"Version":"v1.2.3"`) || !strings.Contains(string(body), `"Time":"2023-11-14T22:13:20Z"`) {
		t.Fatalf(".info = %d %s", code, body)
	}
	code, body = f.get(base+"v0.0.0-20231114221320-"+commit[:12]+".zip", tok)
	if code != http.StatusOK {
		t.Fatalf(".zip = %d %s", code, body)
	}
	if last := fetches[len(fetches)-1]; !strings.HasSuffix(last, " "+commit[:12]) {
		t.Fatalf("a pseudo-version fetched %q, want its commit prefix", last)
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, zf := range zr.File {
		names = append(names, zf.Name)
	}
	if want := "github.com/acme/plans@v0.0.0-20231114221320-" + commit[:12] + "/"; len(names) != 2 ||
		names[0] != want+"go.mod" && names[1] != want+"go.mod" {
		t.Fatalf("zip = %v", names)
	}
	if code, body := f.get(base+"list", tok); code != http.StatusOK || string(body) != "v1.2.3\nv1.3.0-rc.1\n" {
		t.Fatalf("list = %d %q, want the v0/v1 semver tags", code, body)
	}
	for _, path := range []string{
		"/api/v1/runs/run-mod/goproxy/github.com/acme/plans/@latest",
		"/api/v1/runs/run-mod/goproxy/example.com/acme/plans/@v/v1.2.3.mod",
		base + "v1.2.3.tar",
		"/api/v1/runs/run-other/goproxy/github.com/acme/plans/@v/v1.2.3.mod",
	} {
		if code, _ := f.get(path, tok); code != http.StatusNotFound && code != http.StatusForbidden {
			t.Fatalf("%s = %d, want a refusal", path, code)
		}
	}
}

// A claim token gets a cache grant bound to its claim: every controller use
// of the grant re-checks the claim, so it stops working when the run is
// cancelled, and a plan claim's grant uploads only a binary.
func TestRunCacheGrant_IsBoundToTheClaim(t *testing.T) {
	t.Setenv(authwire.CacheGrantKeyEnv, "claim-grant-signing-key")
	f := newAppFixture(t)
	olga := f.ghUser(501, "olga")
	tok := f.launchedRun(olga, "run-grant", "acme", "widgets")
	var out controller.CacheGrantResponse
	if code := f.call("POST", "/api/v1/runs/run-grant/cache-grant", "Bearer "+tok, map[string]any{}, &out); code != http.StatusOK {
		t.Fatalf("cache grant = %d", code)
	}
	grant, err := authwire.VerifyCacheGrant("claim-grant-signing-key", out.Grant, time.Now())
	if err != nil || grant.Claim == nil || grant.Claim.Kind != authwire.CacheClaimToken || grant.Team != olga.team ||
		grant.Run != "run-grant" || grant.Claim.NodeID != store.PlanNodeID {
		t.Fatalf("grant = %+v, %v", grant, err)
	}
	ctx := context.Background()
	if binaryOnly, err := controller.VerifyLiveDataGrant(ctx, f.srv, out.Grant); err != nil || !binaryOnly {
		t.Fatalf("live plan grant = %v, %v; want accepted and binary-only", binaryOnly, err)
	}
	if err := f.store.RequestCancel(ctx, "run-grant"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.VerifyLiveDataGrant(ctx, f.srv, out.Grant); err == nil {
		t.Fatal("the grant outlived the claim's cancel")
	}
	if code := f.call("POST", "/api/v1/runs/run-grant/cache-grant", "Bearer "+tok, map[string]any{}, &out); code != http.StatusForbidden {
		t.Fatalf("cache grant after cancel = %d, want 403", code)
	}
}

func moduleRepo(t *testing.T) (dir, commit string) {
	t.Helper()
	dir = t.TempDir()
	env := append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL="+os.DevNull)
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "--quiet", "-b", "main")
	for name, body := range map[string]string{"go.mod": "module github.com/acme/plans\n\ngo 1.22\n", "plans.go": "package plans\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("commit", "--quiet", "-m", "plans")
	return dir, git("rev-parse", "HEAD")
}

func untar(t *testing.T, body []byte) map[string]string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string]string{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			out[h.Name] = string(b)
		}
	}
}
