package cache

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

func TestSyncSeed_ImportsOnlyRequestedWorkspaceRef(t *testing.T) {
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

	seed := func(workspace bool) {
		f, err := os.Open(bundle)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		path := "/sync/seed?repo=https://git.example.com/acme/widgets.git&sha=" + wanted
		if workspace {
			path += "&workspace=1"
		}
		req := httptest.NewRequest(http.MethodPost, path, f)
		w := httptest.NewRecorder()
		handleSyncSeed(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("workspace=%v status = %d: %s", workspace, w.Code, w.Body.String())
		}
	}
	seed(false)
	seed(true)

	bareRepo := filepath.Join(repoDir, repoHash("https://git.example.com/acme/widgets.git")+".git")
	runGit(t, bareRepo, "cat-file", "-e", wanted+"^{commit}")
	workspaceRefs := strings.Fields(runGit(t, bareRepo, "for-each-ref", "--format=%(refname)", "refs/sparkwing-workspace/"))
	if len(workspaceRefs) != 1 || !strings.HasSuffix(workspaceRefs[0], "/"+wanted) {
		t.Fatalf("workspace refs = %v", workspaceRefs)
	}
	if out, err := exec.Command("git", "-C", bareRepo, "show-ref", "--verify", "refs/sparkwing-seed/"+wanted).CombinedOutput(); err != nil {
		t.Fatalf("ordinary seed ref was removed: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", bareRepo, "show-ref", "--verify", "refs/sparkwing-workspace-incoming/"+wanted).CombinedOutput(); err == nil {
		t.Fatalf("workspace import ref was retained: %s", out)
	}
	if out, err := exec.Command("git", "-C", bareRepo, "cat-file", "-e", private+"^{commit}").CombinedOutput(); err == nil {
		t.Fatalf("private commit was imported unexpectedly: %s", out)
	}
}

func TestSyncSeed_PrunesRejectedWorkspaceObject(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	runGit(t, source, "init")
	if err := os.WriteFile(filepath.Join(source, "blob"), []byte("not a commit"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha := strings.TrimSpace(runGit(t, source, "hash-object", "-w", "blob"))
	ref := "refs/sparkwing-seed/" + sha
	runGit(t, source, "update-ref", ref, sha)
	bundle := filepath.Join(root, "blob.bundle")
	runGit(t, source, "bundle", "create", bundle, ref)

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
	request := httptest.NewRequest(http.MethodPost,
		"/sync/seed?workspace=1&repo=https://git.example.com/acme/widgets.git&sha="+sha, f)
	response := httptest.NewRecorder()
	handleSyncSeed(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", response.Code, response.Body.String())
	}
	bareRepo := filepath.Join(repoDir, repoHash("https://git.example.com/acme/widgets.git")+".git")
	if out, err := exec.Command("git", "-C", bareRepo, "cat-file", "-e", sha).CombinedOutput(); err == nil {
		t.Fatalf("rejected workspace object survived cleanup: %s", out)
	}
}

func TestRetainWorkspaceSeedRejectsNewSnapshotAtCapacity(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo.git")
	runGit(t, repo, "init", "--bare")
	source := filepath.Join(t.TempDir(), "source")
	runGit(t, source, "init")
	runGit(t, source, "config", "user.email", "sparkwing@example.invalid")
	runGit(t, source, "config", "user.name", "Sparkwing Test")
	var shas []string
	for i := range 3 {
		if err := os.WriteFile(filepath.Join(source, "value"), []byte{byte('0' + i)}, 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, source, "add", "value")
		runGit(t, source, "commit", "-m", string(rune('a'+i)))
		sha := strings.TrimSpace(runGit(t, source, "rev-parse", "HEAD"))
		shas = append(shas, sha)
		runGit(t, repo, "fetch", source, sha)
		seedRef := "refs/sparkwing-workspace-incoming/" + sha
		if i == 2 {
			runGit(t, repo, "update-ref", "refs/sparkwing-seed/"+sha, sha)
		}
		runGit(t, repo, "update-ref", seedRef, sha)
		err := retainWorkspaceSeed(repo, seedRef, sha, 2)
		if i < 2 && err != nil {
			t.Fatal(err)
		}
		if i == 2 && (err == nil || !strings.Contains(err.Error(), "limit 2")) {
			t.Fatalf("third retain error = %v, want capacity rejection", err)
		}
	}
	refs := strings.Fields(runGit(t, repo, "for-each-ref", "--format=%(refname)", "refs/sparkwing-workspace/"))
	if len(refs) != 2 {
		t.Fatalf("workspace refs = %v, want cap 2", refs)
	}
	for _, sha := range shas[:2] {
		if !slices.ContainsFunc(refs, func(ref string) bool { return strings.HasSuffix(ref, "/"+sha) }) {
			t.Fatalf("admitted snapshot %s was evicted: %v", sha, refs)
		}
	}
	for _, sha := range shas {
		if out, err := exec.Command("git", "-C", repo, "show-ref", "--verify", "refs/sparkwing-workspace-incoming/"+sha).CombinedOutput(); err == nil {
			t.Fatalf("workspace import ref %s retained: %s", sha, out)
		}
	}
	if out, err := exec.Command("git", "-C", repo, "show-ref", "--verify", "refs/sparkwing-seed/"+shas[2]).CombinedOutput(); err != nil {
		t.Fatalf("ordinary seed ref was removed on workspace rejection: %v: %s", err, out)
	}
	runGit(t, repo, "update-ref", "-d", "refs/sparkwing-seed/"+shas[2])
	pruneUnreachableSeedObjects(repo)
	if out, err := exec.Command("git", "-C", repo, "cat-file", "-e", shas[2]+"^{commit}").CombinedOutput(); err == nil {
		t.Fatalf("rejected workspace object remained after its ordinary ref was removed: %s", out)
	}
}

func TestRetainWorkspaceSeedRefreshesOneRefPerSnapshot(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo.git")
	runGit(t, repo, "init", "--bare")
	source := filepath.Join(t.TempDir(), "source")
	runGit(t, source, "init")
	runGit(t, source, "config", "user.email", "sparkwing@example.invalid")
	runGit(t, source, "config", "user.name", "Sparkwing Test")
	if err := os.WriteFile(filepath.Join(source, "value"), []byte("same"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "value")
	runGit(t, source, "commit", "-m", "same")
	sha := strings.TrimSpace(runGit(t, source, "rev-parse", "HEAD"))
	runGit(t, repo, "fetch", source, sha)
	seedRef := "refs/sparkwing-workspace-incoming/" + sha
	runGit(t, repo, "update-ref", "refs/sparkwing-seed/"+sha, sha)
	for range 3 {
		runGit(t, repo, "update-ref", seedRef, sha)
		if err := retainWorkspaceSeed(repo, seedRef, sha, 2); err != nil {
			t.Fatal(err)
		}
	}
	refs := strings.Fields(runGit(t, repo, "for-each-ref", "--format=%(refname)", "refs/sparkwing-workspace/"))
	if len(refs) != 1 || !strings.HasSuffix(refs[0], "/"+sha) {
		t.Fatalf("workspace refs = %v, want one refreshed ref", refs)
	}
	if out, err := exec.Command("git", "-C", repo, "show-ref", "--verify", "refs/sparkwing-seed/"+sha).CombinedOutput(); err != nil {
		t.Fatalf("ordinary seed ref was removed on workspace success: %v: %s", err, out)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if args[0] == "init" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("git", append([]string{"-c", "commit.gpgSign=false"}, args...)...)
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

func TestRequireToken(t *testing.T) {
	old := apiToken
	apiToken = "s3cret"
	defer func() { apiToken = old }()

	for _, tc := range []struct {
		name      string
		authz     string
		forwarded string
		want      int
	}{
		{name: "correct bearer", authz: "Bearer s3cret", want: http.StatusOK},
		{name: "wrong bearer", authz: "Bearer nope", want: http.StatusUnauthorized},
		{name: "no header", want: http.StatusUnauthorized},
		{name: "no header, forwarded", forwarded: "203.0.113.7", want: http.StatusUnauthorized},
		{name: "wrong bearer, forwarded", authz: "Bearer nope", forwarded: "203.0.113.7", want: http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			served := false
			h := requireToken(func(w http.ResponseWriter, _ *http.Request) {
				served = true
				w.WriteHeader(http.StatusOK)
			})
			req := httptest.NewRequest(http.MethodPut, "/bin/abc", nil)
			if tc.authz != "" {
				req.Header.Set("Authorization", tc.authz)
			}
			if tc.forwarded != "" {
				req.Header.Set("X-Forwarded-For", tc.forwarded)
			}
			w := httptest.NewRecorder()
			h(w, req)

			if w.Code != tc.want {
				t.Errorf("status = %d, want %d", w.Code, tc.want)
			}
			if served != (tc.want == http.StatusOK) {
				t.Errorf("handler served = %t, want %t", served, tc.want == http.StatusOK)
			}
		})
	}
}

func TestRequireTokenServesEveryoneWhenUnauthenticated(t *testing.T) {
	old := apiToken
	apiToken = ""
	defer func() { apiToken = old }()

	served := false
	h := requireToken(func(w http.ResponseWriter, _ *http.Request) {
		served = true
		w.WriteHeader(http.StatusOK)
	})
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodPut, "/bin/abc", nil))

	if !served || w.Code != http.StatusOK {
		t.Errorf("served = %t, status = %d, want true and 200", served, w.Code)
	}
}

func TestNewRejectsEmptyAPIToken(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()

	if _, err := New(cfg); err == nil {
		t.Fatal("New accepted an empty API token")
	} else if !strings.Contains(err.Error(), "--allow-unauthenticated") {
		t.Errorf("error %q does not name the opt-in flag", err)
	}
}
