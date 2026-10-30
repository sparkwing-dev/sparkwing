package githooks

import (
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
	for _, want := range []string{"pre-commit", "/code/sparkwing/.git/hooks", "pre-push", "the hooks are missing or not current"} {
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
	if got := r.Summary(); !strings.Contains(got, "the hooks are missing or not current") {
		t.Errorf("Summary = %q, want it to say the hooks are missing or not current", got)
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
