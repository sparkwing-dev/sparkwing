package gitenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnbind_DropsEveryRepositoryBindingVariable(t *testing.T) {
	t.Setenv(GateIndexVar, "")
	for _, name := range bindingVars {
		t.Setenv(name, "/gating/repo")
	}

	Unbind()

	for _, name := range bindingVars {
		if got, ok := os.LookupEnv(name); ok {
			t.Errorf("%s survived as %q; a child git would still target the gating repository", name, got)
		}
	}
}

func TestUnbind_KeepsIdentityAndConfigSelection(t *testing.T) {
	kept := map[string]string{
		"GIT_AUTHOR_NAME":     "sparkwing test",
		"GIT_COMMITTER_EMAIL": "test@example.invalid",
		"GIT_CONFIG_GLOBAL":   "/tmp/hermetic-gitconfig",
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_TERMINAL_PROMPT": "0",
	}
	for name, value := range kept {
		t.Setenv(name, value)
	}
	t.Setenv("GIT_DIR", "/gating/repo/.git")

	Unbind()

	for name, want := range kept {
		if got := os.Getenv(name); got != want {
			t.Errorf("%s = %q, want %q: unbinding must not disturb identity or config selection", name, got, want)
		}
	}
}

func TestUnbind_LetsGitResolveTheDirectoryItIsPointedAt(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	gating, elsewhere := initRepo(t), initRepo(t)
	t.Setenv("GIT_DIR", filepath.Join(gating, ".git"))
	t.Setenv("GIT_WORK_TREE", gating)

	if got := gitDirOf(t, elsewhere); got != filepath.Join(gating, ".git") {
		t.Fatalf("fixture is not reproducing the hook environment: git resolved %q, want the gating repository", got)
	}

	Unbind()

	if got := gitDirOf(t, elsewhere); got != filepath.Join(elsewhere, ".git") {
		t.Errorf("git -C %s resolved %q; a pipeline step still operates on the gating repository", elsewhere, got)
	}
}

func TestUnbind_KeepsTheGatingCommitsIndexUnderItsOwnName(t *testing.T) {
	index := filepath.Join(t.TempDir(), "next-index-4321.lock")
	writeFile(t, index)
	t.Setenv(GateIndexVar, "")
	t.Setenv("GIT_INDEX_FILE", index)

	Unbind()

	if _, ok := os.LookupEnv("GIT_INDEX_FILE"); ok {
		t.Error("GIT_INDEX_FILE survived; a stray git add anywhere would write into the commit being gated")
	}
	if got := GateIndex(); got != index {
		t.Errorf("GateIndex() = %q, want %q: a staged-diff check has no way to see what is being committed", got, index)
	}
}

func TestUnbind_ResolvesARelativeIndexAgainstTheHooksDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(GateIndexVar, "")
	t.Setenv("GIT_INDEX_FILE", ".git/index")

	Unbind()

	if got, want := os.Getenv(GateIndexVar), filepath.Join(wd, ".git", "index"); got != want {
		t.Errorf("%s = %q, want %q: a step that changed directory would read an index that is not there", GateIndexVar, got, want)
	}
}

func TestUnbind_LeavesAnIndexAnEarlierScrubAlreadyCapturedAlone(t *testing.T) {
	captured := filepath.Join(t.TempDir(), "index.lock")
	writeFile(t, captured)
	t.Setenv(GateIndexVar, captured)
	t.Setenv("GIT_DIR", "/gating/repo/.git")

	Unbind()

	if got := os.Getenv(GateIndexVar); got != captured {
		t.Errorf("%s = %q, want %q: the hook script scrubs before sparkwing does, and its capture is the only one left", GateIndexVar, got, captured)
	}
}

func TestGateIndex_ReportsNoIndexOnceTheGatingCommitHasFinished(t *testing.T) {
	t.Setenv(GateIndexVar, filepath.Join(t.TempDir(), "next-index-77.lock"))

	if got := GateIndex(); got != "" {
		t.Errorf("GateIndex() = %q, want empty: git pointed at a deleted index reads an empty one, which shows a gate zero files", got)
	}
}

func TestShellUnbind_CapturesAndScrubsWhatUnbindDoes(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	script := ShellUnbind() + "echo \"${" + GateIndexVar + ":-none}\"\n" +
		"for v in " + strings.Join(bindingVars, " ") + "; do\n" +
		"eval \"val=\\${$v:-}\"\n" +
		"[ -z \"$val\" ] || echo \"$v survived as $val\"\n" +
		"done\n"

	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_INDEX_FILE=.git/index", "GIT_DIR=.git"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sh: %v\n%s", err, out)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if want := filepath.Join(dir, ".git", "index"); lines[0] != want {
		t.Errorf("the hook exported %q, want %q: an older sparkwing on PATH gets no index to check", lines[0], want)
	}
	if len(lines) > 1 {
		t.Errorf("the hook left bindings behind:\n%s", strings.Join(lines[1:], "\n"))
	}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("index\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-b", "main", dir)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v\n%s", dir, err, out)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func gitDirOf(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--absolute-git-dir").CombinedOutput()
	if err != nil {
		t.Fatalf("git -C %s rev-parse: %v\n%s", dir, err, out)
	}
	return strings.TrimSpace(string(out))
}
