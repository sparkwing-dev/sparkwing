package orchestrator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
)

func TestLocalFleetSourceServesOnlyExactAuthenticatedSnapshot(t *testing.T) {
	fixture := newFleetSourceFixture(t)

	source, err := startLocalFleetSource(fixture.root, fixture.bundle, fixture.sha, fixture.repoURL, "private-cache-hop")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/v1/runs/exact-run/gitcache")
		source.handler().ServeHTTP(w, r)
	}))
	defer proxy.Close()
	dest := filepath.Join(fixture.root, "dest")
	gitcacheURL := proxy.URL + "/api/v1/runs/exact-run/gitcache"
	sparkwingDir, err := bincache.FetchPipelineWorkspaceSourceWithCredentials(context.Background(), gitcacheURL, proxy.URL, "private-cache-hop", "", fixture.repoURL, "main", fixture.sha, dest)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(sparkwingDir, "exact.txt"))
	if err != nil || string(body) != "exact bytes\n" {
		t.Fatalf("materialized source = %q, %v", body, err)
	}
	_, err = bincache.FetchPipelineWorkspaceSourceWithCredentials(context.Background(), gitcacheURL, proxy.URL, "wrong", "", fixture.repoURL, "main", fixture.sha, filepath.Join(fixture.root, "denied"))
	if err == nil {
		t.Fatal("wrong source credential fetched the snapshot")
	}

	if got := strings.Fields(runFleetSourceGit(t, source.bareRepo, "rev-list", "--parents", "--all")); len(got) != 1 || got[0] != fixture.sha {
		t.Fatalf("served snapshot history = %q, want only parentless %s", got, fixture.sha)
	}
	for name, object := range map[string]string{
		"source commit":  fixture.historySHA + "^{commit}",
		"deleted secret": fixture.secretBlob + "^{blob}",
	} {
		if exec.Command("git", "--git-dir", source.bareRepo, "cat-file", "-e", object).Run() == nil {
			t.Fatalf("%s entered the served snapshot object store", name)
		}
	}
	refs := strings.Fields(runFleetSourceGit(t, source.bareRepo, "for-each-ref", "--format=%(refname)"))
	wantRef := bincache.SeedRef(fixture.sha)
	if len(refs) != 1 || refs[0] != wantRef {
		t.Fatalf("served refs = %q, want only %s", refs, wantRef)
	}
	for _, key := range []string{
		"uploadpack.allowAnySHA1InWant",
		"uploadpack.allowReachableSHA1InWant",
		"uploadpack.allowTipSHA1InWant",
	} {
		if got := runFleetSourceGit(t, source.bareRepo, "config", "--get", key); got != "false" {
			t.Fatalf("%s = %q, want false", key, got)
		}
	}
	remote := source.url + "/git/" + source.name
	for name, target := range map[string]string{
		"historical commit": fixture.historySHA,
		"deleted blob":      fixture.secretBlob,
		"arbitrary ref":     "refs/heads/main",
	} {
		t.Run("reject_"+strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			checkout := t.TempDir()
			runFleetSourceGit(t, checkout, "init", "--quiet")
			cmd := exec.Command("git", "-C", checkout, "-c", "http.extraHeader=Authorization: Bearer private-cache-hop", "fetch", "--quiet", remote, target)
			if out, fetchErr := cmd.CombinedOutput(); fetchErr == nil {
				t.Fatalf("fetch %s unexpectedly succeeded: %s", target, out)
			}
		})
	}
}

func TestLocalFleetSourceRejectsHistoryBearingBundle(t *testing.T) {
	fixture := newFleetSourceFixture(t)
	repo := filepath.Join(fixture.root, "history-repo")
	runFleetSourceGit(t, fixture.root, "init", "--quiet", repo)
	runFleetSourceGit(t, repo, "config", "user.name", "test")
	runFleetSourceGit(t, repo, "config", "user.email", "test@example.test")
	runFleetSourceGit(t, repo, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(repo, "one"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runFleetSourceGit(t, repo, "add", ".")
	runFleetSourceGit(t, repo, "commit", "--quiet", "-m", "one")
	if err := os.WriteFile(filepath.Join(repo, "two"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runFleetSourceGit(t, repo, "add", ".")
	runFleetSourceGit(t, repo, "commit", "--quiet", "-m", "two")
	sha := runFleetSourceGit(t, repo, "rev-parse", "HEAD")
	ref := bincache.SeedRef(sha)
	runFleetSourceGit(t, repo, "update-ref", ref, sha)
	bundle := filepath.Join(fixture.root, "history.bundle")
	runFleetSourceGit(t, repo, "bundle", "create", bundle, ref)

	source, err := startLocalFleetSource(filepath.Join(fixture.root, "rejected"), bundle, sha, fixture.repoURL, "private-cache-hop")
	if source != nil {
		source.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "one parentless commit") {
		t.Fatalf("history-bearing source error = %v", err)
	}
}

type fleetSourceFixture struct {
	root, bundle, sha, repoURL string
	historySHA, secretBlob     string
}

func newFleetSourceFixture(t *testing.T) fleetSourceFixture {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.test", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.test")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init", "--quiet", repo)
	runGit("-C", repo, "config", "commit.gpgsign", "false")
	if err := os.MkdirAll(filepath.Join(repo, ".sparkwing"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "deleted-secret.txt"), []byte("historical secret value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("-C", repo, "add", ".")
	runGit("-C", repo, "commit", "--quiet", "-m", "historical source")
	historySHA := runGit("-C", repo, "rev-parse", "HEAD")
	secretBlob := runGit("-C", repo, "rev-parse", "HEAD:deleted-secret.txt")
	if err := os.Remove(filepath.Join(repo, "deleted-secret.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".sparkwing", "exact.txt"), []byte("exact bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("-C", repo, "add", "-A")
	tree := runGit("-C", repo, "write-tree")
	sha := runGit("-C", repo, "commit-tree", tree)
	ref := bincache.SeedRef(sha)
	runGit("-C", repo, "update-ref", ref, sha)
	bundle := filepath.Join(root, "snapshot.bundle")
	runGit("-C", repo, "bundle", "create", bundle, ref)
	return fleetSourceFixture{
		root: root, bundle: bundle, sha: sha,
		repoURL:    "https://source.sparkwing.invalid/exact.git",
		historySHA: historySHA, secretBlob: secretBlob,
	}
}

func runFleetSourceGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	prefix := []string{"-C", repo}
	if strings.HasSuffix(repo, ".git") {
		prefix = []string{"--git-dir", repo}
	}
	cmd := exec.Command("git", append(prefix, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
