package githooks

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func checkout(t *testing.T, gates ...string) (repoRoot, hooksDir string) {
	t.Helper()
	repoRoot = t.TempDir()
	hooksDir = filepath.Join(repoRoot, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range gates {
		body := "#!/bin/sh\n# " + Marker + " " + Format + "\nsparkwing run gate\n"
		if err := os.WriteFile(filepath.Join(hooksDir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return repoRoot, hooksDir
}

func TestSurvey_ArmedWhenGitReadsTheDirectoryTheGatesAreIn(t *testing.T) {
	repo, hooks := checkout(t, "pre-commit")
	got := survey(stubGit(hooks, ""), repo, []string{"pre-commit"})
	if got.State != GateArmed {
		t.Errorf("State = %s, want %s", got.State, GateArmed)
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
	got := survey(stubGit("", ""), repo, []string{"pre-commit"})
	if got.State != GateArmed {
		t.Errorf("State = %s, want %s", got.State, GateArmed)
	}
	if got.ActiveDir != "" {
		t.Errorf("ActiveDir = %s, want empty when git reads the repo's own directory", got.ActiveDir)
	}
}

func TestSurvey_ShadowedWhenAGlobalHooksPathRedirectsGit(t *testing.T) {
	repo, hooks := checkout(t, "pre-commit")
	elsewhere := t.TempDir()
	got := survey(stubGit("", elsewhere), repo, []string{"pre-commit"})
	if got.State != GateShadowed {
		t.Errorf("State = %s, want %s", got.State, GateShadowed)
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
	if got.Scope != "global" || got.ActiveDir != filepath.Clean(elsewhere) {
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
	got := survey(stubGit("", ""), repo, []string{"pre-commit"})
	if got.State != GateBroken {
		t.Errorf("State = %s, want %s", got.State, GateBroken)
	}
	if len(got.Missing) != 1 || got.Missing[0] != "pre-commit" {
		t.Errorf("Missing = %v, want [pre-commit]", got.Missing)
	}
}

func TestSurvey_UninstalledWhenOnlySomeDeclaredHooksWereWritten(t *testing.T) {
	repo, _ := checkout(t, "pre-push")
	got := survey(stubGit("", ""), repo, []string{"pre-commit", "pre-push"})
	if got.State != GateBroken {
		t.Errorf("State = %s, want %s", got.State, GateBroken)
	}
	if len(got.Missing) != 1 || got.Missing[0] != "pre-commit" {
		t.Errorf("Missing = %v, want [pre-commit]", got.Missing)
	}
}

func TestSurvey_UngatedWhenOnlyTheDeclaredPushGateIsMissing(t *testing.T) {
	repo, _ := checkout(t, "pre-commit")
	got := survey(stubGit("", ""), repo, []string{"pre-commit", "pre-push"})
	if got.Gated() {
		t.Error("Gated() = true, want false: a push in this repo runs no gate")
	}
}

func TestSurvey_GatedWhenOnlyTheDeclaredPostCommitHookIsMissing(t *testing.T) {
	repo, _ := checkout(t, "pre-commit")
	got := survey(stubGit("", ""), repo, []string{"post-commit", "pre-commit"})
	if got.State != GateUninstalled {
		t.Errorf("State = %s, want %s", got.State, GateUninstalled)
	}
	if !got.Gated() {
		t.Error("Gated() = false, want true: the commit gate fires, only the notifier is missing")
	}
}

func TestSurvey_UndeclaredWhenNoPipelineAsksForAHook(t *testing.T) {
	repo, _ := checkout(t)
	got := survey(stubGit("", ""), repo, nil)
	if got.State != gateUndeclared {
		t.Errorf("State = %s, want %s", got.State, gateUndeclared)
	}
	if got.Gated() {
		t.Error("Gated() = true, want false: a repo that declares no gate cannot refuse a commit")
	}
}

func TestSurvey_ArmedWhenTheOverridePointsAtTheGatesThemselves(t *testing.T) {
	repo, hooks := checkout(t, "pre-commit")
	got := survey(stubGit(hooks, filepath.Join(t.TempDir(), "global")), repo, []string{"pre-commit"})
	if got.State != GateArmed {
		t.Errorf("State = %s, want %s", got.State, GateArmed)
	}
}

func TestSurvey_MissingCheckoutReportsBrokenRatherThanPanicking(t *testing.T) {
	got := survey(stubGit("", ""), filepath.Join(t.TempDir(), "absent"), []string{"pre-commit"})
	if got.State != GateBroken {
		t.Errorf("State = %s, want %s", got.State, GateBroken)
	}
	if got.ConfigError == "" {
		t.Error("ConfigError is empty, want the reason the hook directory could not be resolved")
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
	rows := surveyFleet(stubGit("", ""), roots, func(string) ([]string, error) { return nil, nil })
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
	rows := surveyFleet(stubGit("", ""), []string{armed, bare}, func(root string) ([]string, error) {
		if root == bare {
			return []string{"pre-push"}, nil
		}
		return []string{"pre-commit"}, nil
	})
	byRepo := map[string]RepoGates{}
	for _, r := range rows {
		byRepo[r.Repo] = r
	}
	if got := byRepo[armed].State; got != GateArmed {
		t.Errorf("%s state = %s, want %s (gates in %s)", armed, got, GateArmed, armedHooks)
	}
	if got := byRepo[bare].State; got != GateBroken {
		t.Errorf("%s state = %s, want %s", bare, got, GateBroken)
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
	surveyFleet(counting, roots, func(string) ([]string, error) { return []string{"pre-commit"}, nil })
	if globalLookups != 1 {
		t.Errorf("global config lookups = %d, want 1 across %d repos", globalLookups, len(roots))
	}
}

func TestSurveyFleet_MarksARepoWhoseDeclarationCannotBeReadBroken(t *testing.T) {
	armed, _ := checkout(t, "pre-commit")
	rows := surveyFleet(stubGit("", ""), []string{armed}, func(string) ([]string, error) {
		return nil, errors.New("config does not load: bad name")
	})
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want one row", rows)
	}
	if rows[0].State != GateBroken {
		t.Errorf("State = %s, want %s", rows[0].State, GateBroken)
	}
	if rows[0].Gated() {
		t.Error("a repo whose declaration cannot be read counted as gated")
	}
	if !strings.Contains(rows[0].Summary(), "config does not load: bad name") {
		t.Errorf("Summary = %q, want the config error", rows[0].Summary())
	}
}

func TestUngated_DropsTheRowsNoGateIsMissingFrom(t *testing.T) {
	rows := []RepoGates{
		{Repo: "armed", Declared: []string{"pre-commit"}, Hooks: currentHooks("pre-commit"), State: GateArmed},
		{Repo: "quiet", State: gateUndeclared},
		{Repo: "notifier", Declared: []string{"post-commit", "pre-commit"}, Hooks: currentHooks("pre-commit"), State: GateUninstalled, Missing: []string{"post-commit"}},
		{Repo: "shadowed", Declared: []string{"pre-commit"}, State: GateShadowed, Shadowed: []string{"pre-commit"}},
		{Repo: "bare", Declared: []string{"pre-commit", "pre-push"}, State: GateUninstalled, Missing: []string{"pre-push"}},
		{Repo: "borrowed", Declared: []string{"pre-commit"}, State: gateBorrowed, Borrowed: []string{"pre-commit"}},
	}
	got := Ungated(rows)
	want := []string{"quiet", "shadowed", "bare", "borrowed"}
	if len(got) != len(want) {
		t.Fatalf("Ungated = %+v, want the %v rows", got, want)
	}
	for i, name := range want {
		if got[i].Repo != name {
			t.Errorf("Ungated[%d] = %s, want %s", i, got[i].Repo, name)
		}
	}
}

func TestSurvey_NeverReportsOneHookAsBothInstalledAndMissing(t *testing.T) {
	repo, _ := checkout(t, "pre-commit", "pre-push")
	got := survey(stubGit("", t.TempDir()), repo, []string{"pre-commit", "pre-push"})
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

func TestSurvey_SeparatesAShadowedHookFromAnUnwrittenOne(t *testing.T) {
	repo, _ := checkout(t, "pre-commit")
	got := survey(stubGit("", t.TempDir()), repo, []string{"pre-commit", "pre-push"})
	if len(got.Shadowed) != 1 || got.Shadowed[0] != "pre-commit" {
		t.Errorf("Shadowed = %v, want [pre-commit]", got.Shadowed)
	}
	if len(got.Missing) != 1 || got.Missing[0] != "pre-push" {
		t.Errorf("Missing = %v, want [pre-push]", got.Missing)
	}
	if got.State != GateBroken {
		t.Errorf("State = %s, want %s", got.State, GateBroken)
	}
}

func TestSurvey_BorrowedWhenTheGateThatFiresLivesInAnotherRepo(t *testing.T) {
	repo, _ := checkout(t)
	sibling, siblingHooks := checkout(t, "pre-commit")
	got := survey(stubGit(siblingHooks, ""), repo, []string{"pre-commit"})
	if got.State != gateBorrowed {
		t.Errorf("State = %s, want %s (hooks in %s)", got.State, gateBorrowed, sibling)
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

func TestRepoGatesRemedy_ClearsTheOverrideBeforeInstallingForABorrowedGate(t *testing.T) {
	r := RepoGates{
		Repo: "/code/sparkwing-platform", Declared: []string{"pre-commit"},
		Borrowed: []string{"pre-commit"}, State: gateBorrowed,
	}
	got := r.Remedy()
	for _, want := range []string{"--unset core.hooksPath", "sparkwing -C /code/sparkwing-platform pipeline hooks install"} {
		if !strings.Contains(got, want) {
			t.Errorf("Remedy = %q, want it to say %q", got, want)
		}
	}
}

func TestRepoGatesRemedy_ClearsALocalOverrideBeforeInstallingAShadowedGate(t *testing.T) {
	r := RepoGates{
		Repo: "/code/sparkwing", Scope: "local", Declared: []string{"pre-commit"},
		Shadowed: []string{"pre-commit"}, State: GateShadowed,
		Hooks: []HookObservation{{Name: "pre-commit", Declared: true, Required: true}},
	}
	got := r.Remedy()
	for _, want := range []string{"--unset core.hooksPath", "sparkwing -C /code/sparkwing pipeline hooks install"} {
		if !strings.Contains(got, want) {
			t.Errorf("Remedy = %q, want it to say %q", got, want)
		}
	}
}

func TestRepoGatesSummary_NamesTheBorrowedGateAndTheMissingOneTogether(t *testing.T) {
	r := RepoGates{
		Repo:      "/code/sparkwing-platform",
		ActiveDir: "/code/sparkwing/.git/hooks",
		Borrowed:  []string{"pre-commit"},
		Missing:   []string{"pre-push"},
		State:     gateBorrowed,
	}
	got := r.Summary()
	for _, want := range []string{"pre-commit", "/code/sparkwing/.git/hooks", "pre-push", "no gate is installed"} {
		if !strings.Contains(got, want) {
			t.Errorf("Summary = %q, want it to name %q", got, want)
		}
	}
}

func TestRepoGatesSummary_NamesTheRedirectForAShadowedGate(t *testing.T) {
	r := RepoGates{
		Repo:      "/code/overwing",
		ActiveDir: "/config/git/hooks",
		Scope:     "global",
		Shadowed:  []string{"pre-commit"},
		State:     GateShadowed,
	}
	got := r.Summary()
	for _, want := range []string{"/code/overwing", "pre-commit", "/config/git/hooks", "global"} {
		if !strings.Contains(got, want) {
			t.Errorf("Summary = %q, want it to name %q", got, want)
		}
	}
}

func TestRepoGatesSummary_SaysNothingIsInstalledWhenNothingIs(t *testing.T) {
	r := RepoGates{Repo: "/code/pulsewing", Missing: []string{"pre-commit"}, State: GateUninstalled}
	if got := r.Summary(); !strings.Contains(got, "no gate is installed") {
		t.Errorf("Summary = %q, want it to say no gate is installed", got)
	}
}

func TestSurvey_UngatedWhenEveryDeclaredHookIsANotifier(t *testing.T) {
	repo, _ := checkout(t, "post-commit")
	got := survey(stubGit("", ""), repo, []string{"post-commit"})
	if got.State != GateArmed {
		t.Errorf("State = %s, want %s: the hook the repo declares does fire", got.State, GateArmed)
	}
	if got.Gated() {
		t.Error("Gated() = true, want false: a post-commit notifier cannot refuse a commit")
	}
	if got.Fires() == nil || got.Fires()[0] != "post-commit" {
		t.Errorf("Fires() = %v, want [post-commit]", got.Fires())
	}
}

func TestSurvey_FiresNamesOnlyTheDeclaredHooksGitRunsFromThisRepo(t *testing.T) {
	repo, hooks := checkout(t, "pre-commit", "post-commit")
	got := survey(stubGit(hooks, ""), repo, []string{"post-commit", "pre-commit", "pre-push"})
	if want := []string{"post-commit", "pre-commit"}; !slices.Equal(got.Fires(), want) {
		t.Errorf("Fires() = %v, want %v", got.Fires(), want)
	}
}

func TestSurvey_FiresDropsAHookThatOnlyRunsOutOfAnotherRepo(t *testing.T) {
	repo, _ := checkout(t)
	borrowed := filepath.Join(t.TempDir(), "hooks")
	if err := os.MkdirAll(borrowed, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\n# " + Marker + " " + Format + "\nsparkwing run gate\n"
	if err := os.WriteFile(filepath.Join(borrowed, "pre-commit"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	got := survey(stubGit("", borrowed), repo, []string{"pre-commit"})
	if len(got.Fires()) != 0 {
		t.Errorf("Fires() = %v, want none: the gate that runs belongs to another repo", got.Fires())
	}
}

func TestRepoGatesSummary_SaysNothingCanRefuseACommitWhenNoGateIsDeclared(t *testing.T) {
	row := RepoGates{Repo: "/code/toolbox", State: gateUndeclared}
	if !strings.Contains(row.Summary(), "nothing here refuses a commit or a push") {
		t.Errorf("Summary = %q, want it to say nothing here refuses work", row.Summary())
	}
}

func TestRepoGatesSummary_SaysSoWhenTheOnlyDeclaredHookIsANotifier(t *testing.T) {
	row := RepoGates{
		Repo:     "/code/toolbox",
		Declared: []string{"post-commit"},
		Firing:   []string{"post-commit"},
		Hooks:    currentHooks("post-commit"),
		State:    GateArmed,
	}
	got := row.Summary()
	if !strings.Contains(got, "nothing here refuses a commit or a push") {
		t.Errorf("Summary = %q, want it to say nothing here refuses work", got)
	}
	if !strings.Contains(got, "post-commit") {
		t.Errorf("Summary = %q, want it to name the hook that does fire", got)
	}
}

func TestRepoGatesRemedy_AsksForAGateDeclarationRatherThanAnInstall(t *testing.T) {
	row := RepoGates{Repo: "/code/toolbox", State: gateUndeclared}
	got := row.Remedy()
	if !strings.Contains(got, "declare a pre_commit trigger") {
		t.Errorf("Remedy = %q, want it to ask for a declared gate: an install writes nothing here", got)
	}
}

func TestSurvey_GatedWhenTheOnlyDeclaredBlockingHookIsPrePush(t *testing.T) {
	repo, hooks := checkout(t, "pre-push")
	got := survey(stubGit(hooks, ""), repo, []string{"pre-push"})
	if !got.Gated() {
		t.Error("Gated() = false, want true: a pre-push hook refuses a push")
	}
	if strings.Contains(got.Summary(), "no pipeline declares") {
		t.Errorf("Summary = %q, want it not to claim the repo declares no blocking hook", got.Summary())
	}
}

func TestSurvey_UngatedRepoRunningACommitGateIsNotCalledGateless(t *testing.T) {
	repo, hooks := checkout(t, "pre-commit")
	got := survey(stubGit(hooks, ""), repo, []string{"pre-commit", "pre-push"})
	if got.Gated() {
		t.Error("Gated() = true, want false: the declared pre-push is not installed")
	}
	if !got.runsBlockingGate() {
		t.Error("runsBlockingGate() = false, want true: the declared pre-commit fires here")
	}
}

func TestSurvey_AnUnreadableHookDirectoryIsBrokenRatherThanGated(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-a-checkout")
	got := survey(stubGit("", ""), missing, []string{"pre-commit"})
	if got.State != GateBroken {
		t.Errorf("State = %s, want %s", got.State, GateBroken)
	}
	if got.Gated() {
		t.Error("Gated() = true, want false: the survey never read this repository")
	}
}

func testExpectations(declared []string) Expectations {
	return func(Git, string, string) (map[string]ExpectedHook, error) {
		specs := map[string]ExpectedHook{}
		for _, name := range declared {
			specs[name] = ExpectedHook{Content: "#!/bin/sh\n# " + Marker + " " + Format + "\nsparkwing run gate\n", Declared: true}
		}
		return specs, nil
	}
}

func survey(git Git, repo string, declared []string) RepoGates {
	return Survey(git, repo, testExpectations(declared))
}

func surveyFleet(git Git, roots []string, declared func(string) ([]string, error)) []RepoGates {
	return SurveyFleet(git, roots, func(git Git, repo, dir string) (map[string]ExpectedHook, error) {
		names, err := declared(repo)
		if err != nil {
			return nil, err
		}
		return testExpectations(names)(git, repo, dir)
	})
}

func currentHooks(names ...string) []HookObservation {
	var hooks []HookObservation
	for _, name := range names {
		hooks = append(hooks, HookObservation{
			Name: name, Present: true, Managed: true, ContentMatches: true,
			VersionMatches: true, Executable: true, Declared: true, Active: true, Required: name != "post-commit",
		})
	}
	return hooks
}
