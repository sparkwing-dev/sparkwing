package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/githooks"
)

type fakeGit struct {
	global string
	local  string
	writes []string
}

func (f *fakeGit) run(_ string, args ...string) (string, error) {
	if len(args) == 3 && args[0] == "config" && args[1] == "--local" && args[2] == "core.hooksPath" {
		return configValue(f.local)
	}
	if len(args) == 4 && args[0] == "config" && args[3] == "core.hooksPath" {
		switch args[1] {
		case "--global":
			return configValue(f.global)
		case "--local":
			return configValue(f.local)
		}
	}
	if len(args) == 3 && args[0] == "config" && args[1] == "core.hooksPath" {
		f.local = args[2]
		f.writes = append(f.writes, args[2])
		return "", nil
	}
	if len(args) == 3 && args[0] == "config" && args[1] == "--unset" && args[2] == "core.hooksPath" {
		f.local = ""
		f.writes = append(f.writes, "--unset")
		return "", nil
	}
	return "", fmt.Errorf("unexpected git %v", args)
}

func configValue(v string) (string, error) {
	if v == "" {
		return "", errors.New("core.hooksPath unset")
	}
	return v + "\n", nil
}

func gateRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	writeRepoFile(t, filepath.Join(repo, ".sparkwing", "sparkwing.yaml"), `pipelines:
  - name: lint
    entrypoint: Lint
    on:
      pre_commit: {}
  - name: self-install
    entrypoint: SelfInstall
    on:
      post_commit: {}
`)
	if err := os.MkdirAll(filepath.Join(repo, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	return repo
}

func installInto(t *testing.T, git githooks.Git, repo string) string {
	t.Helper()
	var err error
	out := captureStdout(t, func() {
		_, err = installHooks(git, repo, filepath.Join(repo, ".sparkwing"), installOptions{})
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	return out
}

func TestRenderHookScript_BlockingHooksAbortOnFailure(t *testing.T) {
	for _, hook := range []string{"pre-commit", "pre-push"} {
		script := renderHookScript(hook, []string{"lint", "test"}, false, "")
		if !strings.Contains(script, "set -e") {
			t.Errorf("%s hook should set -e so git aborts on failure:\n%s", hook, script)
		}
		if !strings.Contains(script, "sparkwing run lint\n") {
			t.Errorf("%s hook should invoke each pipeline plainly:\n%s", hook, script)
		}
		if strings.Contains(script, "|| true") {
			t.Errorf("%s hook must not swallow pipeline failures:\n%s", hook, script)
		}
	}
}

func TestRenderHookScript_QuietByDefault(t *testing.T) {
	for _, hook := range []string{"pre-commit", "pre-push", "post-commit"} {
		script := renderHookScript(hook, []string{"lint"}, false, "")
		if !strings.Contains(script, `export SPARKWING_LOG_FORMAT="${SPARKWING_LOG_FORMAT:-quiet}"`) {
			t.Errorf("%s hook should default the log format to quiet while honoring an override:\n%s", hook, script)
		}
	}
}

func TestRenderHookScript_PostCommitIsNonBlocking(t *testing.T) {
	script := renderHookScript("post-commit", []string{"self-install", "notify"}, false, "")
	if strings.Contains(script, "set -e") {
		t.Errorf("post-commit hook must not set -e (the commit already landed):\n%s", script)
	}
	for _, p := range []string{"self-install", "notify"} {
		if !strings.Contains(script, "sparkwing run "+p+" || true\n") {
			t.Errorf("post-commit hook should tolerate %q failing and continue:\n%s", p, script)
		}
	}
	if !strings.HasSuffix(script, "exit 0\n") {
		t.Errorf("post-commit hook must always exit zero:\n%s", script)
	}
}

func TestRenderHookScript_ChainRunsThePipelineBeforeTheGlobalHook(t *testing.T) {
	script := renderHookScript("pre-push", []string{"gate"}, true, "")
	run := strings.Index(script, "sparkwing run gate")
	chain := strings.Index(script, `exec "$hook"`)
	if run < 0 || chain < 0 {
		t.Fatalf("chained hook should run the pipeline and then hand off:\n%s", script)
	}
	if run > chain {
		t.Errorf("the gate must run before the hand-off to the global hook:\n%s", script)
	}
	if !strings.Contains(script, `hook="$global/pre-push"`) {
		t.Errorf("chained hook should hand off to the same-named global hook:\n%s", script)
	}
	if !strings.Contains(script, "git config --global --type=path core.hooksPath") {
		t.Errorf("chained hook should resolve the global hooks path when it runs:\n%s", script)
	}
	if !strings.Contains(script, "sparkwing run gate </dev/null") {
		t.Errorf("a chained hook must leave its own stdin for the global hook:\n%s", script)
	}
}

func TestRenderHookScript_ForwarderOnlyChains(t *testing.T) {
	script := renderHookScript("prepare-commit-msg", nil, true, "")
	if strings.Contains(script, "sparkwing run") {
		t.Errorf("a forwarder runs no pipeline:\n%s", script)
	}
	if !strings.HasSuffix(script, `exec "$hook" "$@"`+"\n") {
		t.Errorf("a forwarder should end by handing off with the original arguments:\n%s", script)
	}
	if !strings.Contains(script, sparkwingHookMarker) {
		t.Errorf("a forwarder must be marked so uninstall can reclaim it:\n%s", script)
	}
}

func TestRenderHookScript_BlockingGatesCarryTheSelfTestGuard(t *testing.T) {
	for _, hook := range []string{"pre-commit", "pre-push"} {
		script := renderHookScript(hook, []string{"gate"}, false, "")
		if !githooks.CarriesSelfTest(script) {
			t.Errorf("%s hook cannot be asked to refuse:\n%s", hook, script)
		}
		guard := strings.Index(script, githooks.SelfTestEnv)
		run := strings.Index(script, "sparkwing run gate")
		if guard > run {
			t.Errorf("the guard must exit before the gate runs, or the check costs a full gate:\n%s", script)
		}
	}
}

func TestRenderHookScript_NothingButABlockingGateCarriesTheGuard(t *testing.T) {
	for name, script := range map[string]string{
		"post-commit": renderHookScript("post-commit", []string{"notify"}, false, ""),
		"forwarder":   renderHookScript("prepare-commit-msg", nil, true, ""),
	} {
		if githooks.CarriesSelfTest(script) {
			t.Errorf("%s carries a guard it has no gate to prove:\n%s", name, script)
		}
	}
}

func TestHooksInstall_WritesPostCommitHook(t *testing.T) {
	repo := gateRepo(t)
	installInto(t, (&fakeGit{}).run, repo)

	post := readRepoFile(t, filepath.Join(repo, ".git", "hooks", "post-commit"))
	if !strings.Contains(post, sparkwingHookMarker) {
		t.Errorf("post-commit hook missing managed marker:\n%s", post)
	}
	if !strings.Contains(post, "sparkwing run self-install || true") {
		t.Errorf("post-commit hook should invoke its pipeline non-blocking:\n%s", post)
	}
	if strings.Contains(post, "set -e") {
		t.Errorf("post-commit hook must be non-blocking:\n%s", post)
	}

	pre := readRepoFile(t, filepath.Join(repo, ".git", "hooks", "pre-commit"))
	if !strings.Contains(pre, "set -e") || !strings.Contains(pre, "sparkwing run lint") {
		t.Errorf("pre-commit hook should stay blocking:\n%s", pre)
	}

	out := captureStdout(t, func() {
		if err := statusHooks((&fakeGit{}).run, repo); err != nil {
			t.Fatalf("status: %v", err)
		}
	})
	if !strings.Contains(out, "post-commit -> self-install\n") {
		t.Errorf("status should show the clean pipeline name without the || true suffix:\n%s", out)
	}
}

func TestHooksInstall_LeavesGitConfigAloneWithoutAGlobalHooksPath(t *testing.T) {
	repo := gateRepo(t)
	git := &fakeGit{}
	installInto(t, git.run, repo)

	if len(git.writes) != 0 {
		t.Errorf("install pinned core.hooksPath with nothing shadowing it: %v", git.writes)
	}
	pre := readRepoFile(t, filepath.Join(repo, ".git", "hooks", "pre-commit"))
	if strings.Contains(pre, `exec "$hook"`) {
		t.Errorf("nothing to chain, so the hook should not hand off:\n%s", pre)
	}
}

func TestHooksInstall_ChainsAGlobalHooksPathInsteadOfLosingIt(t *testing.T) {
	repo := gateRepo(t)
	global := t.TempDir()
	for _, name := range []string{"prepare-commit-msg", "pre-commit"} {
		if err := os.WriteFile(filepath.Join(global, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	git := &fakeGit{global: global}
	out := installInto(t, git.run, repo)

	hooksDir := filepath.Join(repo, ".git", "hooks")
	if len(git.writes) != 1 || git.writes[0] != hooksDir {
		t.Fatalf("install should claim core.hooksPath for this repo, wrote %v", git.writes)
	}

	pre := readRepoFile(t, filepath.Join(hooksDir, "pre-commit"))
	if !strings.Contains(pre, "sparkwing run lint") || !strings.Contains(pre, `hook="$global/pre-commit"`) {
		t.Errorf("a hook both layers define should run the gate then the global hook:\n%s", pre)
	}

	fwd := readRepoFile(t, filepath.Join(hooksDir, "prepare-commit-msg"))
	if !strings.Contains(fwd, `hook="$global/prepare-commit-msg"`) {
		t.Errorf("a global-only hook should get a forwarder:\n%s", fwd)
	}
	if strings.Contains(fwd, "sparkwing run") {
		t.Errorf("the forwarder should not invent a pipeline:\n%s", fwd)
	}

	post := readRepoFile(t, filepath.Join(hooksDir, "post-commit"))
	if strings.Contains(post, `exec "$hook"`) {
		t.Errorf("a hook the global layer does not define should not hand off:\n%s", post)
	}
	if !strings.Contains(out, "core.hooksPath -> "+hooksDir) {
		t.Errorf("install should say it claimed the hooks path:\n%s", out)
	}
}

func TestHooksInstall_IsIdempotentOnRerun(t *testing.T) {
	repo := gateRepo(t)
	global := t.TempDir()
	if err := os.WriteFile(filepath.Join(global, "prepare-commit-msg"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	git := &fakeGit{global: global}
	installInto(t, git.run, repo)
	first := readRepoFile(t, filepath.Join(repo, ".git", "hooks", "prepare-commit-msg"))

	out := installInto(t, git.run, repo)
	if second := readRepoFile(t, filepath.Join(repo, ".git", "hooks", "prepare-commit-msg")); second != first {
		t.Errorf("re-running the install rewrote the forwarder differently:\n%s\n---\n%s", first, second)
	}
	if !strings.Contains(out, "3 hook(s) installed, 0 skipped") {
		t.Errorf("re-running the install should reclaim its own hooks, not skip them:\n%s", out)
	}
}

func TestHooksInstall_RefusesTheClaimWhenAHandWrittenHookBlocksAGlobalForwarder(t *testing.T) {
	for _, blocked := range []string{"prepare-commit-msg", "pre-commit"} {
		t.Run(blocked, func(t *testing.T) {
			repo := gateRepo(t)
			global := t.TempDir()
			for _, name := range []string{"prepare-commit-msg", "pre-commit"} {
				if err := os.WriteFile(filepath.Join(global, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			mine := "#!/bin/sh\necho hand written\n"
			writeRepoFile(t, filepath.Join(repo, ".git", "hooks", blocked), mine)

			git := &fakeGit{global: global}
			out := installInto(t, git.run, repo)
			if got := readRepoFile(t, filepath.Join(repo, ".git", "hooks", blocked)); got != mine {
				t.Errorf("install overwrote a hook it does not manage:\n%s", got)
			}
			if !strings.Contains(out, "nothing was installed") {
				t.Errorf("install should report that it published no partial hook set:\n%s", out)
			}
			for _, name := range []string{"pre-commit", "post-commit", "prepare-commit-msg"} {
				if name == blocked {
					continue
				}
				if _, err := os.Stat(filepath.Join(repo, ".git", "hooks", name)); !os.IsNotExist(err) {
					t.Errorf("blocked forwarder left dormant %s candidate (stat err = %v)", name, err)
				}
			}
			if len(git.writes) != 0 {
				t.Errorf("install claimed core.hooksPath with the global %s unforwarded, silencing it: %v", blocked, git.writes)
			}
			if !strings.Contains(out, "core.hooksPath left alone") || !strings.Contains(out, blocked) {
				t.Errorf("install should name the global hook that held the claim back:\n%s", out)
			}
			if strings.Contains(out, "still fire") {
				t.Errorf("install promised the global hooks keep firing in the run that could not forward one:\n%s", out)
			}
		})
	}
}

func TestHooksInstall_ClaimsWhenTheSkippedHookIsNoneOfTheMachinesOwn(t *testing.T) {
	repo := gateRepo(t)
	global := t.TempDir()
	if err := os.WriteFile(filepath.Join(global, "prepare-commit-msg"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, filepath.Join(repo, ".git", "hooks", "post-commit"), "#!/bin/sh\necho hand written\n")

	git := &fakeGit{global: global}
	out := installInto(t, git.run, repo)
	hooksDir := filepath.Join(repo, ".git", "hooks")
	if len(git.writes) != 1 || git.writes[0] != hooksDir {
		t.Fatalf("a skipped hook the machine does not define costs it nothing, so the claim should proceed: %v", git.writes)
	}
	if !strings.Contains(out, "still fire") {
		t.Errorf("install should say the global hooks keep firing when every one of them is forwarded:\n%s", out)
	}
}

func TestHooksInstall_ReportsAGlobalHookNothingHandsOffToWhenTheClaimIsAlreadyInPlace(t *testing.T) {
	repo := gateRepo(t)
	hooksDir := filepath.Join(repo, ".git", "hooks")
	global := t.TempDir()
	if err := os.WriteFile(filepath.Join(global, "prepare-commit-msg"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	git := &fakeGit{global: global, local: hooksDir}
	writeRepoFile(t, filepath.Join(hooksDir, "prepare-commit-msg"), "#!/bin/sh\necho hand written\n")

	out := installInto(t, git.run, repo)
	if len(git.writes) != 0 {
		t.Errorf("the claim is already in place; install rewrote it: %v", git.writes)
	}
	if !strings.Contains(out, "global prepare-commit-msg does not fire here") {
		t.Errorf("install should report the machine's hook it cannot forward, claim or no claim:\n%s", out)
	}
}

func TestHooksInstall_IgnoresGlobalFilesGitWouldNeverRunAsAHook(t *testing.T) {
	repo := gateRepo(t)
	hooksDir := filepath.Join(repo, ".git", "hooks")
	global := t.TempDir()
	for _, name := range []string{"common.sh", "README"} {
		if err := os.WriteFile(filepath.Join(global, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	git := &fakeGit{global: global}
	out := installInto(t, git.run, repo)
	for _, name := range []string{"common.sh", "README"} {
		if _, err := os.Stat(filepath.Join(hooksDir, name)); err == nil {
			t.Errorf("install forwarded %s, which git never runs as a hook", name)
		}
	}
	if len(git.writes) != 1 || git.writes[0] != hooksDir {
		t.Errorf("a helper script in the global directory is nothing to lose, so the claim should proceed: %v", git.writes)
	}
	if strings.Contains(out, "common.sh") || strings.Contains(out, "README") {
		t.Errorf("install called a helper script a hook:\n%s", out)
	}
}

func TestHooksInstall_WarnsWhenTheRepositorySetsItsOwnHooksPath(t *testing.T) {
	repo := gateRepo(t)
	git := &fakeGit{local: filepath.Join(repo, ".githooks")}
	out := installInto(t, git.run, repo)

	if len(git.writes) != 0 {
		t.Errorf("install overwrote a core.hooksPath the repo set deliberately: %v", git.writes)
	}
	if !strings.Contains(out, "nothing was installed") || !strings.Contains(out, "--unset core.hooksPath") {
		t.Errorf("install should avoid publishing shadowed hooks and say how to fix it:\n%s", out)
	}
	for _, name := range []string{"pre-commit", "post-commit"} {
		if _, err := os.Stat(filepath.Join(repo, ".git", "hooks", name)); !os.IsNotExist(err) {
			t.Errorf("deliberate local shadow left dormant %s candidate (stat err = %v)", name, err)
		}
	}
}

func TestHooksStatus_ReportsTheChain(t *testing.T) {
	repo := gateRepo(t)
	global := t.TempDir()
	for _, name := range []string{"prepare-commit-msg", "pre-commit"} {
		if err := os.WriteFile(filepath.Join(global, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	installInto(t, (&fakeGit{global: global}).run, repo)

	out := captureStdout(t, func() {
		if err := statusHooks((&fakeGit{global: global}).run, repo); err != nil {
			t.Fatalf("status: %v", err)
		}
	})
	if !strings.Contains(out, "pre-commit -> lint, then the global hook\n") {
		t.Errorf("status should show a chained gate:\n%s", out)
	}
	if !strings.Contains(out, "prepare-commit-msg -> the global hook\n") {
		t.Errorf("status should show a forwarder for what it does:\n%s", out)
	}
}

func TestHooksStatus_ReportsMissingDeclaredHooks(t *testing.T) {
	repo := gateRepo(t)
	writeRepoFile(t, filepath.Join(repo, ".sparkwing", "sparkwing.yaml"), `pipelines:
  - name: lint
    entrypoint: Lint
    on:
      pre_commit: {}
  - name: push-gate
    entrypoint: PushGate
    on:
      pre_push: {}
`)
	writeExec(t, filepath.Join(repo, ".git", "hooks", "pre-commit"),
		renderHookScript("pre-commit", []string{"lint"}, false, ""))

	out := captureStdout(t, func() {
		if err := statusHooks((&fakeGit{}).run, repo); err != nil {
			t.Fatalf("status: %v", err)
		}
	})
	if !strings.Contains(out, "pre-commit -> lint") ||
		!strings.Contains(out, "pipelines declare pre-push but no gate is installed") ||
		!strings.Contains(out, "sparkwing pipeline hooks install --repo "+repo) {
		t.Fatalf("status did not identify and remedy the missing declared hook:\n%s", out)
	}
}

func TestHooksStatus_ReportsAGlobalHookNothingHandsOffTo(t *testing.T) {
	repo := gateRepo(t)
	global := t.TempDir()
	if err := os.WriteFile(filepath.Join(global, "prepare-commit-msg"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	git := &fakeGit{global: global}
	installInto(t, git.run, repo)
	writeRepoFile(t, filepath.Join(repo, ".git", "hooks", "prepare-commit-msg"), "#!/bin/sh\necho hand written\n")

	out := captureStdout(t, func() {
		if err := statusHooks(git.run, repo); err != nil {
			t.Fatalf("status: %v", err)
		}
	})
	if !strings.Contains(out, "global prepare-commit-msg does not fire here") {
		t.Errorf("status should report a global hook the claim silenced:\n%s", out)
	}
}

func TestHooksStatus_QuietAboutGlobalHooksItStillForwards(t *testing.T) {
	repo := gateRepo(t)
	global := t.TempDir()
	if err := os.WriteFile(filepath.Join(global, "prepare-commit-msg"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	git := &fakeGit{global: global}
	installInto(t, git.run, repo)

	out := captureStdout(t, func() {
		if err := statusHooks(git.run, repo); err != nil {
			t.Fatalf("status: %v", err)
		}
	})
	if strings.Contains(out, "does not fire here") {
		t.Errorf("status warned about a global hook the forwarder still reaches:\n%s", out)
	}
}

func TestHooksUninstall_ReleasesTheHooksPathSoGlobalHooksApplyAgain(t *testing.T) {
	repo := gateRepo(t)
	global := t.TempDir()
	if err := os.WriteFile(filepath.Join(global, "prepare-commit-msg"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	git := &fakeGit{global: global}
	installInto(t, git.run, repo)

	captureStdout(t, func() {
		if err := uninstallHooks(git.run, repo); err != nil {
			t.Fatalf("uninstall: %v", err)
		}
	})
	if _, err := os.Stat(filepath.Join(repo, ".git", "hooks", "prepare-commit-msg")); !os.IsNotExist(err) {
		t.Error("uninstall left behind the forwarder it installed")
	}
	if git.local != "" {
		t.Errorf("uninstall stranded the global hooks behind a claimed core.hooksPath = %q", git.local)
	}
}

func TestHooksUninstall_ReleasesTheClaimWhenTheManagedHooksAreAlreadyGone(t *testing.T) {
	wipes := map[string]func(t *testing.T, hooksDir string){
		"hooks deleted by hand": func(t *testing.T, hooksDir string) {
			entries, err := os.ReadDir(hooksDir)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				if err := os.Remove(filepath.Join(hooksDir, e.Name())); err != nil {
					t.Fatal(err)
				}
			}
		},
		"hook directory deleted": func(t *testing.T, hooksDir string) {
			if err := os.RemoveAll(hooksDir); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, wipe := range wipes {
		t.Run(name, func(t *testing.T) {
			repo := gateRepo(t)
			global := t.TempDir()
			if err := os.WriteFile(filepath.Join(global, "prepare-commit-msg"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			git := &fakeGit{global: global}
			installInto(t, git.run, repo)
			wipe(t, filepath.Join(repo, ".git", "hooks"))

			captureStdout(t, func() {
				if err := uninstallHooks(git.run, repo); err != nil {
					t.Fatalf("uninstall: %v", err)
				}
			})
			if git.local != "" {
				t.Errorf("uninstall left core.hooksPath = %q claiming a directory with no forwarders behind it", git.local)
			}
		})
	}
}

func TestHooksUninstall_LeavesAHooksPathItDidNotClaim(t *testing.T) {
	repo := gateRepo(t)
	own := filepath.Join(repo, ".githooks")
	git := &fakeGit{global: t.TempDir(), local: own}
	installInto(t, git.run, repo)

	captureStdout(t, func() {
		if err := uninstallHooks(git.run, repo); err != nil {
			t.Fatalf("uninstall: %v", err)
		}
	})
	if git.local != own {
		t.Errorf("uninstall cleared a core.hooksPath the repo set itself: %q", git.local)
	}
}

func TestHooksGuidance_NamesCommandsTheCLIDispatches(t *testing.T) {
	repo := gateRepo(t)
	global := t.TempDir()
	if err := os.WriteFile(filepath.Join(global, "prepare-commit-msg"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, filepath.Join(repo, ".git", "hooks", "prepare-commit-msg"), "#!/bin/sh\nexit 0\n")

	empty := gateRepo(t)
	shadow := githooks.Shadow{Repo: repo, HooksDir: filepath.Join(repo, ".git", "hooks"), ActiveDir: global, Scope: "global"}
	repoScoped := shadow
	repoScoped.Scope = "local"

	texts := map[string]string{
		"remedy for a machine-wide override": shadow.Remedy(),
		"remedy for a repository override":   repoScoped.Remedy(),
		"install refusing the claim":         installInto(t, (&fakeGit{global: global}).run, repo),
		"rendered hook script":               renderHookScript("pre-commit", []string{"lint"}, true, ""),
		"status with nothing installed": captureStdout(t, func() {
			if err := statusHooks((&fakeGit{}).run, empty); err != nil {
				t.Fatalf("status: %v", err)
			}
		}),
	}
	for what, text := range texts {
		for _, cmd := range sparkwingCommandsNamedIn(text) {
			if !dispatchesCommand(cmd) {
				t.Errorf("the %s tells the operator to run %q, which the CLI does not dispatch", what, cmd)
			}
		}
	}
}

func sparkwingCommandsNamedIn(text string) []string {
	var found []string
	for line := range strings.SplitSeq(text, "\n") {
		parts := strings.Split(line, "`")
		for i := 1; i < len(parts); i += 2 {
			if strings.HasPrefix(parts[i], "sparkwing ") {
				found = append(found, parts[i])
			}
		}
		bare := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(parts[0]), "run:"))
		if strings.HasPrefix(bare, "sparkwing ") {
			found = append(found, bare)
		}
	}
	return found
}

func dispatchesCommand(cmdline string) bool {
	registered := map[string]bool{}
	for _, c := range allCommands {
		registered[c.Path] = true
	}
	words := strings.Fields(cmdline)
	for n := len(words); n >= 2; n-- {
		if registered[strings.Join(words[:n], " ")] {
			return true
		}
	}
	return false
}

func writeRepoFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readRepoFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
