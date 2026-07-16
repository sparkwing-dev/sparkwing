package cache

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleHealth(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	handleHealthCombined(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestArtifactUpload(t *testing.T) {
	oldDir := artifactsDir
	artifactsDir = t.TempDir()
	defer func() { artifactsDir = oldDir }()

	body := strings.NewReader("test content")
	req := httptest.NewRequest(http.MethodPost, "/artifacts/job123?path=coverage/report.html", body)
	w := httptest.NewRecorder()
	handleArtifacts(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	data, err := os.ReadFile(filepath.Join(artifactsDir, "job123", "coverage", "report.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "test content" {
		t.Errorf("expected 'test content', got %s", data)
	}
}

func TestArtifactUpload_MissingPath(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/artifacts/job123", nil)
	w := httptest.NewRecorder()
	handleArtifacts(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 without path, got %d", w.Code)
	}
}

func TestArtifactUpload_DirectoryTraversal(t *testing.T) {
	oldDir := artifactsDir
	artifactsDir = t.TempDir()
	defer func() { artifactsDir = oldDir }()

	req := httptest.NewRequest(http.MethodPost, "/artifacts/job123?path=../../etc/passwd", strings.NewReader("evil"))
	w := httptest.NewRecorder()
	handleArtifacts(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 for traversal, got %d", w.Code)
	}
}

func TestArtifactList(t *testing.T) {
	oldDir := artifactsDir
	artifactsDir = t.TempDir()
	defer func() { artifactsDir = oldDir }()

	os.MkdirAll(filepath.Join(artifactsDir, "job123", "sub"), 0o755)
	os.WriteFile(filepath.Join(artifactsDir, "job123", "a.txt"), nil, 0o644)
	os.WriteFile(filepath.Join(artifactsDir, "job123", "sub", "b.txt"), nil, 0o644)

	req := httptest.NewRequest(http.MethodGet, "/artifacts/job123", nil)
	w := httptest.NewRecorder()
	handleArtifacts(w, req)

	var files []string
	json.NewDecoder(w.Body).Decode(&files)
	if len(files) != 2 {
		t.Errorf("expected 2 files, got %d: %v", len(files), files)
	}
}

func TestArtifactList_Empty(t *testing.T) {
	oldDir := artifactsDir
	artifactsDir = t.TempDir()
	defer func() { artifactsDir = oldDir }()

	req := httptest.NewRequest(http.MethodGet, "/artifacts/nonexistent", nil)
	w := httptest.NewRecorder()
	handleArtifacts(w, req)

	var files []string
	json.NewDecoder(w.Body).Decode(&files)
	if len(files) != 0 {
		t.Errorf("expected empty, got %v", files)
	}
}

func TestArtifactDownload_SingleFile(t *testing.T) {
	oldDir := artifactsDir
	artifactsDir = t.TempDir()
	defer func() { artifactsDir = oldDir }()

	os.MkdirAll(filepath.Join(artifactsDir, "job123"), 0o755)
	os.WriteFile(filepath.Join(artifactsDir, "job123", "report.html"), []byte("html content"), 0o644)

	req := httptest.NewRequest(http.MethodGet, "/artifacts/job123?glob=*.html", nil)
	w := httptest.NewRecorder()
	handleArtifacts(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	body, _ := io.ReadAll(w.Body)
	if !strings.Contains(string(body), "html content") {
		t.Errorf("expected html content, got %s", body)
	}
}

func TestArtifactDownload_NotFound(t *testing.T) {
	oldDir := artifactsDir
	artifactsDir = t.TempDir()
	defer func() { artifactsDir = oldDir }()

	os.MkdirAll(filepath.Join(artifactsDir, "job123"), 0o755)

	req := httptest.NewRequest(http.MethodGet, "/artifacts/job123?glob=*.xyz", nil)
	w := httptest.NewRecorder()
	handleArtifacts(w, req)

	if w.Code != 404 {
		t.Errorf("expected 404 for no matches, got %d", w.Code)
	}
}

func TestValidateGitRef(t *testing.T) {
	valid := []string{"main", "feature/foo", "v1.0.0", "release-2.3", "HEAD"}
	for _, ref := range valid {
		if err := validateGitRef(ref); err != nil {
			t.Errorf("expected %q to be valid, got: %v", ref, err)
		}
	}

	invalid := []string{"", "; rm -rf /", "main$(evil)", "branch name", "a..b", "--format=evil"}
	for _, ref := range invalid {
		if err := validateGitRef(ref); err == nil {
			t.Errorf("expected %q to be invalid", ref)
		}
	}
}

func TestArtifactUpload_AbsolutePath(t *testing.T) {
	oldDir := artifactsDir
	artifactsDir = t.TempDir()
	defer func() { artifactsDir = oldDir }()

	req := httptest.NewRequest(http.MethodPost, "/artifacts/job123?path=/etc/passwd", strings.NewReader("evil"))
	w := httptest.NewRecorder()
	handleArtifacts(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 for absolute path, got %d", w.Code)
	}
}

// TestResolveGitRepo_AutoClonesWhenMissing covers the split-brain
// recovery path: a name is in repoNames (config persisted) but the
// bare-repo dir is missing (disk was wiped or never cloned at
// registration time). resolveGitRepo should clone on demand from a
// reachable URL rather than returning "registered but not cloned"
// forever.
//
// The test uses a local upstream bare repo as the registered URL so
// no SSH / network is required.
func TestResolveGitRepo_AutoClonesWhenMissing(t *testing.T) {
	root := t.TempDir()

	upstream := filepath.Join(root, "upstream.git")
	if out, err := gitCmd("init", "--bare", upstream); err != nil {
		t.Fatalf("init upstream: %v (%s)", err, out)
	}

	oldRepoDir := repoDir
	oldNamesFile := namesFile
	repoDir = filepath.Join(root, "cache")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	namesFile = filepath.Join(root, "names.json")
	t.Cleanup(func() {
		repoDir = oldRepoDir
		namesFile = oldNamesFile
		repoNamesMu.Lock()
		delete(repoNames, "auto-clone-fixture")
		repoNamesMu.Unlock()
	})

	repoNamesMu.Lock()
	repoNames["auto-clone-fixture"] = upstream
	repoNamesMu.Unlock()

	bare, err := resolveGitRepo("auto-clone-fixture")
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if _, err := os.Stat(filepath.Join(bare, "HEAD")); err != nil {
		t.Fatalf("cloned bare missing HEAD: %v", err)
	}

	bare2, err := resolveGitRepo("auto-clone-fixture")
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if bare2 != bare {
		t.Fatalf("expected same bare path; got %q vs %q", bare2, bare)
	}
}

// TestResolveGitRepo_AutoCloneFailureKeepsSeedHint verifies that a
// failed auto-clone (bad URL / no network) still returns an error
// pointing at the /sync/seed recovery path, so the operator's
// playbook stays valid.
func TestResolveGitRepo_AutoCloneFailureKeepsSeedHint(t *testing.T) {
	root := t.TempDir()
	oldRepoDir := repoDir
	repoDir = filepath.Join(root, "cache")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		repoDir = oldRepoDir
		repoNamesMu.Lock()
		delete(repoNames, "bad-url-fixture")
		repoNamesMu.Unlock()
	})

	repoNamesMu.Lock()
	repoNames["bad-url-fixture"] = "/this/path/does/not/exist.git"
	repoNamesMu.Unlock()

	_, err := resolveGitRepo("bad-url-fixture")
	if err == nil {
		t.Fatal("expected error from auto-clone of bogus URL")
	}
	if !strings.Contains(err.Error(), "/sync/seed") {
		t.Fatalf("error should still point operators at /sync/seed; got %v", err)
	}
}

func TestSyncSeed_ImportsOnlyRequestedSeedRef(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	runGit(t, src, "init")
	runGit(t, src, "config", "user.email", "sparkwing@example.invalid")
	runGit(t, src, "config", "user.name", "Sparkwing Test")
	if err := os.WriteFile(filepath.Join(src, "wanted.txt"), []byte("wanted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, src, "add", "wanted.txt")
	runGit(t, src, "commit", "-m", "wanted")
	wanted := strings.TrimSpace(runGit(t, src, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(src, "private.txt"), []byte("private\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, src, "add", "private.txt")
	runGit(t, src, "commit", "-m", "private")
	private := strings.TrimSpace(runGit(t, src, "rev-parse", "HEAD"))
	runGit(t, src, "update-ref", "refs/sparkwing-seed/"+wanted, wanted)
	runGit(t, src, "update-ref", "refs/sparkwing-seed/"+private, private)
	bundle := filepath.Join(root, "seed.bundle")
	runGit(t, src, "bundle", "create", bundle, "refs/sparkwing-seed/"+wanted, "refs/sparkwing-seed/"+private)

	oldRepoDir := repoDir
	oldNamesFile := namesFile
	repoDir = filepath.Join(root, "cache")
	namesFile = filepath.Join(root, "names.json")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		repoDir = oldRepoDir
		namesFile = oldNamesFile
	})

	f, err := os.Open(bundle)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	req := httptest.NewRequest(http.MethodPost, "/sync/seed?repo=https://git.example.com/acme/widgets.git&sha="+wanted, f)
	w := httptest.NewRecorder()
	handleSyncSeed(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}

	bareRepo := filepath.Join(repoDir, repoHash("https://git.example.com/acme/widgets.git")+".git")
	runGit(t, bareRepo, "cat-file", "-e", wanted+"^{commit}")
	if out, err := exec.Command("git", "-C", bareRepo, "cat-file", "-e", private+"^{commit}").CombinedOutput(); err == nil {
		t.Fatalf("private commit was imported unexpectedly: %s", out)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if args[0] == "init" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestRepoHash_Deterministic(t *testing.T) {
	h1 := repoHash("git@github.com:user/repo.git")
	h2 := repoHash("git@github.com:user/repo.git")
	if h1 != h2 {
		t.Error("same URL should produce same hash")
	}
	if len(h1) != 12 {
		t.Errorf("expected 12 char hash, got %d", len(h1))
	}
}

func TestRepoHash_Different(t *testing.T) {
	h1 := repoHash("git@github.com:user/repo1.git")
	h2 := repoHash("git@github.com:user/repo2.git")
	if h1 == h2 {
		t.Error("different URLs should produce different hashes")
	}
}

func TestContains(t *testing.T) {
	s := []string{"a", "b", "c"}
	if !contains(s, "b") {
		t.Error("should contain b")
	}
	if contains(s, "d") {
		t.Error("should not contain d")
	}
}
