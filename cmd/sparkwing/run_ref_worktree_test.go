package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func revParse(t *testing.T, repo, ref string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "rev-parse", ref).Output()
	if err != nil {
		t.Fatalf("rev-parse %s: %v", ref, err)
	}
	return strings.TrimSpace(string(out))
}

func submittedTrigger(t *testing.T, st *store.Store, result submitResult) *store.Trigger {
	t.Helper()
	trig, err := st.GetTrigger(t.Context(), result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	return trig
}

func TestRefWorktreeSubmissionExecutesInTheNamedWorktree(t *testing.T) {
	repo := pipelineRefRepo(t)
	paths, st := pipelineRefStore(t)
	if out, err := exec.Command("git", "-C", repo, "remote", "add", "origin", "https://github.com/acme/widgets.git").CombinedOutput(); err != nil {
		t.Fatalf("add remote: %v: %s", err, out)
	}
	result, err := persistSubmission(t.Context(), st, paths, submission{
		Pipeline: "build", RepoDir: repo, Ref: "feature", RefWorktree: "checks",
		Gate: func(string, string) error { return nil },
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	trig := submittedTrigger(t, st, result)
	named, err := orchestrator.ResolveNamedRefWorktree(t.Context(), paths, repo, "checks")
	if err != nil {
		t.Fatal(err)
	}
	feature := revParse(t, repo, "feature")

	if got := trig.TriggerEnv[orchestrator.SubmitRepoDirKey]; got != named.Dir {
		t.Errorf("the run executes in %q, want the named worktree %q", got, named.Dir)
	}
	if got := trig.TriggerEnv[orchestrator.NamedRefWorktreeRepoKey]; got != named.Repo {
		t.Errorf("recorded repository %q, want %q", got, named.Repo)
	}
	if got := trig.TriggerEnv[orchestrator.RefWorktreeRevKey]; got != feature {
		t.Errorf("recorded commit %q, want feature's %q", got, feature)
	}
	if trig.GitSHA != feature || trig.GitBranch != "" {
		t.Errorf("run reports %s on %q, want %s on no branch, as any --sw-ref run", trig.GitSHA, trig.GitBranch, feature)
	}
	if _, _, slug, url := gitContextIn(repo); trig.Repo != slug || trig.RepoURL != url || slug == "" {
		t.Errorf("run names repository %q at %q, want the submitting checkout's %q at %q", trig.Repo, trig.RepoURL, slug, url)
	}
}

func TestRefWorktreeGatesTheCommitInATreeOfItsOwn(t *testing.T) {
	cases := map[string]struct {
		ref   string
		dirty bool
	}{
		"checkout at the commit":         {ref: "feature"},
		"checkout at another commit":     {ref: "main"},
		"checkout with uncommitted work": {ref: "feature", dirty: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			repo := pipelineRefRepo(t)
			paths, st := pipelineRefStore(t)
			if tc.dirty {
				if err := os.WriteFile(filepath.Join(repo, ".sparkwing", "which"), []byte("edited"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var gated, which string
			_, err := persistSubmission(t.Context(), st, paths, submission{
				Pipeline: "build", RepoDir: repo, Ref: tc.ref, RefWorktree: "ci",
				Gate: func(dir, _ string) error {
					gated = dir
					raw, rerr := os.ReadFile(filepath.Join(dir, ".sparkwing", "which"))
					which = string(raw)
					return rerr
				},
			})
			if err != nil {
				t.Fatalf("submit: %v", err)
			}
			if gated == repo {
				t.Fatal("the risk gate weighed the submitting checkout rather than the commit")
			}
			if want := "pipeline-" + tc.ref; which != want {
				t.Errorf("the risk gate read %q, want the commit's %q", which, want)
			}
			if _, err := os.Stat(gated); !os.IsNotExist(err) {
				t.Errorf("the tree checked out only to gate the commit was left behind: %v", err)
			}
		})
	}
}

func TestRefWorktreeCombinesWithPipelineRef(t *testing.T) {
	repo := pipelineRefRepo(t)
	paths, st := pipelineRefStore(t)
	var gated string
	result, err := persistSubmission(t.Context(), st, paths, submission{
		Pipeline: "build", RepoDir: repo, Ref: "feature", PipelineRef: "main", RefWorktree: "ci",
		Gate: func(dir, _ string) error { gated = dir; return nil },
	})
	if err != nil {
		t.Fatalf("submit naming a run ref, a pipeline ref and a worktree: %v", err)
	}
	trig := submittedTrigger(t, st, result)
	named, err := orchestrator.ResolveNamedRefWorktree(t.Context(), paths, repo, "ci")
	if err != nil {
		t.Fatal(err)
	}

	if got := trig.TriggerEnv[orchestrator.SubmitRepoDirKey]; got != named.Dir {
		t.Errorf("the run executes in %q, want the named worktree %q", got, named.Dir)
	}
	compileTree := trig.TriggerEnv[orchestrator.PipelineDirKey]
	which, err := os.ReadFile(filepath.Join(compileTree, ".sparkwing", "which"))
	if err != nil || string(which) != "pipeline-main" {
		t.Errorf("the pipeline compiles from %q holding %q (%v), want main's pipeline", compileTree, which, err)
	}
	if gated != compileTree {
		t.Errorf("the risk gate weighed %q, want the tree the pipeline compiles from %q", gated, compileTree)
	}
	if got := trig.TriggerEnv[orchestrator.RefWorktreeRevKey]; got != revParse(t, repo, "feature") {
		t.Errorf("recorded commit %q, want feature's", got)
	}
}

func TestRefWorktreeRequiresARunRef(t *testing.T) {
	repo := pipelineRefRepo(t)
	paths, st := pipelineRefStore(t)
	_, err := persistSubmission(t.Context(), st, paths, submission{
		Pipeline: "build", RepoDir: repo, RefWorktree: "checks",
		Gate: func(string, string) error { return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "--sw-ref") {
		t.Fatalf("a worktree with no commit to put in it = %v, want a refusal naming --sw-ref", err)
	}
}

func TestRefWorktreeFlagIsDetachedOnly(t *testing.T) {
	for _, args := range [][]string{{"--sw-ref-worktree", "checks"}, {"--sw-ref-worktree=checks"}} {
		flags, rest := parseRunFlags(args)
		if flags.refWorktree != "checks" || len(rest) != 0 {
			t.Errorf("parse %v = %q with %v left over, want checks and nothing left", args, flags.refWorktree, rest)
		}
		if err := refuseDetachedOnlyFlags(flags); err == nil {
			t.Errorf("a foreground run accepted %v", args)
		}
	}
}

func TestRefWorktreeRefusesANameThatIsNotOneDirectory(t *testing.T) {
	repo := pipelineRefRepo(t)
	paths, st := pipelineRefStore(t)
	_, err := persistSubmission(t.Context(), st, paths, submission{
		Pipeline: "build", RepoDir: repo, Ref: "feature", RefWorktree: "../x",
		Gate: func(string, string) error { return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "--sw-ref-worktree") {
		t.Fatalf("worktree name ../x = %v, want a refusal naming --sw-ref-worktree", err)
	}
	if triggers, lerr := st.ListTriggers(t.Context(), store.TriggerFilter{}); lerr != nil || len(triggers) != 0 {
		t.Errorf("a refused submission stored %d triggers (%v)", len(triggers), lerr)
	}
}

func TestRunDetachedSendsTheRunToItsNamedWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: compiles the fixture pipeline; the fast class runs under -short")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the detached-consumer contract is exercised on POSIX process semantics")
	}
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_CONFIG", filepath.Join(home, "config.yaml"))
	repoDir := detachedDaemonRepo(t)
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"-c", "user.email=t@example.com", "-c", "user.name=t", "add", "-A"},
		{"-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "--quiet", "-m", "fixture"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repoDir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	prevWarm, prevConsumer := ensureRunDaemonFn, ensureTriggerConsumerFn
	t.Cleanup(func() { ensureRunDaemonFn, ensureTriggerConsumerFn = prevWarm, prevConsumer })
	ensureRunDaemonFn = func() {}
	ensureTriggerConsumerFn = func(string, time.Duration, time.Duration) error { return nil }

	t.Chdir(repoDir)
	if err := runDetached(t.Context(), "warmfixture",
		runFlags{detached: true, outputFormat: "json", ref: "HEAD", refWorktree: " ci "}, nil); err != nil {
		t.Fatalf("runDetached: %v", err)
	}
	paths := orchestrator.PathsAt(home)
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	triggers, err := st.ListTriggers(t.Context(), store.TriggerFilter{})
	if err != nil || len(triggers) != 1 {
		t.Fatalf("stored triggers = %d (%v), want 1", len(triggers), err)
	}
	named, err := orchestrator.ResolveNamedRefWorktree(t.Context(), paths, repoDir, "ci")
	if err != nil {
		t.Fatal(err)
	}
	if got := triggers[0].TriggerEnv[orchestrator.SubmitRepoDirKey]; got != named.Dir {
		t.Errorf("--sw-ref-worktree ci runs in %q, want the named worktree %q", got, named.Dir)
	}
}
