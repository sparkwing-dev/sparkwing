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
	longUnicodePath := filepath.Join("unicode-雪", strings.Repeat("a", 180)+"-界.txt")
	writeSnapshotFile(t, repo, longUnicodePath, "long unicode path\n", 0o644)
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
	deletedBlob := strings.TrimSpace(runSnapshotGit(t, repo, "rev-parse", "HEAD:delete.txt"))
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
	if runtime.GOOS != "windows" {
		info, err := os.Stat(snapshot.tempDir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("snapshot workspace mode = %v", info.Mode().Perm())
		}
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
	parents := strings.Fields(runSnapshotGit(t, checkout, "rev-list", "--parents", "-n", "1", snapshot.SHA))
	if len(parents) != 1 || parents[0] != snapshot.SHA {
		t.Fatalf("snapshot commit ancestry = %q, want one parentless commit", parents)
	}
	if got := strings.Fields(runSnapshotGit(t, checkout, "rev-list", "--all")); len(got) != 1 || got[0] != snapshot.SHA {
		t.Fatalf("snapshot history = %q, want only %s", got, snapshot.SHA)
	}
	if exec.Command("git", "-C", checkout, "cat-file", "-e", snapshot.BaseSHA+"^{commit}").Run() == nil {
		t.Fatal("source HEAD history entered the snapshot bundle")
	}
	if exec.Command("git", "-C", checkout, "cat-file", "-e", deletedBlob+"^{blob}").Run() == nil {
		t.Fatal("a blob deleted from the working tree entered the snapshot bundle")
	}
	bundleHeads := strings.Fields(runSnapshotGit(t, checkout, "bundle", "list-heads", snapshot.BundlePath))
	if len(bundleHeads) != 2 || bundleHeads[0] != snapshot.SHA || bundleHeads[1] != bincache.SeedRef(snapshot.SHA) {
		t.Fatalf("snapshot bundle heads = %q", bundleHeads)
	}
	assertSnapshotFile(t, checkout, "tracked.txt", "working\n")
	assertSnapshotFile(t, checkout, "staged.txt", "working version\n")
	assertSnapshotFile(t, checkout, "untracked.txt", "new\n")
	assertSnapshotFile(t, checkout, longUnicodePath, "long unicode path\n")
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
		if strings.Contains(err.Error(), "asset.bin") {
			t.Fatalf("content-filter error exposed a source filename: %v", err)
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

func TestCaptureWorktreeSnapshotRejectsUnsafeSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating Git symlinks requires Windows developer mode")
	}
	for _, tc := range []struct {
		name, link, target, want string
	}{
		{"absolute", "absolute", "/etc/passwd", "absolute symlink"},
		{"escaping", "nested/.env-production-secret", "../../outside-secret", "escaping symlink"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := initSnapshotRepo(t)
			writeSnapshotFile(t, repo, "base", "x\n", 0o644)
			if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, tc.link)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(tc.target, filepath.Join(repo, tc.link)); err != nil {
				t.Fatal(err)
			}
			runSnapshotGit(t, repo, "add", ".")
			runSnapshotGit(t, repo, "commit", "-m", tc.name)
			if _, err := captureWorktreeSnapshot(context.Background(), repo); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("capture error = %v, want %q", err, tc.want)
			} else if strings.Contains(err.Error(), ".env-production-secret") || strings.Contains(err.Error(), "outside-secret") {
				t.Fatalf("capture error exposed a source filename or symlink target: %v", err)
			}
		})
	}
	t.Run("cycle", func(t *testing.T) {
		repo := initSnapshotRepo(t)
		if err := os.Symlink("b", filepath.Join(repo, "a")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("a", filepath.Join(repo, "b")); err != nil {
			t.Fatal(err)
		}
		runSnapshotGit(t, repo, "add", ".")
		runSnapshotGit(t, repo, "commit", "-m", "cycle")
		if _, err := captureWorktreeSnapshot(context.Background(), repo); err == nil || !strings.Contains(err.Error(), "symlink cycle") {
			t.Fatalf("capture error = %v", err)
		}
	})
}

func TestMaterializeFleetSnapshotNeverCarriesCredentialedOrigin(t *testing.T) {
	repo := initSnapshotRepo(t)
	writeSnapshotFile(t, repo, "value", "exact\n", 0o644)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "-m", "source")
	runSnapshotGit(t, repo, "remote", "add", "origin", "https://secret@example.com/private/repo.git")
	snapshot, err := captureWorktreeSnapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snapshot.close() }()
	checkout, repoURL, err := snapshot.materialize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(repoURL, "secret") || !strings.Contains(repoURL, "source.sparkwing.invalid") {
		t.Fatalf("materialized repo URL = %q", repoURL)
	}
	configured := strings.TrimSpace(runSnapshotGit(t, checkout, "remote", "get-url", "origin"))
	if configured != repoURL {
		t.Fatalf("checkout origin = %q, want %q", configured, repoURL)
	}
	if got := strings.TrimSpace(runSnapshotGit(t, checkout, "rev-parse", "HEAD")); got != snapshot.SHA {
		t.Fatalf("coordinator checkout = %q, want exact snapshot %q", got, snapshot.SHA)
	}
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
