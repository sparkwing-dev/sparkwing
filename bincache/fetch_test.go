package bincache

import (
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
		{"https://github.com/sparkwing-dev/sparkwing-platform.git", "sparkwing-platform"},
		{"https://github.com/sparkwing-dev/sparkwing-platform", "sparkwing-platform"},
		{"https://github.com/sparkwing-dev/sparkwing-platform/", "sparkwing-platform"},
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

// gitExecPath returns the directory containing git-http-backend, or
// "" if `git --exec-path` fails or the binary isn't there. Tests skip
// when this is empty rather than fail; ISS-031 unit tests are
// integration-flavored and not all CI hosts ship git-http-backend.
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

// startGitcacheTestServer mounts git-http-backend CGI at /git/<name>/...
// against repoParent (which holds bare repos named <name>.git) and
// stubs /git/register to 200 OK with a JSON body. Mirrors what the
// production sparkwing-cache pod serves to runners.
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
		// Strip the leading /git so PATH_INFO inside the CGI handler
		// becomes /<name>.git/info/refs etc -- what http-backend wants.
		Root: "/git",
	})
	return httptest.NewServer(mux)
}

// makeBareRepoWithSparkwing builds a bare repo at <repoParent>/<name>.git
// containing two commits on the named branch, both with a `.sparkwing/`
// subdir. Returns the SHA of the first (older) commit and the SHA of
// the branch tip (second commit). Used to verify exact-SHA fetch
// lands at the OLDER SHA when both commits are reachable.
func makeBareRepoWithSparkwing(t *testing.T, repoParent, name, branch string) (oldSHA, tipSHA string) {
	t.Helper()
	if err := os.MkdirAll(repoParent, 0o755); err != nil {
		t.Fatal(err)
	}

	// Working tree where we make commits.
	work := filepath.Join(t.TempDir(), name+"-work")
	mustGit := func(dir string, args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
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
	// Required for fetch-by-SHA to work over smart-HTTP. Mirrors what
	// the production cache pod does in enableSHAFetch.
	mustGit(bare, "config", "uploadpack.allowReachableSHA1InWant", "true")
	// http-backend rejects bare repos that aren't marked exportable.
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
	// .git must exist -- the whole point of ISS-031.
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
	// Build a bare repo with a single commit that does NOT contain a
	// .sparkwing dir. FetchPipelineSource should clone successfully
	// then fail with a clear "no .sparkwing" error.
	work := filepath.Join(t.TempDir(), "noSparkwing-work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
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

	// Wrap the gitcache mux to capture register calls.
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
