package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/quick"
)

func TestTriggerSnapshotSelectsCapturedPipelineAndLeavesCheckoutUntouched(t *testing.T) {
	repo, original := triggerPreparationRepo(t)
	writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: deploy-prod\n    entrypoint: Dirty\n", 0o600)
	writeSnapshotFile(t, repo, "untracked.txt", "captured", 0o600)
	before := runSnapshotGit(t, repo, "status", "--porcelain")
	snapshot, err := captureWorktreeSnapshot(t.Context(), repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := snapshot.close(); err != nil {
			t.Error(err)
		}
	})
	directory, err := snapshotTriggerCheckout(t.Context(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	input := triggerPreparationInput(repo)
	input.checkout = directory
	selected, err := prepareTriggerSource(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if selected.git.SHA == original || selected.git.SHA != snapshot.SHA || selected.source.Ref != snapshot.SHA || selected.source.Entrypoint != "Dirty" {
		t.Fatalf("captured source: %+v", selected)
	}
	data, err := os.ReadFile(filepath.Join(directory, "untracked.txt"))
	if err != nil || string(data) != "captured" {
		t.Fatalf("snapshot file: %q %v", data, err)
	}
	if after := runSnapshotGit(t, repo, "status", "--porcelain"); after != before {
		t.Fatalf("checkout changed: before=%q after=%q", before, after)
	}
}

func TestTriggerInspectionCheckoutUsesSelectedCommittedSource(t *testing.T) {
	repo, sha := triggerPreparationRepo(t)
	writeSnapshotFile(t, repo, "untracked.txt", "private working copy", 0o600)
	directory, err := triggerInspectionCheckout(t.Context(), repo, sha, "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if actual := strings.TrimSpace(runSnapshotGit(t, directory, "rev-parse", "HEAD")); actual != sha {
		t.Fatalf("inspection SHA=%s, want %s", actual, sha)
	}
	if _, err := os.Stat(filepath.Join(directory, "untracked.txt")); !os.IsNotExist(err) {
		t.Fatal("committed inspection included untracked content")
	}
	if origin := strings.TrimSpace(runSnapshotGit(t, directory, "remote", "get-url", "origin")); origin != "https://github.com/acme/app.git" {
		t.Fatalf("inspection origin=%s", origin)
	}
}

func TestTriggerSnapshotIncludesPinnedPipelineRevision(t *testing.T) {
	repo, original := triggerPreparationRepo(t)
	writeSnapshotFile(t, repo, "untracked.txt", "captured", 0o600)
	snapshot, err := captureWorktreeSnapshot(t.Context(), repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := snapshot.close(); err != nil {
			t.Error(err)
		}
	})
	if err := snapshot.includeRevision(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(t.TempDir(), "bundle")
	runSnapshotGit(t, repo, "clone", "--quiet", "--mirror", snapshot.BundlePath, clone)
	for _, sha := range []string{snapshot.SHA, original} {
		runSnapshotGit(t, clone, "cat-file", "-e", sha+"^{commit}")
	}
}

func TestTriggerInspectionIgnoresAmbientGitIdentityProperty(t *testing.T) {
	repo, sha := triggerPreparationRepo(t)
	if err := quick.Check(func(forged string) bool {
		t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "foreign.git"))
		t.Setenv("GIT_CONFIG_COUNT", "1")
		t.Setenv("GIT_CONFIG_KEY_0", "remote.origin.url")
		t.Setenv("GIT_CONFIG_VALUE_0", "https://foreign.example/"+strings.Map(func(r rune) rune {
			if r < 33 || r > 126 {
				return 'x'
			}
			return r
		}, forged))
		directory, err := triggerInspectionCheckout(t.Context(), repo, sha, "", t.TempDir())
		if err != nil {
			t.Log(err)
			return false
		}
		actual, err := (triggerCheckout{directory: directory, environment: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}}).read(t.Context(), "rev-parse", "HEAD")
		return err == nil && actual == sha
	}, &quick.Config{MaxCount: 16}); err != nil {
		t.Fatal(err)
	}
}
