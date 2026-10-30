package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
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
	out := captureStderr(t, func() {
		_, err = installHooks(git, repo, filepath.Join(repo, ".sparkwing"))
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	return out
}

func rejectedInstallOutput(t *testing.T, git githooks.Git, repo string) string {
	t.Helper()
	return captureStderr(t, func() {
		if _, err := installHooks(git, repo, filepath.Join(repo, ".sparkwing")); err == nil {
			t.Fatal("rejected installation returned success")
		}
	})
}

func TestRenderHookScript_BlockingHooksAbortOnFailure(t *testing.T) {
	for _, hook := range []string{"pre-commit", "pre-push"} {
		script := renderHookScript(hook, []string{"lint", "test"})
		if !strings.Contains(script, "set -e") {
			t.Errorf("%s hook should set -e so git aborts on failure:\n%s", hook, script)
		}
		if !strings.Contains(script, "sparkwing run 'lint'\n") {
			t.Errorf("%s hook should invoke each pipeline plainly:\n%s", hook, script)
		}
		if strings.Contains(script, "|| true") {
			t.Errorf("%s hook must not swallow pipeline failures:\n%s", hook, script)
		}
	}
}

func TestRenderedHooksUseRunOutputGrammar(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX hook")
	}
	bin := t.TempDir()
	writeExec(t, filepath.Join(bin, "sparkwing"), "#!/bin/sh\nprintf '%s\\n' \"$SPARKWING_LOG_FORMAT\"\n")
	for _, hook := range []string{"pre-commit", "pre-push", "post-commit"} {
		for _, format := range []string{"", "json", "pretty", "plain"} {
			t.Run(hook+"/"+format, func(t *testing.T) {
				t.Setenv("SPARKWING_LOG_FORMAT", format)
				command := exec.CommandContext(t.Context(), "sh", "-c", renderHookScript(hook, []string{"lint"}))
				command.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				raw, err := command.Output()
				if err != nil {
					t.Fatal(err)
				}
				selected := strings.TrimSpace(string(raw))
				t.Setenv("SPARKWING_LOG_FORMAT", selected)
				flags, _ := parseRunFlagsArgs(nil)
				if flags.parseErr != nil {
					t.Fatalf("generated hook output %q: %v", selected, flags.parseErr)
				}
				want := format
				if want == "" {
					want = "plain"
				}
				if flags.outputFormat != want {
					t.Fatalf("output = %s, want %s", flags.outputFormat, want)
				}
			})
		}
	}
}

func TestRenderHookScript_PostCommitIsNonBlocking(t *testing.T) {
	script := renderHookScript("post-commit", []string{"self-install", "notify"})
	if strings.Contains(script, "set -e") {
		t.Errorf("post-commit hook must not set -e (the commit already landed):\n%s", script)
	}
	for _, p := range []string{"self-install", "notify"} {
		if !strings.Contains(script, "sparkwing run '"+p+"' || true\n") {
			t.Errorf("post-commit hook should tolerate %q failing and continue:\n%s", p, script)
		}
	}
	if !strings.HasSuffix(script, "exit 0\n") {
		t.Errorf("post-commit hook must always exit zero:\n%s", script)
	}
}

func TestHooksInstall_WritesPostCommitHook(t *testing.T) {
	repo := gateRepo(t)
	installInto(t, (&fakeGit{}).run, repo)

	post := readRepoFile(t, filepath.Join(repo, ".git", "hooks", "post-commit"))
	if !strings.Contains(post, sparkwingHookMarker) {
		t.Errorf("post-commit hook missing managed marker:\n%s", post)
	}
	if !strings.Contains(post, "sparkwing run 'self-install' || true") {
		t.Errorf("post-commit hook should invoke its pipeline non-blocking:\n%s", post)
	}
	if strings.Contains(post, "set -e") {
		t.Errorf("post-commit hook must be non-blocking:\n%s", post)
	}

	pre := readRepoFile(t, filepath.Join(repo, ".git", "hooks", "pre-commit"))
	if !strings.Contains(pre, "set -e") || !strings.Contains(pre, "sparkwing run 'lint'") {
		t.Errorf("pre-commit hook should stay blocking:\n%s", pre)
	}

	out := captureStdout(t, func() {
		if err := statusHooks((&fakeGit{}).run, repo, "pretty"); err != nil {
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

func TestHooksInstall_IsIdempotentOnRerun(t *testing.T) {
	repo := gateRepo(t)
	git := &fakeGit{}
	installInto(t, git.run, repo)
	first := readRepoFile(t, filepath.Join(repo, ".git", "hooks", "pre-commit"))
	out := installInto(t, git.run, repo)
	if second := readRepoFile(t, filepath.Join(repo, ".git", "hooks", "pre-commit")); second != first {
		t.Errorf("reinstall changed the hook: %s", second)
	}
	if !strings.Contains(out, "2 hook(s) installed, 0 skipped") {
		t.Errorf("reinstall skipped managed hooks: %s", out)
	}
}

func TestHooksInstall_WarnsWhenTheRepositorySetsItsOwnHooksPath(t *testing.T) {
	repo := gateRepo(t)
	git := &fakeGit{local: filepath.Join(repo, ".githooks")}
	_, installErr := installHooks(git.run, repo, filepath.Join(repo, ".sparkwing"))
	if installErr == nil {
		t.Fatal("rejected installation returned success")
	}
	out := installErr.Error()

	if len(git.writes) != 0 {
		t.Errorf("install overwrote a core.hooksPath the repo set deliberately: %v", git.writes)
	}
	if !strings.Contains(out, "--unset core.hooksPath") {
		t.Errorf("install should avoid publishing shadowed hooks and say how to fix it:\n%s", out)
	}
	for _, name := range []string{"pre-commit", "post-commit"} {
		if _, err := os.Stat(filepath.Join(repo, ".git", "hooks", name)); !os.IsNotExist(err) {
			t.Errorf("deliberate local shadow left dormant %s candidate (stat err = %v)", name, err)
		}
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
		renderHookScript("pre-commit", []string{"lint"}))

	out := captureStdout(t, func() {
		if err := statusHooks((&fakeGit{}).run, repo, "pretty"); err != nil {
			t.Fatalf("status: %v", err)
		}
	})
	if !strings.Contains(out, "pre-commit -> lint") ||
		!strings.Contains(out, "pipelines declare pre-push but the hooks are missing or not current") ||
		!strings.Contains(out, "sparkwing -C "+repo+" pipeline hooks install") {
		t.Fatalf("status did not identify and remedy the missing declared hook:\n%s", out)
	}
}

func TestHooksUninstall_LeavesAHooksPathItDidNotClaim(t *testing.T) {
	repo := gateRepo(t)
	own := filepath.Join(repo, ".githooks")
	git := &fakeGit{global: t.TempDir(), local: own}
	rejectedInstallOutput(t, git.run, repo)

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
		"install refusing the claim":         rejectedInstallOutput(t, (&fakeGit{global: global}).run, repo),
		"rendered hook script":               renderHookScript("pre-commit", []string{"lint"}),
		"status with nothing installed": captureStdout(t, func() {
			if err := statusHooks((&fakeGit{}).run, empty, "pretty"); err != nil {
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
	words := strings.Fields(stripRootFlags(cmdline))
	for n := len(words); n >= 2; n-- {
		if registered[strings.Join(words[:n], " ")] {
			return true
		}
	}
	return false
}

func writeRegistry(t *testing.T, path, body string) {
	t.Helper()
	nested := "repos:\n  " + strings.ReplaceAll(strings.TrimSuffix(body, "\n"), "\n", "\n  ") + "\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := fssecure.WriteFile(path, []byte(nested)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_CONFIG", path)
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

func TestHooksUninstallStillRemovesHooksAfterTheProjectFilesAreGone(t *testing.T) {
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".sparkwing"), 0o755); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(repo, ".git", "hooks", "pre-commit")
	writeExec(t, hook, renderHookScript("pre-commit", []string{"lint"}))
	captureStdout(t, func() {
		if err := runIn(t, filepath.Join(repo, ".sparkwing"), parseHooksUninstall); err != nil {
			t.Fatalf("hooks uninstall with no sparkwing.yaml or go.mod: %v", err)
		}
	})
	if _, err := os.Stat(hook); !os.IsNotExist(err) {
		t.Fatalf("managed hook survived uninstall: %v", err)
	}
}

func TestHooksUninstallStopsAtAnUninspectableDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits do not deny directory traversal on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root traverses a mode-000 directory")
	}
	root := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if err := os.MkdirAll(filepath.Join(root, ".sparkwing"), 0o755); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(root, "app")
	inner := filepath.Join(locked, "src")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(inner)
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	_, err := resolveCleanupRoot()
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("cleanup discovery under an uninspectable directory = %v, want the inspection error", err)
	}
}

func TestDiscoveryRefusesTheDefaultStateDirWhenTheHomeIsRedirected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SPARKWING_HOME", t.TempDir())
	stateDir := filepath.Join(home, ".sparkwing")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "sparkwing.yaml"), []byte("sparks: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(home, "code", "app")
	if out, err := exec.Command("git", "init", "-q", checkout).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if _, ok, err := nearestDotSparkwing(checkout); ok || err != nil {
		t.Fatalf("project discovery adopted the default state dir: ok=%v err=%v", ok, err)
	}
	t.Chdir(checkout)
	root, err := resolveCleanupRoot()
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.EvalSymlinks(checkout); root != want && root != checkout {
		t.Fatalf("cleanup discovery = %s, want the checkout %s, not the home holding the default state dir", root, checkout)
	}
}

func TestHooksStatus_ComparesExpectedContentAndFormat(t *testing.T) {
	repo := gateRepo(t)
	git := &fakeGit{}
	installInto(t, git.run, repo)
	for _, changed := range []bool{false, true} {
		if changed {
			path := filepath.Join(repo, ".git", "hooks", "pre-commit")
			body := readRepoFile(t, path)
			writeRepoFile(t, path, strings.Replace(body, "(format 1)", "(format 0)", 1))
		}
		output := captureStdout(t, func() {
			if err := statusHooks(git.run, repo, "json"); err != nil {
				t.Fatal(err)
			}
		})
		want := `"current":true`
		if changed {
			want = `"current":false`
		}
		if !strings.Contains(output, want) {
			t.Fatalf("wanted %s in %s", want, output)
		}
	}
}

func TestHooksStatus_RejectsIncompleteOrStaleGate(t *testing.T) {
	modes := []string{"missing", "unmanaged", "content", "version", "undeclared"}
	if runtime.GOOS != "windows" {
		modes = append(modes, "not-executable")
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			repo := gateRepo(t)
			git := &fakeGit{}
			installInto(t, git.run, repo)
			path := filepath.Join(repo, ".git", "hooks", "pre-commit")
			body := readRepoFile(t, path)
			switch mode {
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "unmanaged":
				writeRepoFile(t, path, "#!/bin/sh\nexit 0\n")
			case "not-executable":
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "content":
				writeRepoFile(t, path, body+"exit 0\n")
			case "version":
				writeRepoFile(t, path, strings.Replace(body, "(format 1)", "(format 0)", 1))
			case "undeclared":
				writeExec(t, filepath.Join(repo, ".git", "hooks", "pre-push"), renderHookScript("pre-push", []string{"old-gate"}))
			}
			output := captureStdout(t, func() {
				if err := statusHooks(git.run, repo, "json"); err != nil {
					t.Fatal(err)
				}
			})
			if strings.Contains(output, `"state":"armed"`) {
				t.Fatalf("stale gate reported as armed: %s", output)
			}
		})
	}
}

func writeExec(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestHooksInstall_RefusesGlobalHooksPathWithoutChangingFilesOrConfig(t *testing.T) {
	repo := gateRepo(t)
	hook := filepath.Join(repo, ".git", "hooks", "pre-commit")
	prior := "#!/bin/sh\n# Installed by sparkwing\nexit 0\n"
	writeExec(t, hook, prior)
	git := &fakeGit{global: filepath.Join(t.TempDir(), "global-hooks")}
	_, err := installHooks(git.run, repo, filepath.Join(repo, ".sparkwing"))
	if err == nil {
		t.Fatal("global core.hooksPath conflict was accepted")
	}
	for _, want := range []string{git.global, "compose the hooks manually", "git config --global --unset core.hooksPath"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q omits %q", err, want)
		}
	}
	if len(git.writes) != 0 {
		t.Fatalf("installation changed git config: %v", git.writes)
	}
	if got := readRepoFile(t, hook); got != prior {
		t.Fatalf("installation changed prior hook: %q", got)
	}
}
