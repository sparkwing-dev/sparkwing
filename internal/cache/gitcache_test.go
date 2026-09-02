package cache

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
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

func TestArtifactUpload_LogEscapesTheCallerPath(t *testing.T) {
	oldDir := artifactsDir
	artifactsDir = t.TempDir()
	defer func() { artifactsDir = oldDir }()

	var logged bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&logged)
	defer log.SetOutput(oldWriter)

	req := httptest.NewRequest(http.MethodPost, "/artifacts/job123?path=a%0Afake.txt", strings.NewReader("x"))
	w := httptest.NewRecorder()
	handleArtifacts(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(logged.String(), `a\nfake.txt`) {
		t.Errorf("log line did not escape the newline: %q", logged.String())
	}
}

func TestArtifactDownload_OptionLikeNameIsNotATarFlag(t *testing.T) {
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar is not on PATH")
	}
	oldDir := artifactsDir
	artifactsDir = t.TempDir()
	defer func() { artifactsDir = oldDir }()

	job := filepath.Join(artifactsDir, "job123")
	if err := os.MkdirAll(job, 0o755); err != nil {
		t.Fatal(err)
	}
	names := []string{"--sparkwing-not-an-option", "report.txt"}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(job, name), []byte("content"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/artifacts/job123?glob=*", nil)
	w := httptest.NewRecorder()
	handleArtifacts(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var got []string
	tr := tar.NewReader(w.Body)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read tar stream: %v", err)
		}
		got = append(got, hdr.Name)
	}
	for _, name := range names {
		if !slices.Contains(got, name) {
			t.Errorf("archive %v is missing %q", got, name)
		}
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

	invalid := []string{
		"", "; rm -rf /", "main$(evil)", "branch name", "a..b", "--format=evil",
		"--show-toplevel", "--all", "--git-dir", "-x",
	}
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

func TestArtifactUpload_AtPrefixedPath(t *testing.T) {
	oldDir := artifactsDir
	artifactsDir = t.TempDir()
	defer func() { artifactsDir = oldDir }()

	for _, p := range []string{"@inline.tar", "sub/@inline.tar"} {
		req := httptest.NewRequest(http.MethodPost, "/artifacts/job123?path="+p, strings.NewReader("evil"))
		w := httptest.NewRecorder()
		handleArtifacts(w, req)

		if w.Code != 400 {
			t.Errorf("expected 400 for %q, got %d", p, w.Code)
		}
	}
}

func TestUploadDownload_PercentEncodedDotDotIsNoExistenceOracle(t *testing.T) {
	oldDir := uploadsDir
	uploadsDir = t.TempDir()
	defer func() { uploadsDir = oldDir }()

	present := filepath.Join(filepath.Dir(uploadsDir), "present.tar.gz")
	if err := os.WriteFile(present, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(present) })

	for _, name := range []string{"present", "absent"} {
		req := httptest.NewRequest(http.MethodGet, "/uploads/%2e%2e/"+name+".tar.gz", nil)
		w := httptest.NewRecorder()
		handleUploadDownload(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d (%s)", name, w.Code, strings.TrimSpace(w.Body.String()))
		}
		if !strings.Contains(w.Body.String(), "invalid upload ID") {
			t.Errorf("%s: body %q does not name the rejected id", name, strings.TrimSpace(w.Body.String()))
		}
	}
}

func TestGitObjectRE_AcceptsOnlyAnObjectID(t *testing.T) {
	valid := []string{strings.Repeat("a", 40), strings.Repeat("0", 64), strings.Repeat("F", 40)}
	for _, sha := range valid {
		if !gitObjectRE.MatchString(sha) {
			t.Errorf("expected %q to be a git object id", sha)
		}
	}

	invalid := []string{"", ".", "..", "/data/repos", "../../etc/passwd", "HEAD", strings.Repeat("z", 40)}
	for _, sha := range invalid {
		if gitObjectRE.MatchString(sha) {
			t.Errorf("expected %q not to be a git object id", sha)
		}
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
		err := retainWorkspaceSeed(repo, seedRef, sha, 2, 24*time.Hour)
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
		if err := retainWorkspaceSeed(repo, seedRef, sha, 2, 24*time.Hour); err != nil {
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

func TestHandleBinDigest(t *testing.T) {
	oldDir := binsDir
	binsDir = t.TempDir()
	defer func() { binsDir = oldDir }()

	const hash = "deadbeef-cafebabe"
	body := []byte("compiled pipeline bytes")
	sum := sha256.Sum256(body)
	wantDigest := "sha-256=" + base64.StdEncoding.EncodeToString(sum[:])

	put := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/bin/"+hash, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer writer-token")
	handleBin(put, req)
	if put.Code != http.StatusCreated {
		t.Fatalf("PUT status = %d: %s", put.Code, put.Body.String())
	}
	if got := put.Header().Get("Digest"); got != wantDigest {
		t.Errorf("PUT Digest = %q, want %q", got, wantDigest)
	}

	meta, err := readBinMeta(hash)
	if err != nil {
		t.Fatalf("readBinMeta: %v", err)
	}
	if meta.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("stored digest = %q, want %q", meta.SHA256, hex.EncodeToString(sum[:]))
	}
	if !strings.HasPrefix(meta.Principal, "token:") {
		t.Errorf("principal = %q, want a token fingerprint", meta.Principal)
	}
	if strings.Contains(meta.Principal, "writer-token") {
		t.Errorf("principal %q leaks the bearer", meta.Principal)
	}

	get := httptest.NewRecorder()
	handleBin(get, httptest.NewRequest(http.MethodGet, "/bin/"+hash, nil))
	if get.Code != http.StatusOK {
		t.Fatalf("GET status = %d", get.Code)
	}
	if got := get.Header().Get("Digest"); got != wantDigest {
		t.Errorf("GET Digest = %q, want %q", got, wantDigest)
	}
	if got := get.Header().Get("ETag"); got != `"`+hex.EncodeToString(sum[:])+`"` {
		t.Errorf("GET ETag = %q", got)
	}
	if !bytes.Equal(get.Body.Bytes(), body) {
		t.Errorf("GET body = %q, want %q", get.Body.Bytes(), body)
	}
}

func TestHandleBinDigestForUnattestedBlob(t *testing.T) {
	oldDir := binsDir
	binsDir = t.TempDir()
	defer func() { binsDir = oldDir }()

	const hash = "deadbeef-cafebabe"
	body := []byte("blob written before digests were recorded")
	if err := os.WriteFile(filepath.Join(binsDir, hash), body, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)

	w := httptest.NewRecorder()
	handleBin(w, httptest.NewRequest(http.MethodGet, "/bin/"+hash, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET status = %d", w.Code)
	}
	if got := w.Header().Get("Digest"); got != "sha-256="+base64.StdEncoding.EncodeToString(sum[:]) {
		t.Errorf("GET Digest = %q", got)
	}
	if !bytes.Equal(w.Body.Bytes(), body) {
		t.Errorf("GET body = %q, want %q", w.Body.Bytes(), body)
	}
	meta, err := readBinMeta(hash)
	if err != nil {
		t.Fatalf("readBinMeta: %v", err)
	}
	if meta.Principal != "unknown" {
		t.Errorf("principal = %q, want unknown", meta.Principal)
	}
}

func TestRequireTokenBlocksAnUnauthenticatedBinPut(t *testing.T) {
	oldDir, oldToken := binsDir, apiToken
	binsDir, apiToken = t.TempDir(), "cache-token"
	defer func() { binsDir, apiToken = oldDir, oldToken }()

	const hash = "deadbeef-cafebabe"
	w := httptest.NewRecorder()
	requireToken(handleBin)(w, httptest.NewRequest(http.MethodPut, "/bin/"+hash, strings.NewReader("poisoned")))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if _, err := os.Stat(filepath.Join(binsDir, hash)); !os.IsNotExist(err) {
		t.Fatalf("unauthorized PUT stored a blob: %v", err)
	}
}

func TestHandleBinClientRejectsPoisonedBlob(t *testing.T) {
	oldDir := binsDir
	binsDir = t.TempDir()
	defer func() { binsDir = oldDir }()

	const hash = "deadbeef-cafebabe"
	srv := httptest.NewServer(http.HandlerFunc(handleBin))
	defer srv.Close()

	src := filepath.Join(t.TempDir(), "pipeline")
	if err := os.WriteFile(src, []byte("compiled pipeline bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := bincache.UploadBinary(srv.URL, "writer-token", hash, src); err != nil {
		t.Fatalf("UploadBinary: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "pipeline")
	if err := bincache.TryBinary(srv.URL, "writer-token", hash, dest); err != nil {
		t.Fatalf("TryBinary: %v", err)
	}

	if err := os.WriteFile(filepath.Join(binsDir, hash), []byte("poisoned bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	poisoned := filepath.Join(t.TempDir(), "pipeline")
	if err := bincache.TryBinary(srv.URL, "writer-token", hash, poisoned); !errors.Is(err, bincache.ErrDigest) {
		t.Fatalf("err = %v, want ErrDigest", err)
	}
	if _, err := os.Stat(poisoned); !os.IsNotExist(err) {
		t.Fatalf("poisoned binary was installed: %v", err)
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
		{name: "lowercase scheme", authz: "bearer s3cret", want: http.StatusOK},
		{name: "padded scheme", authz: "Bearer  s3cret", want: http.StatusOK},
		{name: "no scheme", authz: "s3cret", want: http.StatusUnauthorized},
		{name: "other scheme", authz: "Basic s3cret", want: http.StatusUnauthorized},
		{name: "empty credential", authz: "Bearer ", want: http.StatusUnauthorized},
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

func newTestServer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	saved := struct {
		dataRoot, repoDir, archDir, artifactsDir, binsDir, cacheDir string
		uploadsDir, namesFile, proxyDir, sshKeyDir, apiToken        string
	}{
		dataRoot, repoDir, archDir, artifactsDir, binsDir, cacheDir,
		uploadsDir, namesFile, proxyDir, sshKeyDir, apiToken,
	}
	t.Cleanup(func() {
		dataRoot, repoDir, archDir, artifactsDir, binsDir, cacheDir = saved.dataRoot, saved.repoDir, saved.archDir, saved.artifactsDir, saved.binsDir, saved.cacheDir
		uploadsDir, namesFile, proxyDir, sshKeyDir, apiToken = saved.uploadsDir, saved.namesFile, saved.proxyDir, saved.sshKeyDir, saved.apiToken
	})

	root := t.TempDir()
	cfg := DefaultConfig()
	cfg.DataDir = root
	cfg.ProxyDir = filepath.Join(root, "proxy")
	cfg.SSHKeyDir = filepath.Join(root, "no-ssh-key")
	cfg.APIToken = token
	cfg.AllowUnauthenticated = token == ""
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestMuxGuardsEveryWriteRoute(t *testing.T) {
	srv := newTestServer(t, "s3cret")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"name":"left-pad"}`))
	}))
	defer upstream.Close()

	cases := []struct {
		method, path string
		guarded      bool
	}{
		{method: http.MethodGet, path: "/bin/deadbeef-cafebabe", guarded: true},
		{method: http.MethodPut, path: "/bin/deadbeef-cafebabe", guarded: true},
		{method: http.MethodPut, path: "/cache/lint", guarded: true},
		{method: http.MethodPost, path: "/upload", guarded: true},
		{method: http.MethodGet, path: "/uploads/abc", guarded: true},
		{method: http.MethodPost, path: "/sync/negotiate", guarded: true},
		{method: http.MethodPost, path: "/sync/seed", guarded: true},
		{method: http.MethodPost, path: "/artifacts/job1?path=out.txt", guarded: true},
		{method: http.MethodGet, path: "/artifacts/job1", guarded: true},
		{method: http.MethodGet, path: "/archive?repo=x&branch=main", guarded: true},
		{method: http.MethodGet, path: "/repos", guarded: true},
		{method: http.MethodGet, path: "/file?repo=x&branch=main&path=go.mod", guarded: true},
		{method: http.MethodGet, path: "/tree-hash?repo=x&branch=main&path=.", guarded: true},
		{method: http.MethodGet, path: "/branch-contains?repo=x&branch=main&commit=abc", guarded: true},
		{method: http.MethodPost, path: "/git/register?name=app&repo=https://example.com/a.git", guarded: true},
		{method: http.MethodPost, path: "/git/refresh?name=app", guarded: true},
		{method: http.MethodGet, path: "/git/app/info/refs?service=git-upload-pack", guarded: true},
		{method: http.MethodGet, path: "/health", guarded: false},
		{method: http.MethodGet, path: "/stats", guarded: false},
		{method: http.MethodGet, path: "/metrics", guarded: false},
		{method: http.MethodGet, path: "/proxy/npm/left-pad", guarded: false},
	}

	withTestProxy(t, map[string]Registry{
		"npm": {Name: "npm", Upstream: upstream.URL},
	}, func() {
		for _, tc := range cases {
			t.Run(tc.method+" "+tc.path, func(t *testing.T) {
				req, err := http.NewRequest(tc.method, srv.URL+tc.path, strings.NewReader(""))
				if err != nil {
					t.Fatal(err)
				}
				resp, err := srv.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				if got := resp.StatusCode == http.StatusUnauthorized; got != tc.guarded {
					t.Errorf("status = %d, guarded = %t, want guarded = %t", resp.StatusCode, got, tc.guarded)
				}
			})
		}
	})
}

func TestArtifactUploadRejectsAJobIDThatEscapesTheRoot(t *testing.T) {
	srv := newTestServer(t, "s3cret")

	const key = "deadbeef-cafebabe"
	const escape = "/artifacts/..%2f..%2fx?path=bins/" + key
	body := "#!/bin/sh\nid\n"

	anon, err := http.NewRequest(http.MethodPost, srv.URL+escape, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(anon)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous traversal upload = %d, want 401", resp.StatusCode)
	}

	authed, err := http.NewRequest(http.MethodPost, srv.URL+escape, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	authed.Header.Set("Authorization", "Bearer s3cret")
	resp, err = srv.Client().Do(authed)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("authenticated traversal upload = %d, want 400", resp.StatusCode)
	}

	if _, err := os.Stat(filepath.Join(binsDir, key)); !os.IsNotExist(err) {
		t.Fatalf("artifact upload wrote into the binary cache: %v", err)
	}
	entries, err := os.ReadDir(binsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("bins dir = %v, want empty", entries)
	}
}

func TestArtifactUploadKeepsAValidJobInsideItsDirectory(t *testing.T) {
	srv := newTestServer(t, "s3cret")

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/artifacts/job-1?path=out/report.txt", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer s3cret")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload = %d, want 200", resp.StatusCode)
	}
	got, err := os.ReadFile(filepath.Join(artifactsDir, "job-1", "out", "report.txt"))
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("artifact = %q, want %q", got, "hello")
	}
}

func TestNewRejectsAWhitespaceOnlyAPIToken(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.APIToken = " \n"

	if _, err := New(cfg); err == nil {
		t.Fatal("New accepted a whitespace-only API token")
	} else if !strings.Contains(err.Error(), "--allow-unauthenticated") {
		t.Errorf("error %q does not name the opt-in flag", err)
	}
}

func TestHandleBinFailedPutLeavesNoSidecar(t *testing.T) {
	oldDir := binsDir
	binsDir = t.TempDir()
	defer func() { binsDir = oldDir }()

	const hash = "deadbeef-cafebabe"
	if err := os.MkdirAll(filepath.Join(binsDir, hash, "occupied"), 0o755); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	handleBin(w, httptest.NewRequest(http.MethodPut, "/bin/"+hash, strings.NewReader("compiled pipeline bytes")))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("PUT status = %d, want 500", w.Code)
	}
	if _, err := os.Stat(binMetaPath(hash)); !os.IsNotExist(err) {
		t.Fatalf("failed PUT left a sidecar attesting bytes that were never stored: %v", err)
	}
	entries, err := os.ReadDir(binsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("failed PUT left a staged blob %s", e.Name())
		}
	}
}

func TestHandleBinLegacyGetRacingAPutKeepsTheSidecarHonest(t *testing.T) {
	for i := 0; i < 25; i++ {
		oldDir := binsDir
		binsDir = t.TempDir()

		const hash = "deadbeef-cafebabe"
		legacy := bytes.Repeat([]byte("legacy blob "), 1<<18)
		if err := os.WriteFile(filepath.Join(binsDir, hash), legacy, 0o755); err != nil {
			t.Fatal(err)
		}
		fresh := bytes.Repeat([]byte("fresh blob "), 1<<18)

		var wg sync.WaitGroup
		get := httptest.NewRecorder()
		wg.Add(2)
		go func() {
			defer wg.Done()
			handleBin(get, httptest.NewRequest(http.MethodGet, "/bin/"+hash, nil))
		}()
		go func() {
			defer wg.Done()
			put := httptest.NewRecorder()
			handleBin(put, httptest.NewRequest(http.MethodPut, "/bin/"+hash, bytes.NewReader(fresh)))
		}()
		wg.Wait()

		blob, err := os.ReadFile(filepath.Join(binsDir, hash))
		if err != nil {
			t.Fatal(err)
		}
		onDisk := sha256.Sum256(blob)
		meta, err := readBinMeta(hash)
		if err != nil {
			t.Fatalf("readBinMeta: %v", err)
		}
		if meta.SHA256 != hex.EncodeToString(onDisk[:]) {
			t.Fatalf("iteration %d: sidecar %s attests neither blob on disk (%s)", i, meta.SHA256, hex.EncodeToString(onDisk[:]))
		}
		if get.Code == http.StatusOK {
			served := sha256.Sum256(get.Body.Bytes())
			want := "sha-256=" + base64.StdEncoding.EncodeToString(served[:])
			if got := get.Header().Get("Digest"); got != want {
				t.Fatalf("iteration %d: served Digest = %q, body hashes to %q", i, got, want)
			}
		}
		binsDir = oldDir
	}
}

func TestEveryResponseCarriesNosniff(t *testing.T) {
	srv := newTestServer(t, "s3cret")

	for _, path := range []string{"/health", "/repos", "/metrics"} {
		resp, err := srv.Client().Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s X-Content-Type-Options = %q, want nosniff", path, got)
		}
	}
}

func TestArtifactDownloadServesAnAttachment(t *testing.T) {
	srv := newTestServer(t, "s3cret")

	upload, err := http.NewRequest(http.MethodPost, srv.URL+"/artifacts/job-1?path=report.html",
		strings.NewReader("<script>alert(1)</script>"))
	if err != nil {
		t.Fatal(err)
	}
	upload.Header.Set("Authorization", "Bearer s3cret")
	resp, err := srv.Client().Do(upload)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload = %d, want 200", resp.StatusCode)
	}

	download, err := http.NewRequest(http.MethodGet, srv.URL+"/artifacts/job-1?glob=report.html", nil)
	if err != nil {
		t.Fatal(err)
	}
	download.Header.Set("Authorization", "Bearer s3cret")
	resp, err = srv.Client().Do(download)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", got)
	}
	if got := resp.Header.Get("Content-Disposition"); got != `attachment; filename="report.html"` {
		t.Errorf("Content-Disposition = %q, want an attachment", got)
	}
}

func TestGitRegisterValidatesTheName(t *testing.T) {
	srv := newTestServer(t, "s3cret")

	for _, name := range []string{"../escape", "a/b", strings.Repeat("n", 65), "na me", ""} {
		req, err := http.NewRequest(http.MethodPost,
			srv.URL+"/git/register?repo=https%3A%2F%2Fexample.invalid%2Fa.git&name="+url.QueryEscape(name), nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer s3cret")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("register name %q = %d, want 400", name, resp.StatusCode)
		}
	}
}

func TestGitRegisterAcceptsClaimScopedRepoName(t *testing.T) {
	srv := newTestServer(t, "s3cret")
	repoURL := "https://example.invalid/acme/widgets.git"
	name := bincache.ClaimedRepoNameFromURL(repoURL)
	if code := registerName(t, srv, name, repoURL, "s3cret"); code != http.StatusOK {
		t.Fatalf("register claim-scoped name length %d = %d, want 200", len(name), code)
	}
}

func isolateRepoNames(t *testing.T) {
	t.Helper()
	repoNamesMu.Lock()
	saved := repoNames
	repoNames = map[string]string{}
	repoNamesMu.Unlock()
	t.Cleanup(func() {
		repoNamesMu.Lock()
		repoNames = saved
		repoNamesMu.Unlock()
	})
}

func registerName(t *testing.T, srv *httptest.Server, name, repo, token string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost,
		srv.URL+"/git/register?name="+url.QueryEscape(name)+"&repo="+url.QueryEscape(repo), nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func registeredRepo(name string) string {
	repoNamesMu.RLock()
	defer repoNamesMu.RUnlock()
	return repoNames[name]
}

func TestGitRegisterRefusesAnUnauthenticatedRepoint(t *testing.T) {
	srv := newTestServer(t, "s3cret")
	isolateRepoNames(t)

	if code := registerName(t, srv, "app", "https://example.invalid/first.git", "s3cret"); code != http.StatusOK {
		t.Fatalf("first registration = %d, want 200", code)
	}
	if code := registerName(t, srv, "app", "https://example.invalid/second.git", ""); code != http.StatusUnauthorized {
		t.Errorf("unauthenticated repoint = %d, want 401", code)
	}
	if got := registeredRepo("app"); got != "https://example.invalid/first.git" {
		t.Errorf("registered repo = %q, want the original", got)
	}
	if code := registerName(t, srv, "app", "https://example.invalid/second.git", "s3cret"); code != http.StatusOK {
		t.Errorf("authenticated repoint = %d, want 200", code)
	}
	if got := registeredRepo("app"); got != "https://example.invalid/second.git" {
		t.Errorf("registered repo = %q, want the repointed one", got)
	}
}

func TestGitRegisterAllowsARepointOnAnOpenCache(t *testing.T) {
	srv := newTestServer(t, "")
	isolateRepoNames(t)

	if code := registerName(t, srv, "app", "https://example.invalid/first.git", ""); code != http.StatusOK {
		t.Fatalf("first registration = %d, want 200", code)
	}
	if code := registerName(t, srv, "app", "https://example.invalid/second.git", ""); code != http.StatusOK {
		t.Errorf("repoint on an open cache = %d, want 200", code)
	}
	if got := registeredRepo("app"); got != "https://example.invalid/second.git" {
		t.Errorf("registered repo = %q, want the repointed one", got)
	}
}

func TestAutoRegisterSkipsAnInvalidName(t *testing.T) {
	newTestServer(t, "s3cret")
	isolateRepoNames(t)

	saved := autoRegisterReposSpec
	autoRegisterReposSpec = "a/../b=https://example.invalid/a.git,ok=https://example.invalid/b.git"
	t.Cleanup(func() { autoRegisterReposSpec = saved })

	autoRegisterRepos()

	if got := registeredRepo("a/../b"); got != "" {
		t.Errorf("auto-register accepted an invalid name: %q", got)
	}
	if got := registeredRepo("ok"); got != "https://example.invalid/b.git" {
		t.Errorf("auto-register dropped a valid entry: %q", got)
	}
}

func TestWorkspaceRefExpired(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	ref := func(age time.Duration) string {
		return fmt.Sprintf("%s%020d/%s", workspaceRefPrefix, now.Add(-age).UnixNano(), strings.Repeat("a", 40))
	}
	for _, tc := range []struct {
		name    string
		ref     string
		maxAge  time.Duration
		expired bool
	}{
		{name: "older than the window", ref: ref(48 * time.Hour), maxAge: 24 * time.Hour, expired: true},
		{name: "inside the window", ref: ref(time.Hour), maxAge: 24 * time.Hour},
		{name: "expiry disabled", ref: ref(48 * time.Hour), maxAge: 0},
		{name: "unparsable stamp", ref: workspaceRefPrefix + "not-a-stamp/abc", maxAge: 24 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := workspaceRefExpired(tc.ref, workspaceRefPrefix, now, tc.maxAge); got != tc.expired {
				t.Errorf("workspaceRefExpired = %t, want %t", got, tc.expired)
			}
		})
	}
}

func TestRetainWorkspaceSeedExpiresRefsPastTheMaxAge(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo.git")
	runGit(t, repo, "init", "--bare")
	source := filepath.Join(t.TempDir(), "source")
	runGit(t, source, "init")
	runGit(t, source, "config", "user.email", "sparkwing@example.invalid")
	runGit(t, source, "config", "user.name", "Sparkwing Test")
	runGit(t, source, "commit", "--allow-empty", "-m", "snapshot")
	sha := strings.TrimSpace(runGit(t, source, "rev-parse", "HEAD"))
	runGit(t, repo, "fetch", source, sha)

	stale := fmt.Sprintf("%s%020d/%s", workspaceRefPrefix, time.Now().Add(-72*time.Hour).UnixNano(), strings.Repeat("b", 40))
	runGit(t, repo, "update-ref", stale, sha)
	seedRef := "refs/sparkwing-workspace-incoming/" + sha
	runGit(t, repo, "update-ref", seedRef, sha)

	if err := retainWorkspaceSeed(repo, seedRef, sha, 1, 24*time.Hour); err != nil {
		t.Fatalf("retainWorkspaceSeed: %v", err)
	}

	refs := strings.Fields(runGit(t, repo, "for-each-ref", "--format=%(refname)", workspaceRefPrefix))
	if len(refs) != 1 {
		t.Fatalf("retained refs = %v, want only the new snapshot", refs)
	}
	if refs[0] == stale {
		t.Errorf("retained the expired ref %s", stale)
	}
}

func TestRetainWorkspaceSeedArchivesExpiredRefsSoARetryStillFindsTheSnapshot(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo.git")
	runGit(t, repo, "init", "--bare")
	source := filepath.Join(t.TempDir(), "source")
	runGit(t, source, "init")
	runGit(t, source, "config", "user.email", "sparkwing@example.invalid")
	runGit(t, source, "config", "user.name", "Sparkwing Test")
	runGit(t, source, "commit", "--allow-empty", "-m", "old snapshot")
	old := strings.TrimSpace(runGit(t, source, "rev-parse", "HEAD"))
	runGit(t, source, "commit", "--allow-empty", "-m", "new snapshot")
	fresh := strings.TrimSpace(runGit(t, source, "rev-parse", "HEAD"))
	runGit(t, repo, "fetch", source, old, fresh)

	stale := fmt.Sprintf("%s%020d/%s", workspaceRefPrefix, time.Now().Add(-72*time.Hour).UnixNano(), old)
	runGit(t, repo, "update-ref", stale, old)
	seedRef := "refs/sparkwing-workspace-incoming/" + fresh
	runGit(t, repo, "update-ref", seedRef, fresh)

	if err := retainWorkspaceSeed(repo, seedRef, fresh, 128, 24*time.Hour); err != nil {
		t.Fatalf("retainWorkspaceSeed: %v", err)
	}
	pruneUnreachableSeedObjects(repo)

	archived := strings.Fields(runGit(t, repo, "for-each-ref", "--format=%(refname)", workspaceArchiveRefPrefix))
	if len(archived) != 1 || !strings.HasSuffix(archived[0], "/"+old) {
		t.Fatalf("archived refs = %v, want the expired snapshot", archived)
	}
	if out, err := gitCmd("-C", repo, "cat-file", "-e", old+"^{commit}"); err != nil {
		t.Errorf("expired snapshot object was pruned: %v %s", err, out)
	}
}

func TestPruneWorkspaceArchiveDropsRefsPastTheArchiveWindow(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo.git")
	runGit(t, repo, "init", "--bare")
	source := filepath.Join(t.TempDir(), "source")
	runGit(t, source, "init")
	runGit(t, source, "config", "user.email", "sparkwing@example.invalid")
	runGit(t, source, "config", "user.name", "Sparkwing Test")
	runGit(t, source, "commit", "--allow-empty", "-m", "snapshot")
	sha := strings.TrimSpace(runGit(t, source, "rev-parse", "HEAD"))
	runGit(t, repo, "fetch", source, sha)

	now := time.Now().UTC()
	inside := fmt.Sprintf("%s%020d/%s", workspaceArchiveRefPrefix, now.Add(-48*time.Hour).UnixNano(), sha)
	outside := fmt.Sprintf("%s%020d/%s", workspaceArchiveRefPrefix, now.Add(-30*24*time.Hour).UnixNano(), sha)
	runGit(t, repo, "update-ref", inside, sha)
	runGit(t, repo, "update-ref", outside, sha)

	if err := pruneWorkspaceArchive(repo, 128, 24*time.Hour, now); err != nil {
		t.Fatalf("pruneWorkspaceArchive: %v", err)
	}

	refs := strings.Fields(runGit(t, repo, "for-each-ref", "--format=%(refname)", workspaceArchiveRefPrefix))
	if len(refs) != 1 || refs[0] != inside {
		t.Errorf("archived refs = %v, want only %s", refs, inside)
	}
}

func TestMetricsDoNotEnumerateMirrors(t *testing.T) {
	srv := newTestServer(t, "s3cret")
	isolateRepoNames(t)

	const repoURL = "https://git.example.invalid/acme/secret-service.git"
	hash := repoHash(repoURL)
	repoNamesMu.Lock()
	repoNames["secret-service"] = repoURL
	repoNamesMu.Unlock()
	runGit(t, filepath.Join(repoDir, hash+".git"), "init", "--bare")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go backgroundFetchLoop(ctx, time.Millisecond)

	deadline := time.Now().Add(10 * time.Second)
	var body string
	for time.Now().Before(deadline) {
		resp, err := srv.Client().Get(srv.URL + "/metrics")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		body = string(raw)
		if strings.Contains(body, "sparkwing_gitcache_fetch_duration_seconds") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(body, "sparkwing_gitcache_fetch_duration_seconds") {
		t.Fatalf("/metrics never exported the fetch histogram:\n%s", body)
	}
	for _, leak := range []string{hash, "secret-service", `repo="`} {
		if strings.Contains(body, leak) {
			t.Errorf("/metrics leaks %q:\n%s", leak, body)
		}
	}
}

func TestSetupSSHFailsWhenTheKeyCannotBeStaged(t *testing.T) {
	saved := sshKeyDir
	t.Cleanup(func() { sshKeyDir = saved })
	root := t.TempDir()

	sshKeyDir = filepath.Join(root, "absent")
	if err := setupSSH(); err != nil {
		t.Fatalf("setupSSH with no key secret: %v", err)
	}

	sshKeyDir = filepath.Join(root, "key")
	if err := os.MkdirAll(sshKeyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshKeyDir, "id_ed25519"), []byte("private-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	if err := os.WriteFile(home, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	err := setupSSH()
	if err == nil {
		t.Fatal("setupSSH accepted a home it cannot write, leaving every private-repo mirror keyless")
	}
	if !strings.Contains(err.Error(), "stage SSH key") {
		t.Fatalf("err = %v, want it to name the staging step", err)
	}
}
