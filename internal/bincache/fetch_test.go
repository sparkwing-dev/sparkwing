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

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

const testRepoSSH = "git@github.com:sparkwing-dev/sparkwing.git"

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
	oldSHA, tipSHA := makeBareRepoWithSparkwing(t, repoParent, sourceurl.ClaimedRepoNameFromURL(testRepoSSH), "main")
	srv := startGitcacheTestServer(t, repoParent)
	defer srv.Close()

	parentDir := t.TempDir()
	sparkwingDir, err := FetchPipelineSource(context.Background(), srv.URL, "git@github.com:sparkwing-dev/sparkwing.git",
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
	_, tipSHA := makeBareRepoWithSparkwing(t, repoParent, sourceurl.ClaimedRepoNameFromURL(testRepoSSH), "main")
	srv := startGitcacheTestServer(t, repoParent)
	defer srv.Close()

	parentDir := t.TempDir()
	sparkwingDir, err := FetchPipelineSource(context.Background(), srv.URL, "git@github.com:sparkwing-dev/sparkwing.git",
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
	makeBareRepoWithSparkwing(t, repoParent, sourceurl.ClaimedRepoNameFromURL(testRepoSSH), "main")
	srv := startGitcacheTestServer(t, repoParent)
	defer srv.Close()

	parentDir := t.TempDir()
	bogus := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	_, err := FetchPipelineSource(context.Background(), srv.URL, "git@github.com:sparkwing-dev/sparkwing.git",
		"main", bogus, parentDir)
	if err == nil {
		t.Fatal("expected error for nonexistent SHA, got nil")
	}
	if !strings.Contains(err.Error(), "fetch") && !strings.Contains(err.Error(), bogus[:8]) {
		t.Errorf("error should mention the failed fetch / SHA, got: %v", err)
	}
}

func TestFetchExactSHARejectsARevisionThatIsNotAnObjectID(t *testing.T) {
	for _, sha := range []string{"--upload-pack=evil", "main", "abc1234", "HEAD", ""} {
		dest := t.TempDir()
		err := fetchExactSHA(context.Background(), "https://cache.example", "https://cache.example/git/widgets", "", sha, dest)
		if err == nil || !strings.Contains(err.Error(), "hex object id") {
			t.Errorf("fetchExactSHA(context.Background(), %q) error = %v, want a rejected object id", sha, err)
		}
		if _, statErr := os.Stat(filepath.Join(dest, ".git")); statErr == nil {
			t.Errorf("fetchExactSHA(context.Background(), %q) ran git before validating the revision", sha)
		}
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

	bare := filepath.Join(repoParent, sourceurl.ClaimedRepoNameFromURL("git@github.com:your-org/noSparkwing.git")+".git")
	mustGit("", "clone", "--bare", "--quiet", work, bare)
	mustGit(bare, "config", "uploadpack.allowReachableSHA1InWant", "true")
	if err := os.WriteFile(filepath.Join(bare, "git-daemon-export-ok"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	srv := startGitcacheTestServer(t, repoParent)
	defer srv.Close()

	parentDir := t.TempDir()
	_, err := FetchPipelineSource(context.Background(), srv.URL, "git@github.com:your-org/noSparkwing.git",
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
	_, _ = makeBareRepoWithSparkwing(t, repoParent, sourceurl.ClaimedRepoNameFromURL(testRepoSSH), "main")

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
	if _, err := FetchPipelineSource(context.Background(), srv.URL, "git@github.com:sparkwing-dev/sparkwing.git",
		"main", "", parentDir); err != nil {
		t.Fatalf("FetchPipelineSource: %v", err)
	}
	if len(registered) != 1 {
		t.Fatalf("want 1 register call, got %d: %v", len(registered), registered)
	}
	want := "name=" + sourceurl.ClaimedRepoNameFromURL(testRepoSSH) + " repo=" + testRepoSSH
	if registered[0] != want {
		t.Errorf("register call: got %q, want %q", registered[0], want)
	}
}

func TestFetchPipelineSource_RegistersDistinctNamesForEqualBasenames(t *testing.T) {
	repoParent := t.TempDir()
	first := "git@github.com:acme/utils.git"
	second := "git@github.com:other/utils.git"
	for _, repoURL := range []string{first, second} {
		makeBareRepoWithSparkwing(t, repoParent, sourceurl.ClaimedRepoNameFromURL(repoURL), "main")
	}

	execPath := gitExecPath(t)
	if execPath == "" {
		t.Skip("git --exec-path unavailable")
	}
	registered := map[string]string{}
	mux := http.NewServeMux()
	mux.HandleFunc("/git/register", func(w http.ResponseWriter, r *http.Request) {
		name, repoURL := r.URL.Query().Get("name"), r.URL.Query().Get("repo")
		if bound, ok := registered[name]; ok && bound != repoURL {
			http.Error(w, "name is already registered to another repository", http.StatusConflict)
			return
		}
		registered[name] = repoURL
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

	for _, repoURL := range []string{first, second} {
		if _, err := FetchPipelineSource(context.Background(), srv.URL, repoURL, "main", "", t.TempDir()); err != nil {
			t.Fatalf("FetchPipelineSource(%s): %v", repoURL, err)
		}
	}
	if len(registered) != 2 {
		t.Fatalf("two repositories registered %d name(s): %v", len(registered), registered)
	}
}

func TestFetchPipelineSourceWithCredentials_AuthenticatesRegisterAndGit(t *testing.T) {
	repoParent := t.TempDir()
	_, tipSHA := makeBareRepoWithSparkwing(t, repoParent, sourceurl.ClaimedRepoNameFromURL(testRepoSSH), "main")
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

	sparkwingDir, err := FetchPipelineSourceWithCredentials(context.Background(), srv.URL+"/api/v1/gitcache", srv.URL, "runner-token", "",
		"git@github.com:sparkwing-dev/sparkwing.git", "main", tipSHA, t.TempDir())
	if err != nil {
		t.Fatalf("FetchPipelineSourceWithCredentials: %v", err)
	}
	got, err := exec.Command("git", "-C", filepath.Dir(sparkwingDir), "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != tipSHA {
		t.Fatalf("HEAD = %q, want %q", strings.TrimSpace(string(got)), tipSHA)
	}
}

func TestFetchPipelineSourceWithCredentials_DoesNotSendControllerTokenToDirectCache(t *testing.T) {
	repoParent := t.TempDir()
	_, tipSHA := makeBareRepoWithSparkwing(t, repoParent, sourceurl.ClaimedRepoNameFromURL(testRepoSSH), "main")
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

	if _, err := FetchPipelineSourceWithCredentials(context.Background(), srv.URL, "https://controller.example", "controller-admin-token", "",
		"git@github.com:sparkwing-dev/sparkwing.git", "main", tipSHA, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	for _, authorization := range authorizations {
		if authorization != "" {
			t.Fatalf("direct cache received controller authorization %q", authorization)
		}
	}
}

func TestFetchPipelineSourceWithCredentials_ControllerRedirectCannotCarryBearer(t *testing.T) {
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

	_, err := FetchPipelineSourceWithCredentials(context.Background(), controller.URL+"/api/v1/gitcache", controller.URL, "runner-token", "",
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
			_, err := FetchPipelineSourceWithCredentials(context.Background(), cache.URL, "https://controller.example", "agent-token", "",
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
	_, tipSHA := makeBareRepoWithSparkwing(t, repoParent, sourceurl.ClaimedRepoNameFromURL(testRepoSSH), "main")
	bareRepo := filepath.Join(repoParent, sourceurl.ClaimedRepoNameFromURL(testRepoSSH)+".git")
	tree, err := exec.Command("git", "-C", bareRepo, "rev-parse", tipSHA+"^{tree}").Output()
	if err != nil {
		t.Fatal(err)
	}
	commit := exec.Command("git", "-C", bareRepo, "commit-tree", strings.TrimSpace(string(tree)))
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

	sparkwingDir, err := FetchPipelineSourceWithCredentials(context.Background(), srv.URL, "https://controller.example", "ignored-controller-token", "",
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
	if got := controllerClaimedRepoName(gcURL, controller, repoURL); got != sourceurl.ClaimedRepoNameFromURL(repoURL) {
		t.Fatalf("claim-scoped name = %q", got)
	}
	if got := controllerClaimedRepoName("https://cache.example", controller, repoURL); got != "" {
		t.Fatalf("direct-cache name override = %q", got)
	}
}

func TestFetchPipelineSourceWithCredentials_UsesOnlyTheDirectCacheToken(t *testing.T) {
	repoParent := t.TempDir()
	_, tipSHA := makeBareRepoWithSparkwing(t, repoParent, sourceurl.ClaimedRepoNameFromURL(testRepoSSH), "main")
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

	if _, err := FetchPipelineSourceWithCredentials(context.Background(), srv.URL, "https://controller.example",
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
	err := UploadBinary(context.Background(), source.URL, "cache-token", "deadbeef-cafebabe", binary)
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
	err := TryBinary(context.Background(), source.URL, "", "deadbeef-cafebabe", dest)
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
			err := TryBinary(context.Background(), srv.URL, "cache-token", "deadbeef-cafebabe", dest)
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
	err := UploadBinary(context.Background(), srv.URL, "cache-token", "deadbeef-cafebabe", src)
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
			if err := TryBinary(context.Background(), srv.URL, tc.token, "deadbeef-cafebabe", dest); !errors.Is(err, ErrMiss) {
				t.Fatalf("error = %v, want ErrMiss", err)
			}
			if authz != tc.want {
				t.Fatalf("Authorization = %q, want %q", authz, tc.want)
			}
		})
	}
}

func workspaceSnapshotCommit(t *testing.T, bareRepo, baseSHA, addPath, addContent string) string {
	t.Helper()
	index := filepath.Join(t.TempDir(), "index")
	run := func(env []string, args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", bareRepo}, args...)...)
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	indexEnv := []string{"GIT_INDEX_FILE=" + index}
	run(indexEnv, "read-tree", baseSHA)
	writeBlob := exec.Command("git", "-C", bareRepo, "hash-object", "-w", "--stdin")
	writeBlob.Stdin = strings.NewReader(addContent)
	blobOut, err := writeBlob.Output()
	if err != nil {
		t.Fatalf("hash-object: %v", err)
	}
	blob := strings.TrimSpace(string(blobOut))
	run(indexEnv, "update-index", "--add", "--cacheinfo", "100644,"+blob+","+addPath)
	tree := run(indexEnv, "write-tree")

	commit := exec.Command("git", "-C", bareRepo, "commit-tree", tree)
	commit.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Sparkwing", "GIT_AUTHOR_EMAIL=workspace@sparkwing.dev", "GIT_AUTHOR_DATE=2000-01-01T00:00:00Z",
		"GIT_COMMITTER_NAME=Sparkwing", "GIT_COMMITTER_EMAIL=workspace@sparkwing.dev", "GIT_COMMITTER_DATE=2000-01-01T00:00:00Z",
	)
	commit.Stdin = strings.NewReader("sparkwing working-tree snapshot\n")
	out, err := commit.CombinedOutput()
	if err != nil {
		t.Fatalf("commit workspace tree: %v: %s", err, out)
	}
	sha := strings.TrimSpace(string(out))
	run(nil, "update-ref", "refs/heads/workspace", sha)
	return sha
}

func TestAdoptWorkspaceBaseline_ResolvesTheBaselineAStepDiffsAgainst(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	repoParent := t.TempDir()
	name := sourceurl.ClaimedRepoNameFromURL(testRepoSSH)
	baseSHA, tipSHA := makeBareRepoWithSparkwing(t, repoParent, name, "main")
	bareRepo := filepath.Join(repoParent, name+".git")
	workspaceSHA := workspaceSnapshotCommit(t, bareRepo, tipSHA, "added.txt", "added\n")
	srv := startGitcacheTestServer(t, repoParent)
	defer srv.Close()

	for _, tc := range []struct {
		name  string
		adopt bool
		want  []string
	}{
		{name: "with the recorded baseline", adopt: true, want: []string{".sparkwing/marker", "added.txt"}},
		{name: "without one"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sparkwingDir, err := FetchPipelineWorkspaceSourceWithCredentials(context.Background(), srv.URL, "https://controller.example", "ignored", "",
				testRepoSSH, "main", workspaceSHA, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Dir(sparkwingDir)
			if tc.adopt {
				if err := AdoptWorkspaceBaseline(context.Background(), root, srv.URL, "",
					workspaceSHA, WorkspaceBaseline{Ref: "origin/main", SHA: baseSHA}); err != nil {
					t.Fatalf("AdoptWorkspaceBaseline: %v", err)
				}
			}
			base, mergeBaseErr := exec.Command("git", "-C", root, "merge-base", "origin/main", "HEAD").Output()
			if !tc.adopt {
				if mergeBaseErr == nil {
					t.Fatalf("a checkout with no adopted baseline resolved origin/main at %s", strings.TrimSpace(string(base)))
				}
				return
			}
			if mergeBaseErr != nil {
				t.Fatalf("merge-base origin/main HEAD: %v", mergeBaseErr)
			}
			if got := strings.TrimSpace(string(base)); got != baseSHA {
				t.Fatalf("merge-base = %s, want the recorded baseline %s", got, baseSHA)
			}
			changed, err := exec.Command("git", "-C", root, "diff", "--name-only", "--diff-filter=ACMR", baseSHA).Output()
			if err != nil {
				t.Fatalf("diff against the baseline: %v", err)
			}
			got := strings.Fields(string(changed))
			if len(got) != len(tc.want) {
				t.Fatalf("changed files = %v, want %v", got, tc.want)
			}
			for i, want := range tc.want {
				if got[i] != want {
					t.Fatalf("changed files = %v, want %v", got, tc.want)
				}
			}
			if head, headErr := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output(); headErr != nil {
				t.Fatal(headErr)
			} else if strings.TrimSpace(string(head)) != workspaceSHA {
				t.Fatalf("HEAD = %s, want the snapshot commit %s", strings.TrimSpace(string(head)), workspaceSHA)
			}
		})
	}
}

func TestAdoptWorkspaceBaseline_SkipsASourceThatServesOnlyTheSnapshot(t *testing.T) {
	repoParent := t.TempDir()
	name := sourceurl.ClaimedRepoNameFromURL(testRepoSSH)
	baseSHA, tipSHA := makeBareRepoWithSparkwing(t, repoParent, name, "main")
	bareRepo := filepath.Join(repoParent, name+".git")
	workspaceSHA := workspaceSnapshotCommit(t, bareRepo, tipSHA, "added.txt", "added\n")
	srv := startGitcacheTestServer(t, repoParent)
	defer srv.Close()

	sparkwingDir, err := FetchPipelineWorkspaceSourceWithCredentials(context.Background(), srv.URL, "https://controller.example", "ignored", "",
		testRepoSSH, "main", workspaceSHA, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(sparkwingDir)

	// safety: a local fleet run serves its own bundle this way, with the snapshot ref and nothing else.
	snapshotOnly := filepath.Join(t.TempDir(), "snapshot-only")
	mustGit(t, "", "clone", "--bare", "--quiet", bareRepo, snapshotOnly)
	mustGit(t, snapshotOnly, "update-ref", SeedRef(workspaceSHA), workspaceSHA)
	refs := strings.Fields(mustGit(t, snapshotOnly, "for-each-ref", "--format=%(refname)"))
	for _, ref := range refs {
		if ref != SeedRef(workspaceSHA) {
			mustGit(t, snapshotOnly, "update-ref", "-d", ref)
		}
	}
	mustGit(t, root, "remote", "set-url", "origin", snapshotOnly)

	err = AdoptWorkspaceBaseline(context.Background(), root, srv.URL, "",
		workspaceSHA, WorkspaceBaseline{Ref: "origin/main", SHA: baseSHA})
	if !errors.Is(err, ErrBaselineUnservable) {
		t.Fatalf("AdoptWorkspaceBaseline = %v, want ErrBaselineUnservable", err)
	}
	if out, refErr := exec.Command("git", "-C", root, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/main").Output(); refErr == nil {
		t.Fatalf("a source that serves only the snapshot still named origin/main at %s", strings.TrimSpace(string(out)))
	}
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// The operator's own grants register the mirror a run needs, and the cache
// refuses every other team's, so a grant holder asks and a refusal leaves the
// already-registered mirror to serve the clone.
func TestFetchPipelineSourceWithAGrantSurvivesARefusedRegistration(t *testing.T) {
	execPath := gitExecPath(t)
	if execPath == "" {
		t.Skip("git --exec-path unavailable (no git-http-backend on PATH)")
	}
	repoParent := t.TempDir()
	_, tipSHA := makeBareRepoWithSparkwing(t, repoParent, sourceurl.ClaimedRepoNameFromURL(testRepoSSH), "main")
	registered := false
	mux := http.NewServeMux()
	mux.HandleFunc("/git/register", func(w http.ResponseWriter, r *http.Request) {
		registered = true
		http.Error(w, "mirror registration takes the cache's operator token", http.StatusForbidden)
	})
	mux.Handle("/git/", &cgi.Handler{
		Path: filepath.Join(execPath, "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + repoParent, "GIT_HTTP_EXPORT_ALL=1"},
		Root: "/git",
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv(authwire.CacheGrantEnv, authwire.CacheGrantPrefix+"payload.sig")

	sparkwingDir, err := FetchPipelineSource(context.Background(), srv.URL, testRepoSSH, "main", tipSHA, t.TempDir())
	if err != nil {
		t.Fatalf("FetchPipelineSource with a grant: %v", err)
	}
	if !registered {
		t.Error("a grant holder never asked the cache to register the mirror")
	}
	if _, err := os.Stat(filepath.Join(sparkwingDir, "marker")); err != nil {
		t.Fatalf("fetched tree: %v", err)
	}
}
