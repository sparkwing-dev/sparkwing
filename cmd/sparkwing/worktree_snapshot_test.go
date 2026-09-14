package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
)

func TestCaptureWorktreeSnapshotRejectsUnmergedIndex(t *testing.T) {
	repo := initSnapshotRepo(t)
	writeSnapshotFile(t, repo, "file.txt", "base\n", 0o644)
	runSnapshotGit(t, repo, "add", "file.txt")
	runSnapshotGit(t, repo, "commit", "-m", "base")
	head := strings.TrimSpace(runSnapshotGit(t, repo, "rev-parse", "HEAD"))
	blob := strings.TrimSpace(runSnapshotGitInput(t, repo, "ours\n", "hash-object", "-w", "--stdin"))
	runSnapshotGitInput(t, repo, "100644 "+blob+" 1\tfile.txt\n100644 "+blob+" 2\tfile.txt\n100644 "+blob+" 3\tfile.txt\n", "update-index", "--index-info")

	_, err := captureWorktreeSnapshot(context.Background(), repo)
	if err == nil || !strings.Contains(err.Error(), "unmerged index") {
		t.Fatalf("error = %v", err)
	}
	if got := strings.TrimSpace(runSnapshotGit(t, repo, "rev-parse", "HEAD")); got != head {
		t.Fatalf("HEAD changed: got %s want %s", got, head)
	}
}

func TestCaptureWorktreeSnapshotRejectsUnsupportedRepositoryShapesEarly(t *testing.T) {
	t.Run("shallow", func(t *testing.T) {
		origin := initSnapshotRepo(t)
		writeSnapshotFile(t, origin, "value", "one\n", 0o644)
		runSnapshotGit(t, origin, "add", "value")
		runSnapshotGit(t, origin, "commit", "-m", "one")
		writeSnapshotFile(t, origin, "value", "two\n", 0o644)
		runSnapshotGit(t, origin, "commit", "-am", "two")
		checkout := filepath.Join(t.TempDir(), "shallow")
		cmd := exec.Command("git", "clone", "--quiet", "--depth", "1", "file://"+origin, checkout)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("shallow clone: %v: %s", err, out)
		}
		_, err := captureWorktreeSnapshot(context.Background(), checkout)
		if err == nil || !strings.Contains(err.Error(), "complete repository") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("sha256", func(t *testing.T) {
		repo := filepath.Join(t.TempDir(), "sha256")
		if err := os.Mkdir(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("git", "-C", repo, "init", "--quiet", "--object-format=sha256")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("Git does not support SHA-256 repositories: %v: %s", err, out)
		}
		runSnapshotGit(t, repo, "config", "user.name", "Snapshot Test")
		runSnapshotGit(t, repo, "config", "user.email", "snapshot@example.test")
		writeSnapshotFile(t, repo, "value", "one\n", 0o644)
		runSnapshotGit(t, repo, "add", "value")
		runSnapshotGit(t, repo, "commit", "-m", "one")
		_, err := captureWorktreeSnapshot(context.Background(), repo)
		if err == nil || !strings.Contains(err.Error(), "SHA-1 repositories only") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestCaptureWorktreeSnapshotRejectsCompressibleUncompressedOversize(t *testing.T) {
	repo := initSnapshotRepo(t)
	writeSnapshotFile(t, repo, "large.txt", strings.Repeat("0", 2048), 0o644)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "-m", "compressible source")

	snapshot, err := captureWorktreeSnapshotWithLimits(context.Background(), repo, worktreeSnapshotLimits{
		bytes: 1024,
		files: maxWorktreeSnapshotFiles,
	})
	if snapshot != nil {
		_ = snapshot.close()
	}
	if err == nil || !strings.Contains(err.Error(), "uncompressed source limit") {
		t.Fatalf("capture error = %v, want uncompressed source limit", err)
	}
}

func TestCaptureWorktreeSnapshotRejectsFileCountOversize(t *testing.T) {
	repo := initSnapshotRepo(t)
	writeSnapshotFile(t, repo, "one.txt", "one\n", 0o644)
	writeSnapshotFile(t, repo, "two.txt", "two\n", 0o644)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "-m", "two files")

	snapshot, err := captureWorktreeSnapshotWithLimits(context.Background(), repo, worktreeSnapshotLimits{
		bytes: maxWorktreeSnapshotBytes,
		files: 1,
	})
	if snapshot != nil {
		_ = snapshot.close()
	}
	if err == nil || !strings.Contains(err.Error(), "more than 1 files") {
		t.Fatalf("capture error = %v, want file-count limit", err)
	}
}

func initSnapshotRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runSnapshotGit(t, repo, "init", "--quiet")
	runSnapshotGit(t, repo, "config", "user.name", "Snapshot Test")
	runSnapshotGit(t, repo, "config", "user.email", "snapshot@example.test")
	runSnapshotGit(t, repo, "config", "commit.gpgsign", "false")
	return repo
}

func writeSnapshotFile(t *testing.T, repo, name, body string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(repo, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func runSnapshotGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	return runSnapshotGitInput(t, repo, "", args...)
}

func runSnapshotGitInput(t *testing.T, repo, input string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "commit.gpgSign=false", "-C", repo}, args...)...)
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func importSnapshotBundle(t *testing.T, snapshot *worktreeSnapshot) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "checkout")
	runSnapshotGit(t, filepath.Dir(dir), "init", "--quiet", dir)
	ref := bincache.SeedRef(snapshot.SHA)
	runSnapshotGit(t, dir, "fetch", "--quiet", snapshot.BundlePath, ref+":"+ref)
	runSnapshotGit(t, dir, "checkout", "--quiet", snapshot.SHA)
	return dir
}

func assertSnapshotFile(t *testing.T, root, name, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", name, got, want)
	}
}
