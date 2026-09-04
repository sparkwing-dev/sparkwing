package sourceidentity

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitTreeManifestDigestBindsExactRecursiveTree(t *testing.T) {
	repo := t.TempDir()
	runGitTreeTest(t, repo, "init", "--quiet")
	runGitTreeTest(t, repo, "config", "user.name", "test")
	runGitTreeTest(t, repo, "config", "user.email", "test@example.test")
	runGitTreeTest(t, repo, "config", "commit.gpgsign", "false")
	if err := os.MkdirAll(filepath.Join(repo, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "nested", "source.txt"), []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTreeTest(t, repo, "add", ".")
	runGitTreeTest(t, repo, "commit", "--quiet", "-m", "first")
	first := strings.TrimSpace(runGitTreeTest(t, repo, "rev-parse", "HEAD"))
	firstDigest, err := GitTreeManifestDigest(context.Background(), repo, first)
	if err != nil {
		t.Fatal(err)
	}
	if !IsSHA256(firstDigest) {
		t.Fatalf("manifest digest = %q, want canonical sha256", firstDigest)
	}
	if err := os.WriteFile(filepath.Join(repo, "nested", "source.txt"), []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTreeTest(t, repo, "add", ".")
	runGitTreeTest(t, repo, "commit", "--quiet", "-m", "second")
	second := strings.TrimSpace(runGitTreeTest(t, repo, "rev-parse", "HEAD"))
	secondDigest, err := GitTreeManifestDigest(context.Background(), repo, second)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest == secondDigest {
		t.Fatal("different recursive Git trees produced the same manifest digest")
	}
	again, err := GitTreeManifestDigest(context.Background(), repo, first)
	if err != nil || again != firstDigest {
		t.Fatalf("old immutable tree digest = (%q, %v), want %q", again, err, firstDigest)
	}
	if _, err := GitTreeManifestDigest(context.Background(), repo, "missing"); err == nil {
		t.Fatal("missing revision produced a manifest digest")
	}
}

func runGitTreeTest(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}
