package cache

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleGitRefresh_RunsFetchOnCachedRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	oldRoot, oldRepoDir := dataRoot, repoDir
	tmp := t.TempDir()
	dataRoot = tmp
	repoDir = filepath.Join(tmp, "repos")
	t.Cleanup(func() {
		dataRoot, repoDir = oldRoot, oldRepoDir
	})
	if err := exec.Command("mkdir", "-p", repoDir).Run(); err != nil {
		t.Fatal(err)
	}

	upstream := filepath.Join(tmp, "upstream.git")
	mustGit(t, "", "init", "--bare", upstream)
	work := filepath.Join(tmp, "work")
	mustGit(t, "", "clone", upstream, work)
	mustGit(t, work, "config", "user.email", "t@t")
	mustGit(t, work, "config", "user.name", "t")
	mustGit(t, work, "commit", "--allow-empty", "-m", "first")
	mustGit(t, work, "branch", "-M", "main")
	mustGit(t, work, "push", "origin", "main")

	repoURL := upstream
	hash := repoHash(repoURL)
	bareRepo := filepath.Join(repoDir, hash+".git")
	mustGit(t, "", "clone", "--bare", upstream, bareRepo)

	mustGit(t, work, "commit", "--allow-empty", "-m", "post-push")
	mustGit(t, work, "push", "origin", "main")
	wantSHA := strings.TrimSpace(string(mustGitOut(t, work, "rev-parse", "HEAD")))

	if err := exec.Command("git", "-C", bareRepo, "cat-file", "-e", wantSHA).Run(); err == nil {
		t.Fatalf("setup wrong: bare mirror already had %s before refresh", wantSHA)
	}

	req := httptest.NewRequest(http.MethodPost, "/git/refresh?repo="+repoURL, nil)
	w := httptest.NewRecorder()
	handleGitRefresh(w, req)

	if w.Code != 200 {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response body not JSON: %v (%s)", err, w.Body.String())
	}
	if ok, _ := resp["ok"].(bool); !ok {
		t.Errorf("response missing ok=true: %v", resp)
	}

	if err := exec.Command("git", "-C", bareRepo, "cat-file", "-e", wantSHA).Run(); err != nil {
		t.Errorf("after /git/refresh, bare mirror still missing %s: %v", wantSHA, err)
	}
}

func TestHandleGitRefresh_MissingArgs(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/git/refresh", nil)
	w := httptest.NewRecorder()
	handleGitRefresh(w, req)
	if w.Code != 400 {
		t.Errorf("status: got %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestHandleGitRefresh_UncachedRepo(t *testing.T) {
	oldRoot, oldRepoDir := dataRoot, repoDir
	tmp := t.TempDir()
	dataRoot = tmp
	repoDir = filepath.Join(tmp, "repos")
	t.Cleanup(func() {
		dataRoot, repoDir = oldRoot, oldRepoDir
	})
	if err := exec.Command("mkdir", "-p", repoDir).Run(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/git/refresh?repo=git@github.com:never/cloned.git", nil)
	w := httptest.NewRecorder()
	handleGitRefresh(w, req)
	if w.Code != 404 {
		t.Errorf("status: got %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

func TestHandleGitRefresh_GETRejected(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/git/refresh?repo=foo", nil)
	w := httptest.NewRecorder()
	handleGitRefresh(w, req)
	if w.Code != 405 {
		t.Errorf("status: got %d, want 405", w.Code)
	}
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func mustGitOut(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return out
}
