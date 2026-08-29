package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
)

func TestCaptureWorktreeSnapshotPreservesExactWorkingTreeWithoutMutatingRepository(t *testing.T) {
	repo := initSnapshotRepo(t)
	writeSnapshotFile(t, repo, "tracked.txt", "base\n", 0o644)
	writeSnapshotFile(t, repo, "delete.txt", "delete me\n", 0o644)
	writeSnapshotFile(t, repo, "staged.txt", "base staged\n", 0o644)
	writeSnapshotFile(t, repo, ".gitattributes", "*.crlf text eol=lf\n", 0o644)
	writeSnapshotFile(t, repo, "exact.crlf", "base\r\n", 0o644)
	writeSnapshotFile(t, repo, ".gitignore", "ignored.txt\n", 0o644)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "-m", "base")

	writeSnapshotFile(t, repo, "tracked.txt", "working\n", 0o644)
	writeSnapshotFile(t, repo, "staged.txt", "staged version\n", 0o644)
	runSnapshotGit(t, repo, "add", "staged.txt")
	writeSnapshotFile(t, repo, "staged.txt", "working version\n", 0o644)
	writeSnapshotFile(t, repo, "untracked.txt", "new\n", 0o644)
	writeSnapshotFile(t, repo, "ignored.txt", "secret\n", 0o644)
	writeSnapshotFile(t, repo, "script.sh", "#!/bin/sh\n", 0o755)
	writeSnapshotFile(t, repo, "exact.crlf", "working\r\n", 0o644)
	if err := os.Remove(filepath.Join(repo, "delete.txt")); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink("tracked.txt", filepath.Join(repo, "link")); err != nil {
			t.Fatal(err)
		}
		writeSnapshotFile(t, repo, "odd\nname.txt", "newline path\r\n", 0o644)
	}

	headBefore := runSnapshotGit(t, repo, "rev-parse", "HEAD")
	indexPath := strings.TrimSpace(runSnapshotGit(t, repo, "rev-parse", "--git-path", "index"))
	if !filepath.IsAbs(indexPath) {
		indexPath = filepath.Join(repo, indexPath)
	}
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	refsBefore := runSnapshotGit(t, repo, "show-ref")

	snapshot, err := captureWorktreeSnapshot(context.Background(), repo)
	if err != nil {
		t.Fatalf("captureWorktreeSnapshot: %v", err)
	}
	defer func() { _ = snapshot.close() }()

	if snapshot.BaseSHA != strings.TrimSpace(headBefore) {
		t.Fatalf("base SHA = %q, want %q", snapshot.BaseSHA, strings.TrimSpace(headBefore))
	}
	if snapshot.SHA == snapshot.BaseSHA || snapshot.Size == 0 || snapshot.FileCount == 0 {
		t.Fatalf("invalid snapshot metadata: %+v", snapshot)
	}
	if got := runSnapshotGit(t, repo, "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("HEAD changed: got %q want %q", got, headBefore)
	}
	if got := runSnapshotGit(t, repo, "show-ref"); got != refsBefore {
		t.Fatalf("refs changed:\n%s\nwant:\n%s", got, refsBefore)
	}
	indexAfter, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(indexAfter) != string(indexBefore) {
		t.Fatal("real Git index changed")
	}
	if exec.Command("git", "-C", repo, "cat-file", "-e", snapshot.SHA).Run() == nil {
		t.Fatal("snapshot commit leaked into the source repository object database")
	}

	checkout := importSnapshotBundle(t, snapshot)
	assertSnapshotFile(t, checkout, "tracked.txt", "working\n")
	assertSnapshotFile(t, checkout, "staged.txt", "working version\n")
	assertSnapshotFile(t, checkout, "untracked.txt", "new\n")
	assertSnapshotFile(t, checkout, "script.sh", "#!/bin/sh\n")
	assertSnapshotFile(t, checkout, "exact.crlf", "working\r\n")
	if _, err := os.Stat(filepath.Join(checkout, "delete.txt")); !os.IsNotExist(err) {
		t.Fatalf("deleted file survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(checkout, "ignored.txt")); !os.IsNotExist(err) {
		t.Fatalf("ignored file entered snapshot: %v", err)
	}
	mode, err := os.Stat(filepath.Join(checkout, "script.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && mode.Mode().Perm()&0o111 == 0 {
		t.Fatalf("executable bit lost: %v", mode.Mode())
	}
	if runtime.GOOS != "windows" {
		target, err := os.Readlink(filepath.Join(checkout, "link"))
		if err != nil || target != "tracked.txt" {
			t.Fatalf("symlink target = %q, err=%v", target, err)
		}
		assertSnapshotFile(t, checkout, "odd\nname.txt", "newline path\r\n")
	}

	again, err := captureWorktreeSnapshot(context.Background(), repo)
	if err != nil {
		t.Fatalf("second capture: %v", err)
	}
	defer func() { _ = again.close() }()
	if again.SHA != snapshot.SHA {
		t.Fatalf("same source produced %s then %s", snapshot.SHA, again.SHA)
	}
}

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

func TestCaptureWorktreeSnapshotRejectsGitlinksAndContentFilters(t *testing.T) {
	t.Run("gitlink", func(t *testing.T) {
		repo := initSnapshotRepo(t)
		writeSnapshotFile(t, repo, "base", "x\n", 0o644)
		runSnapshotGit(t, repo, "add", "base")
		runSnapshotGit(t, repo, "commit", "-m", "base")
		nested := initSnapshotRepo(t)
		writeSnapshotFile(t, nested, "nested.txt", "nested\n", 0o644)
		runSnapshotGit(t, nested, "add", "nested.txt")
		runSnapshotGit(t, nested, "commit", "-m", "nested")
		runSnapshotGit(t, repo, "-c", "protocol.file.allow=always", "submodule", "add", "--quiet", nested, "nested")
		runSnapshotGit(t, repo, "commit", "-m", "submodule")

		_, err := captureWorktreeSnapshot(context.Background(), repo)
		if err == nil || !strings.Contains(err.Error(), "submodules") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("filter", func(t *testing.T) {
		repo := initSnapshotRepo(t)
		writeSnapshotFile(t, repo, ".gitattributes", "*.bin filter=lfs\n", 0o644)
		writeSnapshotFile(t, repo, "asset.bin", "pointer-or-bytes\n", 0o644)
		runSnapshotGit(t, repo, "add", ".gitattributes")
		runSnapshotGit(t, repo, "commit", "-m", "base")

		_, err := captureWorktreeSnapshot(context.Background(), repo)
		if err == nil || !strings.Contains(err.Error(), `content filter "lfs"`) {
			t.Fatalf("error = %v", err)
		}
	})
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
