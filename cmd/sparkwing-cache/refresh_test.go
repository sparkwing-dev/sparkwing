package main

import (
	"encoding/json"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestHandleGitRefresh_RunsFetchOnCachedRepo simulates dispatch-time
// eager refresh: a registered repo with a local bare mirror exists,
// the operator just pushed a new commit, and the dispatcher POSTs
// /git/refresh?repo=... before creating a trigger. We verify the
// handler runs git fetch (by pointing origin at a throwaway upstream
// and confirming refs propagate), returns 200, and writes a JSON
// ack.
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

	// 1. Build an upstream "GitHub" with one commit on main.
	upstream := filepath.Join(tmp, "upstream.git")
	mustGit(t, "", "init", "--bare", upstream)
	work := filepath.Join(tmp, "work")
	mustGit(t, "", "clone", upstream, work)
	mustGit(t, work, "config", "user.email", "t@t")
	mustGit(t, work, "config", "user.name", "t")
	mustGit(t, work, "commit", "--allow-empty", "-m", "first")
	// rename default branch to main if needed
	mustGit(t, work, "branch", "-M", "main")
	mustGit(t, work, "push", "origin", "main")

	// 2. Pretend the cache has already cloned this upstream as a bare mirror.
	//    Use the real repoHash so handleGitRefresh's name->URL->hash chain
	//    resolves to the same path on disk.
	repoURL := upstream
	hash := repoHash(repoURL)
	bareRepo := filepath.Join(repoDir, hash+".git")
	mustGit(t, "", "clone", "--bare", upstream, bareRepo)

	// 3. Push a NEW commit to upstream that the bare mirror doesn't know
	//    about yet (mirrors the "operator pushed; cache hasn't fetched" race).
	mustGit(t, work, "commit", "--allow-empty", "-m", "post-push")
	mustGit(t, work, "push", "origin", "main")
	wantSHA := strings.TrimSpace(string(mustGitOut(t, work, "rev-parse", "HEAD")))

	// Sanity: the bare mirror should NOT contain wantSHA yet.
	if err := exec.Command("git", "-C", bareRepo, "cat-file", "-e", wantSHA).Run(); err == nil {
		t.Fatalf("setup wrong: bare mirror already had %s before refresh", wantSHA)
	}

	// 4. Hit the endpoint.
	req := httptest.NewRequest("POST", "/git/refresh?repo="+repoURL, nil)
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

	// 5. Verify the fetch actually landed: the bare mirror now knows wantSHA.
	if err := exec.Command("git", "-C", bareRepo, "cat-file", "-e", wantSHA).Run(); err != nil {
		t.Errorf("after /git/refresh, bare mirror still missing %s: %v", wantSHA, err)
	}
}

// TestHandleGitRefresh_MissingArgs guards the input contract: at least
// one of name/repo must be set.
func TestHandleGitRefresh_MissingArgs(t *testing.T) {
	req := httptest.NewRequest("POST", "/git/refresh", nil)
	w := httptest.NewRecorder()
	handleGitRefresh(w, req)
	if w.Code != 400 {
		t.Errorf("status: got %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

// TestHandleGitRefresh_UncachedRepo: 404 when the repo URL isn't
// already mirrored. The dispatcher tolerates this — first-time repos
// will be cloned on the regular /git/<name> path on first runner pull.
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

	req := httptest.NewRequest("POST", "/git/refresh?repo=git@github.com:never/cloned.git", nil)
	w := httptest.NewRecorder()
	handleGitRefresh(w, req)
	if w.Code != 404 {
		t.Errorf("status: got %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

// TestHandleGitRefresh_GETRejected pins the POST-only contract.
func TestHandleGitRefresh_GETRejected(t *testing.T) {
	req := httptest.NewRequest("GET", "/git/refresh?repo=foo", nil)
	w := httptest.NewRecorder()
	handleGitRefresh(w, req)
	if w.Code != 405 {
		t.Errorf("status: got %d, want 405", w.Code)
	}
}

// --- helpers ---

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
