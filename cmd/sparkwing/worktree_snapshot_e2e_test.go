//go:build e2e

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
