package jobs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureLineageContainsLatestRelease(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	side := filepath.Join(root, "side")

	runTestGit(t, root, "init", "--bare", remote)
	runTestGit(t, root, "clone", remote, work)
	runTestGit(t, work, "config", "user.name", "Test User")
	runTestGit(t, work, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, work, "add", "README.md")
	runTestGit(t, work, "commit", "-m", "initial")
	runTestGit(t, work, "branch", "-M", "main")
	runTestGit(t, work, "push", "-u", "origin", "main")

	if err := ensureLineageContainsLatestRelease(ctx, work); err != nil {
		t.Fatalf("repo with no release tags rejected: %v", err)
	}

	runTestGit(t, work, "tag", "-a", "v0.1.0", "-m", "Release v0.1.0")
	runTestGit(t, work, "push", "origin", "refs/tags/v0.1.0")
	if err := ensureLineageContainsLatestRelease(ctx, work); err != nil {
		t.Fatalf("line containing latest release rejected: %v", err)
	}

	runTestGit(t, root, "clone", remote, side)
	runTestGit(t, side, "config", "user.name", "Test User")
	runTestGit(t, side, "config", "user.email", "test@example.com")
	runTestGit(t, side, "checkout", "-b", "release-line")
	if err := os.WriteFile(filepath.Join(side, "README.md"), []byte("orphan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, side, "add", "README.md")
	runTestGit(t, side, "commit", "-m", "work released off a branch")
	runTestGit(t, side, "tag", "-a", "v0.2.0", "-m", "Release v0.2.0")
	runTestGit(t, side, "push", "origin", "refs/tags/v0.2.0")

	err := ensureLineageContainsLatestRelease(ctx, work)
	if err == nil {
		t.Fatal("line missing the latest release passed the lineage gate")
	}
	if !strings.Contains(err.Error(), "v0.2.0") {
		t.Fatalf("gate error does not name the missing release: %v", err)
	}

	runTestGit(t, work, "fetch", "--tags", "origin")
	runTestGit(t, work, "merge", "v0.2.0", "-m", "bring the release line back")
	if err := ensureLineageContainsLatestRelease(ctx, work); err != nil {
		t.Fatalf("line rejected after merging the release back: %v", err)
	}

	runTestGit(t, side, "checkout", "-b", "tombstone")
	runTestGit(t, side, "tag", "-a", "v1.6.1", "-m", "tombstone")
	runTestGit(t, side, "push", "origin", "refs/tags/v1.6.1")
	if err := ensureLineageContainsLatestRelease(ctx, work); err != nil {
		t.Fatalf("retracted v1.x tombstone tag biased the lineage gate: %v", err)
	}
}
