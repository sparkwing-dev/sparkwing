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
	snapshot, err := captureWorktreeSnapshot(t.Context(), os.Environ(), repo, nil)
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
	input.snapshot = snapshot
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
	directory, err := triggerInspectionCheckout(t.Context(), os.Environ(), repo, sha, "", t.TempDir())
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
	snapshot, err := captureWorktreeSnapshot(t.Context(), os.Environ(), repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := snapshot.close(); err != nil {
			t.Error(err)
		}
	})
	if err := snapshot.includeRevisions(t.Context(), original); err != nil {
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
		directory, err := triggerInspectionCheckout(t.Context(), os.Environ(), repo, sha, "", t.TempDir())
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

func TestWorkingTreeTriggerPreservesNamedReferenceIdentity(t *testing.T) {
	for _, ref := range []string{"release", "origin/main"} {
		t.Run(ref, func(t *testing.T) {
			repo, base := triggerPreparationRepo(t)
			runSnapshotGit(t, repo, "tag", "-d", "release")
			runSnapshotGit(t, repo, "branch", "-m", "main")
			runSnapshotGit(t, repo, "checkout", "--quiet", "-b", "release")
			writeSnapshotFile(t, repo, "selected.txt", "selected code\n", 0o644)
			runSnapshotGit(t, repo, "add", "selected.txt")
			runSnapshotGit(t, repo, "commit", "--quiet", "-m", "selected code")
			selectedSHA := strings.TrimSpace(runSnapshotGit(t, repo, "rev-parse", "HEAD"))
			runSnapshotGit(t, repo, "update-ref", "refs/remotes/origin/main", selectedSHA)
			runSnapshotGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
			runSnapshotGit(t, repo, "checkout", "--quiet", "main")
			if strings.TrimSpace(runSnapshotGit(t, repo, "rev-parse", "HEAD")) != base {
				t.Fatal("fixture main changed")
			}
			writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: deploy-prod\n    entrypoint: Dirty\n    source: "+ref+"\n", 0o600)
			snapshot, err := captureWorktreeSnapshot(t.Context(), os.Environ(), repo, nil)
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
			if refs := runSnapshotGit(t, directory, "for-each-ref", "--format=%(refname)"); strings.Contains(refs, "refs/heads/release") {
				t.Fatal("inspection imported an unselected branch")
			}
			input := triggerPreparationInput(repo)
			input.snapshot = snapshot
			selection, err := prepareTriggerSource(t.Context(), input)
			if err != nil {
				t.Fatal(err)
			}
			if selection.git.SHA != snapshot.SHA || selection.source.Ref != selectedSHA || selection.source.RequestedRef != ref || selection.source.Entrypoint != "Dirty" {
				t.Fatalf("selection lost named reference identity: execution=%s code=%s intent=%s entry=%s", selection.git.SHA, selection.source.Ref, selection.source.RequestedRef, selection.source.Entrypoint)
			}
			if !selection.git.DefaultBranch.Set || selection.git.DefaultBranch.Value != "main" {
				t.Fatal("inspection lost the original symbolic default branch")
			}
			input.ref = ref
			selection, err = prepareTriggerSource(t.Context(), input)
			if err != nil || selection.git.SHA != selectedSHA {
				t.Fatalf("explicit execution reference changed: got=%s want=%s err=%v", selection.git.SHA, selectedSHA, err)
			}
		})
	}
}
