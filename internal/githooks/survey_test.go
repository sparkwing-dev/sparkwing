package githooks_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/githooks"
)

// checkout builds a repository whose hook directory holds the named managed
// gates. A gate runs a pipeline; anything else sparkwing writes is a
// forwarder and does not count as one.
func checkout(t *testing.T, gates ...string) (repoRoot, hooksDir string) {
	t.Helper()
	repoRoot = t.TempDir()
	hooksDir = filepath.Join(repoRoot, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range gates {
		body := "#!/bin/sh\n# " + githooks.Marker + "\nsparkwing run gate\n"
		if err := os.WriteFile(filepath.Join(hooksDir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return repoRoot, hooksDir
}

func TestSurvey_ArmedWhenGitReadsTheDirectoryTheGatesAreIn(t *testing.T) {
	repo, hooks := checkout(t, "pre-commit")
	got := githooks.Survey(stubGit(hooks, ""), repo, []string{"pre-commit"})
	if got.State != githooks.GateArmed {
		t.Errorf("State = %s, want %s", got.State, githooks.GateArmed)
	}
	if !got.Gated() {
		t.Error("Gated() = false, want true")
	}
	if len(got.Missing) != 0 {
		t.Errorf("Missing = %v, want none", got.Missing)
	}
}

func TestSurvey_ArmedWhenNoHooksPathOverrideIsSetAtAll(t *testing.T) {
	repo, _ := checkout(t, "pre-commit")
	got := githooks.Survey(stubGit("", ""), repo, []string{"pre-commit"})
	if got.State != githooks.GateArmed {
		t.Errorf("State = %s, want %s", got.State, githooks.GateArmed)
	}
	if got.ActiveDir != "" {
		t.Errorf("ActiveDir = %s, want empty when git reads the repo's own directory", got.ActiveDir)
	}
}

func TestSurvey_ShadowedWhenAGlobalHooksPathRedirectsGit(t *testing.T) {
	repo, hooks := checkout(t, "pre-commit")
	elsewhere := t.TempDir()
	got := githooks.Survey(stubGit("", elsewhere), repo, []string{"pre-commit"})
	if got.State != githooks.GateShadowed {
		t.Errorf("State = %s, want %s", got.State, githooks.GateShadowed)
	}
	if got.Gated() {
		t.Error("Gated() = true, want false: the gate is installed but never runs")
	}
	if len(got.Shadowed) != 1 || got.Shadowed[0] != "pre-commit" {
		t.Errorf("Shadowed = %v, want [pre-commit]", got.Shadowed)
	}
	if len(got.NotFiring()) != 1 || got.NotFiring()[0] != "pre-commit" {
		t.Errorf("NotFiring() = %v, want [pre-commit]", got.NotFiring())
	}
	if got.Scope != "global" || got.ActiveDir != elsewhere {
		t.Errorf("redirect = %s (%s), want %s (global)", got.ActiveDir, got.Scope, elsewhere)
	}
	if len(got.Installed) != 1 || got.Installed[0] != "pre-commit" {
		t.Errorf("Installed = %v, want the gate to still be reported as present in %s", got.Installed, hooks)
	}
	if len(got.Firing) != 0 {
		t.Errorf("Firing = %v, want none", got.Firing)
	}
}

func TestSurvey_UninstalledWhenAnUnwrittenHookIsDeclared(t *testing.T) {
	repo, _ := checkout(t)
	got := githooks.Survey(stubGit("", ""), repo, []string{"pre-commit"})
	if got.State != githooks.GateUninstalled {
		t.Errorf("State = %s, want %s", got.State, githooks.GateUninstalled)
	}
	if len(got.Missing) != 1 || got.Missing[0] != "pre-commit" {
		t.Errorf("Missing = %v, want [pre-commit]", got.Missing)
	}
}

// A repository can hold one gate and be missing another; the verdict follows
// the hook that does not fire, not the one that does.
func TestSurvey_UninstalledWhenOnlySomeDeclaredHooksWereWritten(t *testing.T) {
	repo, _ := checkout(t, "pre-push")
	got := githooks.Survey(stubGit("", ""), repo, []string{"pre-commit", "pre-push"})
	if got.State != githooks.GateUninstalled {
		t.Errorf("State = %s, want %s", got.State, githooks.GateUninstalled)
	}
	if len(got.Missing) != 1 || got.Missing[0] != "pre-commit" {
		t.Errorf("Missing = %v, want [pre-commit]", got.Missing)
	}
}

// A push gate that does not fire leaves the pushes unchecked, so the
// repository is ungated however healthy its commit gate is.
func TestSurvey_UngatedWhenOnlyTheDeclaredPushGateIsMissing(t *testing.T) {
	repo, _ := checkout(t, "pre-commit")
	got := githooks.Survey(stubGit("", ""), repo, []string{"pre-commit", "pre-push"})
	if got.Gated() {
		t.Error("Gated() = true, want false: a push in this repo runs no gate")
	}
}

// A post-commit hook cannot refuse work, so a missing one costs a
// notification rather than a gate -- reporting the repository as ungated over
// it would bury the ones whose commits really do go unchecked.
func TestSurvey_GatedWhenOnlyTheDeclaredPostCommitHookIsMissing(t *testing.T) {
	repo, _ := checkout(t, "pre-commit")
	got := githooks.Survey(stubGit("", ""), repo, []string{"post-commit", "pre-commit"})
	if got.State != githooks.GateUninstalled {
		t.Errorf("State = %s, want %s", got.State, githooks.GateUninstalled)
	}
	if !got.Gated() {
		t.Error("Gated() = false, want true: the commit gate fires, only the notifier is missing")
	}
}

func TestSurvey_UndeclaredWhenNoPipelineAsksForAHook(t *testing.T) {
	repo, _ := checkout(t)
	got := githooks.Survey(stubGit("", ""), repo, nil)
	if got.State != githooks.GateUndeclared {
		t.Errorf("State = %s, want %s", got.State, githooks.GateUndeclared)
	}
	if !got.Gated() {
		t.Error("Gated() = false, want true: a repo that asks for no gate is not ungated")
	}
}

// A hooks path pointing at a directory that does carry the gate is how the
// install arms a repository, so it must read as armed rather than redirected.
func TestSurvey_ArmedWhenTheOverridePointsAtTheGatesThemselves(t *testing.T) {
	repo, hooks := checkout(t, "pre-commit")
	got := githooks.Survey(stubGit(hooks, filepath.Join(t.TempDir(), "global")), repo, []string{"pre-commit"})
	if got.State != githooks.GateArmed {
		t.Errorf("State = %s, want %s", got.State, githooks.GateArmed)
	}
}

func TestSurvey_MissingCheckoutReportsUndeclaredRatherThanPanicking(t *testing.T) {
	got := githooks.Survey(stubGit("", ""), filepath.Join(t.TempDir(), "absent"), []string{"pre-commit"})
	if got.State != githooks.GateUndeclared {
		t.Errorf("State = %s, want %s", got.State, githooks.GateUndeclared)
	}
}

func TestSurveyFleet_SortsRowsByRepoPath(t *testing.T) {
	root := t.TempDir()
	var roots []string
	for _, name := range []string{"zeta", "alpha", "mid"} {
		repo := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Join(repo, ".git", "hooks"), 0o755); err != nil {
			t.Fatal(err)
		}
		roots = append(roots, repo)
	}
	rows := githooks.SurveyFleet(stubGit("", ""), roots, func(string) []string { return nil })
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	for i, want := range []string{"alpha", "mid", "zeta"} {
		if filepath.Base(rows[i].Repo) != want {
			t.Errorf("rows[%d] = %s, want %s", i, filepath.Base(rows[i].Repo), want)
		}
	}
}

func TestSurveyFleet_AsksForEachRepositorysOwnDeclaredHooks(t *testing.T) {
	armed, armedHooks := checkout(t, "pre-commit")
	bare, _ := checkout(t)
	rows := githooks.SurveyFleet(stubGit("", ""), []string{armed, bare}, func(root string) []string {
		if root == bare {
			return []string{"pre-push"}
		}
		return []string{"pre-commit"}
	})
	byRepo := map[string]githooks.RepoGates{}
	for _, r := range rows {
		byRepo[r.Repo] = r
	}
	if got := byRepo[armed].State; got != githooks.GateArmed {
		t.Errorf("%s state = %s, want %s (gates in %s)", armed, got, githooks.GateArmed, armedHooks)
	}
	if got := byRepo[bare].State; got != githooks.GateUninstalled {
		t.Errorf("%s state = %s, want %s", bare, got, githooks.GateUninstalled)
	}
}

func TestSurveyFleet_AsksGitForTheMachineWideConfigOnce(t *testing.T) {
	root := t.TempDir()
	var roots []string
	for _, name := range []string{"one", "two", "three"} {
		repo := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Join(repo, ".git", "hooks"), 0o755); err != nil {
			t.Fatal(err)
		}
		roots = append(roots, repo)
	}
	globalLookups := 0
	counting := func(dir string, args ...string) (string, error) {
		for _, a := range args {
			if a == "--global" {
				globalLookups++
			}
		}
		return stubGit("", "")(dir, args...)
	}
	githooks.SurveyFleet(counting, roots, func(string) []string { return []string{"pre-commit"} })
	if globalLookups != 1 {
		t.Errorf("global config lookups = %d, want 1 across %d repos", globalLookups, len(roots))
	}
}

func TestUngated_DropsTheRowsNoGateIsMissingFrom(t *testing.T) {
	rows := []githooks.RepoGates{
		{Repo: "armed", State: githooks.GateArmed},
		{Repo: "quiet", State: githooks.GateUndeclared},
		{Repo: "notifier", State: githooks.GateUninstalled, Missing: []string{"post-commit"}},
		{Repo: "shadowed", State: githooks.GateShadowed, Shadowed: []string{"pre-commit"}},
		{Repo: "bare", State: githooks.GateUninstalled, Missing: []string{"pre-push"}},
		{Repo: "borrowed", State: githooks.GateBorrowed, Borrowed: []string{"pre-commit"}},
	}
	got := githooks.Ungated(rows)
	if len(got) != 3 || got[0].Repo != "shadowed" || got[1].Repo != "bare" || got[2].Repo != "borrowed" {
		t.Errorf("Ungated = %+v, want the shadowed, uninstalled and borrowed rows", got)
	}
}

// The report contradicted itself for every shadowed repo: the hook sat in both
// installed and missing, and a reader who resolved that the wrong way saw a
// repo with nothing to fix.
func TestSurvey_NeverReportsOneHookAsBothInstalledAndMissing(t *testing.T) {
	repo, _ := checkout(t, "pre-commit", "pre-push")
	got := githooks.Survey(stubGit("", t.TempDir()), repo, []string{"pre-commit", "pre-push"})
	installed := map[string]bool{}
	for _, n := range got.Installed {
		installed[n] = true
	}
	for _, n := range got.Missing {
		if installed[n] {
			t.Errorf("%s is reported as both installed and missing: %+v", n, got)
		}
	}
	if len(got.Shadowed) != 2 {
		t.Errorf("Shadowed = %v, want both declared hooks", got.Shadowed)
	}
	if len(got.Missing) != 0 {
		t.Errorf("Missing = %v, want none: both hooks exist, they just do not run", got.Missing)
	}
}

// A repo that declares two hooks, holds one and never wrote the other has one
// of each, and the two reasons need separate lists to stay actionable.
func TestSurvey_SeparatesAShadowedHookFromAnUnwrittenOne(t *testing.T) {
	repo, _ := checkout(t, "pre-commit")
	got := githooks.Survey(stubGit("", t.TempDir()), repo, []string{"pre-commit", "pre-push"})
	if len(got.Shadowed) != 1 || got.Shadowed[0] != "pre-commit" {
		t.Errorf("Shadowed = %v, want [pre-commit]", got.Shadowed)
	}
	if len(got.Missing) != 1 || got.Missing[0] != "pre-push" {
		t.Errorf("Missing = %v, want [pre-push]", got.Missing)
	}
	if got.State != githooks.GateUninstalled {
		t.Errorf("State = %s, want %s", got.State, githooks.GateUninstalled)
	}
}

// The state that read as "no gate" while the gate was armed and blocking: a
// core.hooksPath pointing at a sibling repo's hooks.
func TestSurvey_BorrowedWhenTheGateThatFiresLivesInAnotherRepo(t *testing.T) {
	repo, _ := checkout(t)
	sibling, siblingHooks := checkout(t, "pre-commit")
	got := githooks.Survey(stubGit(siblingHooks, ""), repo, []string{"pre-commit"})
	if got.State != githooks.GateBorrowed {
		t.Errorf("State = %s, want %s (hooks in %s)", got.State, githooks.GateBorrowed, sibling)
	}
	if len(got.Borrowed) != 1 || got.Borrowed[0] != "pre-commit" {
		t.Errorf("Borrowed = %v, want [pre-commit]", got.Borrowed)
	}
	if len(got.Missing) != 0 {
		t.Errorf("Missing = %v, want none: a gate does fire, it is just not this repo's", got.Missing)
	}
	if got.Gated() {
		t.Error("Gated() = true, want false: nothing here declares or keeps the gate that fires")
	}
}

// A borrowed gate is only fixable once the repo's own override is cleared:
// install treats a repo-scoped core.hooksPath as deliberate and leaves it be,
// so the install on its own changes nothing.
func TestRepoGatesRemedy_ClearsTheOverrideBeforeInstallingForABorrowedGate(t *testing.T) {
	r := githooks.RepoGates{Repo: "/code/sparkwing-platform", Borrowed: []string{"pre-commit"}, State: githooks.GateBorrowed}
	got := r.Remedy()
	for _, want := range []string{"--unset core.hooksPath", "hooks install --repo /code/sparkwing-platform"} {
		if !strings.Contains(got, want) {
			t.Errorf("Remedy = %q, want it to say %q", got, want)
		}
	}
}

// The one-word state cannot carry a repo with two different problems, so the
// summary names every hook the repo does not run its own gate for.
func TestRepoGatesSummary_NamesTheBorrowedGateAndTheMissingOneTogether(t *testing.T) {
	r := githooks.RepoGates{
		Repo:      "/code/sparkwing-platform",
		ActiveDir: "/code/sparkwing/.git/hooks",
		Borrowed:  []string{"pre-commit"},
		Missing:   []string{"pre-push"},
		State:     githooks.GateBorrowed,
	}
	got := r.Summary()
	for _, want := range []string{"pre-commit", "/code/sparkwing/.git/hooks", "pre-push", "no gate is installed"} {
		if !strings.Contains(got, want) {
			t.Errorf("Summary = %q, want it to name %q", got, want)
		}
	}
}

func TestRepoGatesSummary_NamesTheRedirectForAShadowedGate(t *testing.T) {
	r := githooks.RepoGates{
		Repo:      "/code/overwing",
		ActiveDir: "/config/git/hooks",
		Scope:     "global",
		Shadowed:  []string{"pre-commit"},
		State:     githooks.GateShadowed,
	}
	got := r.Summary()
	for _, want := range []string{"/code/overwing", "pre-commit", "/config/git/hooks", "global"} {
		if !strings.Contains(got, want) {
			t.Errorf("Summary = %q, want it to name %q", got, want)
		}
	}
}

func TestRepoGatesSummary_SaysNothingIsInstalledWhenNothingIs(t *testing.T) {
	r := githooks.RepoGates{Repo: "/code/pulsewing", Missing: []string{"pre-commit"}, State: githooks.GateUninstalled}
	if got := r.Summary(); !strings.Contains(got, "no gate is installed") {
		t.Errorf("Summary = %q, want it to say no gate is installed", got)
	}
}
