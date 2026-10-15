package bincache

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceBaselineFromEnv_KeepsOnlyAUsablePair(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for name, tc := range map[string]struct {
		env  map[string]string
		want WorkspaceBaseline
	}{
		"a remote-tracking ref and a commit": {
			env:  map[string]string{WorkspaceBaseRefEnvKey: "origin/main", WorkspaceBaseSHAEnvKey: strings.ToUpper(sha)},
			want: WorkspaceBaseline{Ref: "origin/main", SHA: sha},
		},
		"no ref":          {env: map[string]string{WorkspaceBaseSHAEnvKey: sha}},
		"no commit":       {env: map[string]string{WorkspaceBaseRefEnvKey: "origin/main"}},
		"a local branch":  {env: map[string]string{WorkspaceBaseRefEnvKey: "main", WorkspaceBaseSHAEnvKey: sha}},
		"a parent escape": {env: map[string]string{WorkspaceBaseRefEnvKey: "origin/../../evil", WorkspaceBaseSHAEnvKey: sha}},
		"a git option":    {env: map[string]string{WorkspaceBaseRefEnvKey: "--upload-pack=evil/x", WorkspaceBaseSHAEnvKey: sha}},
		"a lock file":     {env: map[string]string{WorkspaceBaseRefEnvKey: "origin/main.lock", WorkspaceBaseSHAEnvKey: sha}},
		"a revision, not a commit": {
			env: map[string]string{WorkspaceBaseRefEnvKey: "origin/main", WorkspaceBaseSHAEnvKey: "HEAD~1"},
		},
		"nothing at all": {env: nil},
	} {
		t.Run(name, func(t *testing.T) {
			if got := WorkspaceBaselineFromEnv(tc.env); got != tc.want {
				t.Fatalf("WorkspaceBaselineFromEnv = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestWorkspaceBaselineEnv_RoundTripsThroughATrigger(t *testing.T) {
	baseline := WorkspaceBaseline{Ref: "origin/release-2.x", SHA: strings.Repeat("b", 40)}
	env := baseline.Env()
	if env[WorkspaceBaseRefEnvKey] != baseline.Ref || env[WorkspaceBaseSHAEnvKey] != baseline.SHA {
		t.Fatalf("Env = %v, want the baseline %+v", env, baseline)
	}
	if got := WorkspaceBaselineFromEnv(env); got != baseline {
		t.Fatalf("round trip = %+v, want %+v", got, baseline)
	}
	if env := (WorkspaceBaseline{Ref: "origin/main"}).Env(); env != nil {
		t.Fatalf("Env of an incomplete baseline = %v, want nil", env)
	}
}

func workspaceSnapshotCommit(t *testing.T, bareRepo, baseSHA, addPath, addContent string) string {
	t.Helper()
	index := filepath.Join(t.TempDir(), "index")
	run := func(env []string, args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", bareRepo}, args...)...)
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	indexEnv := []string{"GIT_INDEX_FILE=" + index}
	run(indexEnv, "read-tree", baseSHA)
	writeBlob := exec.Command("git", "-C", bareRepo, "hash-object", "-w", "--stdin")
	writeBlob.Stdin = strings.NewReader(addContent)
	blobOut, err := writeBlob.Output()
	if err != nil {
		t.Fatalf("hash-object: %v", err)
	}
	blob := strings.TrimSpace(string(blobOut))
	run(indexEnv, "update-index", "--add", "--cacheinfo", "100644,"+blob+","+addPath)
	tree := run(indexEnv, "write-tree")

	commit := exec.Command("git", "-C", bareRepo, "commit-tree", tree)
	commit.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Sparkwing", "GIT_AUTHOR_EMAIL=workspace@sparkwing.dev", "GIT_AUTHOR_DATE=2000-01-01T00:00:00Z",
		"GIT_COMMITTER_NAME=Sparkwing", "GIT_COMMITTER_EMAIL=workspace@sparkwing.dev", "GIT_COMMITTER_DATE=2000-01-01T00:00:00Z",
	)
	commit.Stdin = strings.NewReader("sparkwing working-tree snapshot\n")
	out, err := commit.CombinedOutput()
	if err != nil {
		t.Fatalf("commit workspace tree: %v: %s", err, out)
	}
	sha := strings.TrimSpace(string(out))
	run(nil, "update-ref", "refs/heads/workspace", sha)
	return sha
}

func TestAdoptWorkspaceBaselineUsesTheVerifiedDirectCheckout(t *testing.T) {
	parent := t.TempDir()
	baseSHA, tipSHA := makeBareRepoWithSparkwing(t, parent, "widgets", "main")
	bare := filepath.Join(parent, "widgets.git")
	snapshotSHA := workspaceSnapshotCommit(t, bare, tipSHA, "added.txt", "added\n")
	checkout := filepath.Join(t.TempDir(), "snapshot")
	baselineDir := filepath.Join(t.TempDir(), "baseline")
	for _, dest := range []string{checkout, baselineDir} {
		if out, err := exec.Command("git", "clone", "--quiet", bare, dest).CombinedOutput(); err != nil {
			t.Fatalf("clone: %v: %s", err, out)
		}
	}
	for dir, sha := range map[string]string{checkout: snapshotSHA, baselineDir: baseSHA} {
		if out, err := exec.Command("git", "-C", dir, "checkout", "--quiet", "--detach", sha).CombinedOutput(); err != nil {
			t.Fatalf("checkout %s: %v: %s", sha, err, out)
		}
	}
	baseline := WorkspaceBaseline{Ref: "origin/main", SHA: baseSHA}
	if err := adoptBaselineFromCheckout(t.Context(), checkout, baselineDir, snapshotSHA, baseline); err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, checkout, "merge-base", "origin/main", "HEAD"); got != baseSHA {
		t.Fatalf("merge-base = %s, want %s", got, baseSHA)
	}
	if got := gitOut(t, checkout, "rev-parse", "HEAD"); got != snapshotSHA {
		t.Fatalf("HEAD = %s, want snapshot %s", got, snapshotSHA)
	}
	if got := gitOut(t, checkout, "diff", "--name-only", baseSHA); !strings.Contains(got, "added.txt") {
		t.Fatalf("diff against baseline = %q", got)
	}
}

func TestAdoptWorkspaceBaselineRefusesWrongDirectCheckout(t *testing.T) {
	parent := t.TempDir()
	baseSHA, tipSHA := makeBareRepoWithSparkwing(t, parent, "widgets", "main")
	bare := filepath.Join(parent, "widgets.git")
	checkout := filepath.Join(t.TempDir(), "snapshot")
	baselineDir := filepath.Join(t.TempDir(), "baseline")
	for _, dest := range []string{checkout, baselineDir} {
		if out, err := exec.Command("git", "clone", "--quiet", bare, dest).CombinedOutput(); err != nil {
			t.Fatalf("clone: %v: %s", err, out)
		}
	}
	if err := adoptBaselineFromCheckout(t.Context(), checkout, baselineDir, tipSHA,
		WorkspaceBaseline{Ref: "origin/main", SHA: baseSHA}); err == nil {
		t.Fatal("a checkout at a different commit was accepted as the baseline")
	}
}

func TestAdoptWorkspaceBaselineBeforePipelineWasAdded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	bare := filepath.Join(t.TempDir(), "repository.git")
	if out, err := exec.Command("git", "init", "--bare", "--quiet", bare).CombinedOutput(); err != nil {
		t.Fatalf("init: %v: %s", err, out)
	}
	emptyTree := gitOut(t, bare, "mktree")
	baseSHA := workspaceSnapshotCommit(t, bare, emptyTree, "README.md", "baseline\n")
	snapshotSHA := workspaceSnapshotCommit(t, bare, baseSHA, ".sparkwing/pipeline.go", "package main\n")
	checkout := filepath.Join(t.TempDir(), "snapshot")
	if out, err := exec.Command("git", "clone", "--quiet", "--branch", "workspace", bare, checkout).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v: %s", err, out)
	}
	baselineDir := filepath.Join(t.TempDir(), "baseline")
	if err := directCheckout(t.Context(), bare, "", baseSHA, baselineDir, directOptions{protocols: "file"}); err != nil {
		t.Fatal(err)
	}
	baseline := WorkspaceBaseline{Ref: "origin/main", SHA: baseSHA}
	if err := adoptBaselineFromCheckout(t.Context(), checkout, baselineDir, snapshotSHA, baseline); err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, checkout, "merge-base", "origin/main", "HEAD"); got != baseSHA {
		t.Fatalf("merge-base = %s, want %s", got, baseSHA)
	}
}
