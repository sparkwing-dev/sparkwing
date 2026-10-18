package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
)

func TestSnapshotBundleStopsAtCommitsTheRemoteHas(t *testing.T) {
	repo, _ := triggerPreparationRepo(t)
	const leaked = "TOKEN=pushed-history-secret\n"
	writeSnapshotFile(t, repo, ".env", leaked, 0o600)
	runSnapshotGit(t, repo, "add", "-f", ".env")
	runSnapshotGit(t, repo, "commit", "--quiet", "-m", "add env")
	runSnapshotGit(t, repo, "rm", "--quiet", ".env")
	runSnapshotGit(t, repo, "commit", "--quiet", "-m", "remove env")
	origin := filepath.Join(t.TempDir(), "origin.git")
	runSnapshotGit(t, repo, "clone", "--quiet", "--bare", repo, origin)
	runSnapshotGit(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	writeSnapshotFile(t, repo, "local.txt", "unpushed\n", 0o644)
	runSnapshotGit(t, repo, "add", "local.txt")
	runSnapshotGit(t, repo, "commit", "--quiet", "-m", "unpushed")
	execution := strings.TrimSpace(runSnapshotGit(t, repo, "rev-parse", "HEAD"))
	writeSnapshotFile(t, repo, "dirty.txt", "dirty\n", 0o644)
	envBlob := strings.TrimSpace(runSnapshotGit(t, repo, "rev-parse", "HEAD~2:.env"))

	snapshot, err := captureWorktreeSnapshot(t.Context(), os.Environ(), repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := snapshot.close(); err != nil {
			t.Error(err)
		}
	})
	if err := snapshot.includeRevisions(t.Context(), origin, execution); err != nil {
		t.Fatal(err)
	}
	bundle, err := os.ReadFile(snapshot.BundlePath)
	if err != nil {
		t.Fatal(err)
	}
	header, _, _ := strings.Cut(string(bundle), "\n\n")
	if !strings.Contains(header, "\n-") {
		t.Fatalf("bundle carries the remote's history instead of naming it as a prerequisite")
	}

	local := preparedTriggerCheckout(t, snapshot.environment, snapshot.RepoRoot, execution, snapshot.BundlePath)
	if _, err := os.Stat(filepath.Join(local, "local.txt")); err != nil {
		t.Fatalf("local inspection of the thin bundle: %v", err)
	}

	sourcetest.Remote(t, map[string]string{"https://8.8.8.8/acme/app": origin})
	entry := acquireSnapshotBundle(t, snapshot, source.Spec{SHA: execution, PipelineSHA: execution, PipelineDir: ".sparkwing"})
	if entry.SHA != execution {
		t.Fatalf("execution SHA=%s, want %s", entry.SHA, execution)
	}
	if _, err := os.Stat(filepath.Join(entry.CheckoutDirectory(), "local.txt")); err != nil {
		t.Fatal(err)
	}
	cmd := snapshotGitCommand(t.Context(), os.Environ(), "-C", entry.CheckoutDirectory(), "cat-file", "-e", envBlob)
	if cmd.Run() == nil {
		t.Fatal("the removed .env from pushed history reached the receiver")
	}
}

func TestSnapshotBundleKeepsCommitsTheRunRemoteLacks(t *testing.T) {
	repo, _ := triggerPreparationRepo(t)
	origin := filepath.Join(t.TempDir(), "origin.git")
	runSnapshotGit(t, repo, "clone", "--quiet", "--bare", repo, origin)
	runSnapshotGit(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	writeSnapshotFile(t, repo, "fork.txt", "fork only\n", 0o644)
	runSnapshotGit(t, repo, "add", "fork.txt")
	runSnapshotGit(t, repo, "commit", "--quiet", "-m", "on fork only")
	fork := strings.TrimSpace(runSnapshotGit(t, repo, "rev-parse", "HEAD"))
	runSnapshotGit(t, repo, "update-ref", "refs/remotes/fork/main", "HEAD")
	writeSnapshotFile(t, repo, "stale.txt", "deleted upstream\n", 0o644)
	runSnapshotGit(t, repo, "add", "stale.txt")
	runSnapshotGit(t, repo, "commit", "--quiet", "-m", "branch the remote deleted")
	stale := strings.TrimSpace(runSnapshotGit(t, repo, "rev-parse", "HEAD"))
	runSnapshotGit(t, repo, "update-ref", "refs/remotes/origin/gone", "HEAD")
	writeSnapshotFile(t, repo, "local.txt", "unpushed\n", 0o644)
	runSnapshotGit(t, repo, "add", "local.txt")
	runSnapshotGit(t, repo, "commit", "--quiet", "-m", "unpushed")
	execution := strings.TrimSpace(runSnapshotGit(t, repo, "rev-parse", "HEAD"))

	snapshot, err := captureWorktreeSnapshot(t.Context(), os.Environ(), repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := snapshot.close(); err != nil {
			t.Error(err)
		}
	})
	if err := snapshot.includeRevisions(t.Context(), origin, execution); err != nil {
		t.Fatal(err)
	}
	bundle, err := os.ReadFile(snapshot.BundlePath)
	if err != nil {
		t.Fatal(err)
	}
	header, _, _ := strings.Cut(string(bundle), "\n\n")
	for name, sha := range map[string]string{"fork-only": fork, "stale tracking": stale} {
		if strings.Contains(header, "-"+sha) {
			t.Errorf("the %s commit %s was assumed present on the run's remote", name, sha)
		}
	}
	sourcetest.Remote(t, map[string]string{"https://8.8.8.8/acme/app": origin})
	entry := acquireSnapshotBundle(t, snapshot, source.Spec{SHA: execution, PipelineSHA: execution, PipelineDir: ".sparkwing"})
	for _, file := range []string{"fork.txt", "stale.txt", "local.txt"} {
		if _, err := os.Stat(filepath.Join(entry.CheckoutDirectory(), file)); err != nil {
			t.Fatal(err)
		}
	}
}
