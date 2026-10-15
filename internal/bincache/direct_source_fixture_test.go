package bincache

import (
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitExecPath(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		return ""
	}
	dir := strings.TrimSpace(string(out))
	if _, err := exec.LookPath(filepath.Join(dir, "git-http-backend")); err != nil {
		return ""
	}
	return dir
}

func gitHTTPBackend(execPath string) string {
	path, _ := exec.LookPath(filepath.Join(execPath, "git-http-backend"))
	return path
}

func startDirectGitTestServer(t *testing.T, repoParent string) *httptest.Server {
	t.Helper()
	execPath := gitExecPath(t)
	if execPath == "" {
		t.Skip("git --exec-path unavailable (no git-http-backend on PATH)")
	}

	mux := http.NewServeMux()
	mux.Handle("/git/", &cgi.Handler{
		Path: gitHTTPBackend(execPath),
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
