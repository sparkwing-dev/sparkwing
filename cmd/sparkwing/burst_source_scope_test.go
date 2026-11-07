package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBurstCapturesOnlyDeclaredAndApprovedSourceFiles(t *testing.T) {
	repo := initSnapshotRepo(t)
	writeSnapshotFile(t, repo, ".gitignore", "*.local\n", 0o644)
	writeSnapshotFile(t, repo, "tracked.txt", "base\n", 0o644)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "-m", "base")
	writeSnapshotFile(t, repo, "declared.local", "declared\n", 0o644)
	writeSnapshotFile(t, repo, "other.local", "approved for another pipeline\n", 0o600)

	snapshot, err := burstSourceSnapshot(t.Context(), os.Environ(), repo, []string{"declared.local", "tracked.txt"}, []string{"declared.local", "other.local"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := snapshot.close(); err != nil {
			t.Error(err)
		}
	})
	checkout := importSnapshotBundle(t, snapshot)
	assertSnapshotFile(t, checkout, "declared.local", "declared\n")
	assertSnapshotFile(t, checkout, "tracked.txt", "base\n")
	if _, err := os.Stat(filepath.Join(checkout, "other.local")); !os.IsNotExist(err) {
		t.Fatalf("a machine-approved file the pipeline did not declare was captured: %v", err)
	}

	if _, err := burstSourceSnapshot(t.Context(), os.Environ(), repo, []string{"other.local", "declared.local"}, []string{"declared.local"}, nil); err == nil || !strings.Contains(err.Error(), "run.routing.source_files") {
		t.Fatalf("an unapproved declaration was accepted: %v", err)
	}
}

func TestExplicitCloudUploadsOnlyNamedSecretShapedFiles(t *testing.T) {
	repo := initSnapshotRepo(t)
	writeSnapshotFile(t, repo, "fixture/cache-key.txt", "not a credential\n", 0o644)
	writeSnapshotFile(t, repo, "tracked.txt", "base\n", 0o644)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "-m", "base")

	if _, err := burstSourceSnapshot(t.Context(), os.Environ(), repo, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "fixture/cache-key.txt") {
		t.Fatalf("a secret-shaped name was uploaded without approval: %v", err)
	}
	if _, err := burstSourceSnapshot(t.Context(), os.Environ(), repo, nil, nil, []string{"missing.key"}); err == nil || !strings.Contains(err.Error(), "missing.key") {
		t.Fatalf("an unmatched approval was accepted: %v", err)
	}
	snapshot, err := burstSourceSnapshot(t.Context(), os.Environ(), repo, nil, nil, []string{"fixture/cache-key.txt"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := snapshot.close(); err != nil {
			t.Error(err)
		}
	})
	assertSnapshotFile(t, importSnapshotBundle(t, snapshot), "fixture/cache-key.txt", "not a credential\n")
}
