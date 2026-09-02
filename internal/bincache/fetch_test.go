package bincache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoNameFromURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"git@github.com:sparkwing-dev/sparkwing.git", "sparkwing"},
		{"git@github.com:sparkwing-dev/sparkwing", "sparkwing"},
		{"https://github.com/acme/another-repo.git", "another-repo"},
		{"https://github.com/acme/another-repo", "another-repo"},
		{"https://github.com/acme/another-repo/", "another-repo"},
		{"sparkwing-dev/sparkwing", "sparkwing"},
		{"sparkwing", "sparkwing"},
		{"sparkwing.git", "sparkwing"},
		{"  sparkwing  ", "sparkwing"},
		{"", ""},
	}
	for _, c := range cases {
		if got := RepoNameFromURL(c.in); got != c.want {
			t.Errorf("RepoNameFromURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestClaimedRepoNameFromURL_DistinguishesEqualBasenames(t *testing.T) {
	first := ClaimedRepoNameFromURL("https://git.example.com/acme/widgets.git")
	second := ClaimedRepoNameFromURL("https://git.example.com/other/widgets.git")
	if first == second {
		t.Fatalf("claim-scoped names collide: %q", first)
	}
	if !strings.HasPrefix(first, "repo-") || len(first) != 64 {
		t.Fatalf("claim-scoped name = %q", first)
	}
	if got := ClaimedRepoNameFromURL("  https://git.example.com/acme/widgets.git  "); got != first {
		t.Fatalf("normalized claim-scoped name = %q, want %q", got, first)
	}
}

func gitExecPath(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		return ""
	}
	dir := strings.TrimSpace(string(out))
	if _, err := os.Stat(filepath.Join(dir, "git-http-backend")); err != nil {
		return ""
	}
	return dir
}

func startGitcacheTestServer(t *testing.T, repoParent string) *httptest.Server {
	t.Helper()
	execPath := gitExecPath(t)
	if execPath == "" {
		t.Skip("git --exec-path unavailable (no git-http-backend on PATH)")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/git/register", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.Handle("/git/", &cgi.Handler{
		Path: filepath.Join(execPath, "git-http-backend"),
		Env: []string{
			"GIT_PROJECT_ROOT=" + repoParent,
			"GIT_HTTP_EXPORT_ALL=1",
		},
		Root: "/git",
	})
	return httptest.NewServer(mux)
}

func makeBareRepoWithSparkwing(t *testing.T, repoParent, name, branch string) (oldSHA, tipSHA string) {
	t.Helper()
	if err := os.MkdirAll(repoParent, 0o755); err != nil {
		t.Fatal(err)
	}

	work := filepath.Join(t.TempDir(), name+"-work")
	mustGit := func(dir string, args ...string) string {
		cmd := exec.Command("git", append([]string{"-c", "commit.gpgSign=false"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(
			os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(work, "init", "--quiet", "--initial-branch="+branch)
	if err := os.MkdirAll(filepath.Join(work, ".sparkwing"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".sparkwing", "marker"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".gitattributes"), []byte("exact.txt text eol=crlf\nident.txt ident\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "exact.txt"), []byte("exact\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "ident.txt"), []byte("$Id$\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(work, "add", ".")
	mustGit(work, "commit", "--quiet", "-m", "first")
	oldSHA = mustGit(work, "rev-parse", "HEAD")

	if err := os.WriteFile(filepath.Join(work, ".sparkwing", "marker"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(work, "add", ".")
	mustGit(work, "commit", "--quiet", "-m", "second")
	tipSHA = mustGit(work, "rev-parse", "HEAD")

	bare := filepath.Join(repoParent, name+".git")
	mustGit("", "clone", "--bare", "--quiet", work, bare)
	mustGit(bare, "config", "uploadpack.allowReachableSHA1InWant", "true")
	if err := os.WriteFile(filepath.Join(bare, "git-daemon-export-ok"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return oldSHA, tipSHA
}

func TestFetchPipelineSource_PinsToExactSHA(t *testing.T) {
	repoParent := t.TempDir()
	oldSHA, tipSHA := makeBareRepoWithSparkwing(t, repoParent, "sparkwing", "main")
	srv := startGitcacheTestServer(t, repoParent)
	defer srv.Close()

	parentDir := t.TempDir()
	sparkwingDir, err := FetchPipelineSource(srv.URL, "git@github.com:sparkwing-dev/sparkwing.git",
		"main", oldSHA, parentDir)
	if err != nil {
		t.Fatalf("FetchPipelineSource: %v", err)
	}

	workTree := filepath.Dir(sparkwingDir)
	gotSHA, err := exec.Command("git", "-C", workTree, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	got := strings.TrimSpace(string(gotSHA))
	if got != oldSHA {
		t.Errorf("HEAD landed at %s, want pinned %s (tip is %s)", got, oldSHA, tipSHA)
	}
	if _, err := os.Stat(filepath.Join(workTree, ".git")); err != nil {
		t.Errorf("expected .git in %s: %v", workTree, err)
	}
}

func TestFetchPipelineSource_BranchTipFallback_WhenNoSHA(t *testing.T) {
	repoParent := t.TempDir()
	_, tipSHA := makeBareRepoWithSparkwing(t, repoParent, "sparkwing", "main")
	srv := startGitcacheTestServer(t, repoParent)
	defer srv.Close()

	parentDir := t.TempDir()
	sparkwingDir, err := FetchPipelineSource(srv.URL, "git@github.com:sparkwing-dev/sparkwing.git",
		"main", "", parentDir)
	if err != nil {
		t.Fatalf("FetchPipelineSource: %v", err)
	}

	workTree := filepath.Dir(sparkwingDir)
	gotSHA, _ := exec.Command("git", "-C", workTree, "rev-parse", "HEAD").Output()
	got := strings.TrimSpace(string(gotSHA))
	if got != tipSHA {
		t.Errorf("HEAD = %s, want branch tip %s", got, tipSHA)
	}
}

func TestFetchPipelineSource_BadSHA(t *testing.T) {
	repoParent := t.TempDir()
	makeBareRepoWithSparkwing(t, repoParent, "sparkwing", "main")
	srv := startGitcacheTestServer(t, repoParent)
	defer srv.Close()

	parentDir := t.TempDir()
	bogus := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	_, err := FetchPipelineSource(srv.URL, "git@github.com:sparkwing-dev/sparkwing.git",
		"main", bogus, parentDir)
	if err == nil {
		t.Fatal("expected error for nonexistent SHA, got nil")
	}
	if !strings.Contains(err.Error(), "fetch") && !strings.Contains(err.Error(), bogus[:8]) {
		t.Errorf("error should mention the failed fetch / SHA, got: %v", err)
	}
}

func TestFetchPipelineSource_NoSparkwingDir(t *testing.T) {
	repoParent := t.TempDir()
	work := filepath.Join(t.TempDir(), "noSparkwing-work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(
			os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@e.x",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@e.x",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	mustGit(work, "init", "--quiet", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(work, "add", ".")
	mustGit(work, "commit", "--quiet", "-m", "no .sparkwing")

	bare := filepath.Join(repoParent, "noSparkwing.git")
	mustGit("", "clone", "--bare", "--quiet", work, bare)
	mustGit(bare, "config", "uploadpack.allowReachableSHA1InWant", "true")
	if err := os.WriteFile(filepath.Join(bare, "git-daemon-export-ok"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	srv := startGitcacheTestServer(t, repoParent)
	defer srv.Close()

	parentDir := t.TempDir()
	_, err := FetchPipelineSource(srv.URL, "git@github.com:your-org/noSparkwing.git",
		"main", "", parentDir)
	if err == nil {
		t.Fatal("expected error for missing .sparkwing, got nil")
	}
	if !strings.Contains(err.Error(), ".sparkwing") {
		t.Errorf("error should mention .sparkwing, got: %v", err)
	}
}

func TestFetchPipelineSource_RegistersWithCache(t *testing.T) {
	repoParent := t.TempDir()
	_, _ = makeBareRepoWithSparkwing(t, repoParent, "sparkwing", "main")

	execPath := gitExecPath(t)
	if execPath == "" {
		t.Skip("git --exec-path unavailable")
	}
	var registered []string
	mux := http.NewServeMux()
	mux.HandleFunc("/git/register", func(w http.ResponseWriter, r *http.Request) {
		registered = append(registered, fmt.Sprintf("name=%s repo=%s",
			r.URL.Query().Get("name"), r.URL.Query().Get("repo")))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.Handle("/git/", &cgi.Handler{
		Path: filepath.Join(execPath, "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + repoParent, "GIT_HTTP_EXPORT_ALL=1"},
		Root: "/git",
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	parentDir := t.TempDir()
	if _, err := FetchPipelineSource(srv.URL, "git@github.com:sparkwing-dev/sparkwing.git",
		"main", "", parentDir); err != nil {
		t.Fatalf("FetchPipelineSource: %v", err)
	}
	if len(registered) != 1 {
		t.Fatalf("want 1 register call, got %d: %v", len(registered), registered)
	}
	want := "name=sparkwing repo=git@github.com:sparkwing-dev/sparkwing.git"
	if registered[0] != want {
		t.Errorf("register call: got %q, want %q", registered[0], want)
	}
}

func TestFetchPipelineSourceWithToken_AuthenticatesRegisterAndGit(t *testing.T) {
	repoParent := t.TempDir()
	_, tipSHA := makeBareRepoWithSparkwing(t, repoParent, "sparkwing", "main")
	execPath := gitExecPath(t)
	if execPath == "" {
		t.Skip("git --exec-path unavailable")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/gitcache/git/register", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.Handle("/api/v1/gitcache/git/", &cgi.Handler{
		Path: filepath.Join(execPath, "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + repoParent, "GIT_HTTP_EXPORT_ALL=1"},
		Root: "/api/v1/gitcache/git",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer runner-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	defer srv.Close()

	sparkwingDir, err := FetchPipelineSourceWithToken(srv.URL+"/api/v1/gitcache", srv.URL, "runner-token",
		"git@github.com:sparkwing-dev/sparkwing.git", "main", tipSHA, t.TempDir())
	if err != nil {
		t.Fatalf("FetchPipelineSourceWithToken: %v", err)
	}
	got, err := exec.Command("git", "-C", filepath.Dir(sparkwingDir), "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != tipSHA {
		t.Fatalf("HEAD = %q, want %q", strings.TrimSpace(string(got)), tipSHA)
	}
}

func TestFetchPipelineSourceWithToken_DoesNotSendControllerTokenToDirectCache(t *testing.T) {
	repoParent := t.TempDir()
	_, tipSHA := makeBareRepoWithSparkwing(t, repoParent, "sparkwing", "main")
	execPath := gitExecPath(t)
	if execPath == "" {
		t.Skip("git --exec-path unavailable")
	}
	var authorizations []string
	mux := http.NewServeMux()
	mux.HandleFunc("/git/register", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.Handle("/git/", &cgi.Handler{
		Path: filepath.Join(execPath, "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + repoParent, "GIT_HTTP_EXPORT_ALL=1"},
		Root: "/git",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorizations = append(authorizations, r.Header.Get("Authorization"))
		mux.ServeHTTP(w, r)
	}))
	defer srv.Close()

	if _, err := FetchPipelineSourceWithToken(srv.URL, "https://controller.example", "controller-admin-token",
		"git@github.com:sparkwing-dev/sparkwing.git", "main", tipSHA, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	for _, authorization := range authorizations {
		if authorization != "" {
			t.Fatalf("direct cache received controller authorization %q", authorization)
		}
	}
}

func TestFetchPipelineSourceWithToken_ControllerRedirectCannotCarryBearer(t *testing.T) {
	var targetRequests int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetRequests++
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("redirect target received controller authorization %q", got)
		}
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	defer target.Close()
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer runner-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/git/register") {
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		http.Redirect(w, r, target.URL+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	}))
	defer controller.Close()

	_, err := FetchPipelineSourceWithToken(controller.URL+"/api/v1/gitcache", controller.URL, "runner-token",
		"git@github.com:sparkwing-dev/sparkwing.git", "main", strings.Repeat("a", 40), t.TempDir())
	if err == nil {
		t.Fatal("controller Git redirect unexpectedly succeeded")
	}
	if targetRequests != 0 {
		t.Fatalf("redirect target requests = %d, want 0", targetRequests)
	}
}

func TestFetchPipelineSource_DirectCacheRedirectsStayAtConfiguredOrigin(t *testing.T) {
	for _, redirectPath := range []string{"register", "git"} {
		t.Run(redirectPath, func(t *testing.T) {
			var targetRequests int
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				targetRequests++
				w.WriteHeader(http.StatusOK)
			}))
			defer target.Close()
			cache := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if redirectPath == "git" && strings.HasSuffix(r.URL.Path, "/git/register") {
					_, _ = w.Write([]byte(`{"ok":true}`))
					return
				}
				http.Redirect(w, r, target.URL+r.URL.RequestURI(), http.StatusTemporaryRedirect)
			}))
			defer cache.Close()
			_, err := FetchPipelineSourceWithToken(cache.URL, "https://controller.example", "agent-token",
				"git@github.com:sparkwing-dev/sparkwing.git", "main", strings.Repeat("a", 40), t.TempDir())
			if err == nil {
				t.Fatal("direct cache redirect unexpectedly succeeded")
			}
			if targetRequests != 0 {
				t.Fatalf("redirect target requests = %d, want 0", targetRequests)
			}
		})
	}
}

func TestFetchPipelineWorkspaceSource_RestoresRawGitBlobs(t *testing.T) {
	repoParent := t.TempDir()
	_, tipSHA := makeBareRepoWithSparkwing(t, repoParent, "sparkwing", "main")
	bareRepo := filepath.Join(repoParent, "sparkwing.git")
	tree, err := exec.Command("git", "-C", bareRepo, "rev-parse", tipSHA+"^{tree}").Output()
	if err != nil {
		t.Fatal(err)
	}
	commit := exec.Command("git", "-C", bareRepo, "commit-tree", strings.TrimSpace(string(tree)), "-p", tipSHA)
	commit.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Sparkwing", "GIT_AUTHOR_EMAIL=workspace@sparkwing.dev", "GIT_AUTHOR_DATE=2000-01-01T00:00:00Z",
		"GIT_COMMITTER_NAME=Sparkwing", "GIT_COMMITTER_EMAIL=workspace@sparkwing.dev", "GIT_COMMITTER_DATE=2000-01-01T00:00:00Z",
	)
	commit.Stdin = strings.NewReader("sparkwing working-tree snapshot\n")
	out, err := commit.CombinedOutput()
	if err != nil {
		t.Fatalf("commit workspace tree: %v: %s", err, out)
	}
	workspaceSHA := strings.TrimSpace(string(out))
	if out, err := exec.Command("git", "-C", bareRepo, "update-ref", "refs/heads/workspace", workspaceSHA).CombinedOutput(); err != nil {
		t.Fatalf("publish workspace test ref: %v: %s", err, out)
	}
	srv := startGitcacheTestServer(t, repoParent)
	defer srv.Close()

	sparkwingDir, err := FetchPipelineSourceWithToken(srv.URL, "https://controller.example", "ignored-controller-token",
		"git@github.com:sparkwing-dev/sparkwing.git", "main", workspaceSHA, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(sparkwingDir)
	for name, want := range map[string]string{"exact.txt": "exact\n", "ident.txt": "$Id$\n"} {
		got, readErr := os.ReadFile(filepath.Join(root, name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want raw blob %q", name, got, want)
		}
	}
}

func TestControllerGitcacheToken_RequiresExactControllerOriginAndPath(t *testing.T) {
	const token = "controller-admin-token"
	controllerURL := "https://controller.example:8443/sparkwing"
	for name, cacheURL := range map[string]string{
		"foreign origin": "https://attacker.example/api/v1/gitcache",
		"foreign port":   "https://controller.example/sparkwing/api/v1/gitcache",
		"suffix path":    "https://controller.example:8443/other/api/v1/gitcache",
		"query":          "https://controller.example:8443/sparkwing/api/v1/gitcache?target=other",
	} {
		t.Run(name, func(t *testing.T) {
			if got := ControllerGitcacheToken(cacheURL, controllerURL, token); got != "" {
				t.Fatalf("token = %q, want empty", got)
			}
		})
	}
	if got := ControllerGitcacheToken(
		"https://controller.example:8443/sparkwing/api/v1/gitcache/", controllerURL+"/", token,
	); got != token {
		t.Fatalf("token = %q, want %q", got, token)
	}
}

func TestControllerRunGitcacheURL_UsesClaimBoundControllerPath(t *testing.T) {
	controller := "https://controller.example:8443/sparkwing"
	base := controller + "/api/v1/gitcache"
	want := controller + "/api/v1/runs/run-123/gitcache"
	if got := ControllerRunGitcacheURL(base, controller, "run-123"); got != want {
		t.Fatalf("ControllerRunGitcacheURL = %q, want %q", got, want)
	}
	if got := ControllerGitcacheToken(want, controller, "runner-token"); got != "runner-token" {
		t.Fatalf("claim-bound controller token = %q, want runner-token", got)
	}
	direct := "https://cache.tail.example"
	if got := ControllerRunGitcacheURL(direct, controller, "run-123"); got != direct {
		t.Fatalf("direct cache URL changed to %q", got)
	}
}

func TestControllerRunGitcacheURL_UsesClaimScopedRepoIdentity(t *testing.T) {
	controller := "https://controller.example"
	gcURL := controller + "/api/v1/runs/run-123/gitcache"
	repoURL := "https://git.example.com/acme/widgets.git"
	if got := controllerClaimedRepoName(gcURL, controller, repoURL); got != ClaimedRepoNameFromURL(repoURL) {
		t.Fatalf("claim-scoped name = %q", got)
	}
	if got := controllerClaimedRepoName("https://cache.example", controller, repoURL); got != "" {
		t.Fatalf("direct-cache name override = %q", got)
	}
}

func TestFetchPipelineSourceWithCredentials_UsesOnlyTheDirectCacheToken(t *testing.T) {
	repoParent := t.TempDir()
	_, tipSHA := makeBareRepoWithSparkwing(t, repoParent, "sparkwing", "main")
	execPath := gitExecPath(t)
	if execPath == "" {
		t.Skip("git --exec-path unavailable")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/git/register", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.Handle("/git/", &cgi.Handler{
		Path: filepath.Join(execPath, "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + repoParent, "GIT_HTTP_EXPORT_ALL=1"},
		Root: "/git",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer cache-token" {
			http.Error(w, "wrong bearer", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	defer srv.Close()

	if _, err := FetchPipelineSourceWithCredentials(srv.URL, "https://controller.example",
		"controller-token", "cache-token", "git@github.com:sparkwing-dev/sparkwing.git",
		"main", tipSHA, t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestSeedWorkspaceBundle_DoesNotFollowRedirectWithToken(t *testing.T) {
	var targetRequests int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetRequests++
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer cache-token" {
			t.Fatalf("authorization = %q", got)
		}
		http.Redirect(w, r, target.URL+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	bundle := filepath.Join(t.TempDir(), "snapshot.bundle")
	if err := os.WriteFile(bundle, []byte("bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := SeedWorkspaceBundle(context.Background(), source.URL, "cache-token",
		"https://git.example.com/acme/widgets.git", bundle, strings.Repeat("a", 40))
	if err == nil || !strings.Contains(err.Error(), "307") {
		t.Fatalf("error = %v, want redirect rejection", err)
	}
	if targetRequests != 0 {
		t.Fatalf("redirect target requests = %d, want 0", targetRequests)
	}
}

func TestUploadBinary_DoesNotFollowRedirectWithToken(t *testing.T) {
	var targetRequests int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetRequests++
		w.WriteHeader(http.StatusCreated)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer cache-token" {
			t.Fatalf("authorization = %q", got)
		}
		http.Redirect(w, r, target.URL+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	binary := filepath.Join(t.TempDir(), "pipeline")
	if err := os.WriteFile(binary, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := UploadBinary(source.URL, "cache-token", "deadbeef-cafebabe", binary)
	if err == nil || !strings.Contains(err.Error(), "307") {
		t.Fatalf("error = %v, want redirect rejection", err)
	}
	if targetRequests != 0 {
		t.Fatalf("redirect target requests = %d, want 0", targetRequests)
	}
}

func TestTryBinary_DoesNotInstallRedirectedContent(t *testing.T) {
	var targetRequests int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetRequests++
		_, _ = w.Write([]byte("redirected executable"))
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	dest := filepath.Join(t.TempDir(), "pipeline")
	err := TryBinary(source.URL, "", "deadbeef-cafebabe", dest)
	if err == nil || !strings.Contains(err.Error(), "307") {
		t.Fatalf("error = %v, want redirect rejection", err)
	}
	if targetRequests != 0 {
		t.Fatalf("redirect target requests = %d, want 0", targetRequests)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("redirected binary was installed: %v", err)
	}
}

func TestTryBinary_VerifiesAdvertisedDigest(t *testing.T) {
	body := []byte("compiled pipeline bytes")
	sum := sha256.Sum256(body)
	honest := base64.StdEncoding.EncodeToString(sum[:])
	other := sha256.Sum256([]byte("attacker bytes"))

	cases := []struct {
		name    string
		digest  string
		served  []byte
		wantErr bool
	}{
		{name: "matching digest", digest: "sha-256=" + honest, served: body},
		{name: "tampered body", digest: "sha-256=" + honest, served: []byte("attacker bytes"), wantErr: true},
		{name: "tampered digest", digest: "sha-256=" + base64.StdEncoding.EncodeToString(other[:]), served: body, wantErr: true},
		{name: "absent digest", digest: "", served: body, wantErr: true},
		{name: "unsupported algorithm", digest: "md5=" + honest, served: body, wantErr: true},
		{name: "undecodable digest", digest: "sha-256=not-base64!", served: body, wantErr: true},
		{name: "short digest", digest: "sha-256=" + base64.StdEncoding.EncodeToString(sum[:16]), served: body, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.digest != "" {
					w.Header().Set("Digest", tc.digest)
				}
				_, _ = w.Write(tc.served)
			}))
			defer srv.Close()

			dest := filepath.Join(t.TempDir(), "pipeline")
			err := TryBinary(srv.URL, "cache-token", "deadbeef-cafebabe", dest)
			if tc.wantErr {
				if !errors.Is(err, ErrDigest) {
					t.Fatalf("err = %v, want ErrDigest", err)
				}
				if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
					t.Fatalf("unverified binary was installed: %v", statErr)
				}
				if _, statErr := os.Stat(dest + ".tmp"); !os.IsNotExist(statErr) {
					t.Errorf("temp download left behind: %v", statErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("TryBinary: %v", err)
			}
			got, readErr := os.ReadFile(dest)
			if readErr != nil {
				t.Fatalf("read dest: %v", readErr)
			}
			if !bytes.Equal(got, body) {
				t.Errorf("payload = %q, want %q", got, body)
			}
		})
	}
}

func TestUploadBinary_RejectsDifferentStoredDigest(t *testing.T) {
	other := sha256.Sum256([]byte("something else"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Digest", "sha-256="+base64.StdEncoding.EncodeToString(other[:]))
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	src := filepath.Join(t.TempDir(), "pipeline")
	if err := os.WriteFile(src, []byte("compiled pipeline bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := UploadBinary(srv.URL, "cache-token", "deadbeef-cafebabe", src)
	if !errors.Is(err, ErrDigest) {
		t.Fatalf("err = %v, want ErrDigest", err)
	}
}

func TestTryBinary_SendsTokenAsBearer(t *testing.T) {
	for _, tc := range []struct {
		name  string
		token string
		want  string
	}{
		{name: "token", token: "cache-token", want: "Bearer cache-token"},
		{name: "no token", token: "", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var authz string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				authz = r.Header.Get("Authorization")
				w.WriteHeader(http.StatusNotFound)
			}))
			defer srv.Close()

			dest := filepath.Join(t.TempDir(), "pipeline")
			if err := TryBinary(srv.URL, tc.token, "deadbeef-cafebabe", dest); !errors.Is(err, ErrMiss) {
				t.Fatalf("error = %v, want ErrMiss", err)
			}
			if authz != tc.want {
				t.Fatalf("Authorization = %q, want %q", authz, tc.want)
			}
		})
	}
}
