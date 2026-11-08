package testgit

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandsLeaveAnAmbientRepositoryUntouched(t *testing.T) {
	for _, shell := range []bool{false, true} {
		name := "native"
		if shell {
			name = "shell"
		}
		t.Run(name, func(t *testing.T) { checkAmbientRepository(t, shell) })
	}
}

func checkAmbientRepository(t *testing.T, shell bool) {
	t.Helper()
	script, err := filepath.Abs("../../../bin/test-git-env.sh")
	if err != nil {
		t.Fatal(err)
	}
	if shell {
		if _, err := exec.LookPath("bash"); err != nil {
			t.Skip("bash is not installed")
		}
	}
	sentinel, fixture := t.TempDir(), t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		args = append([]string{"-C", dir}, args...)
		cmd := CommandContext(t.Context(), "git", args...)
		if shell {
			cmd = CommandContext(t.Context(), "bash", append([]string{"-c", `source "$1"; shift; git "$@"`, "fixture", script}, args...)...)
			cmd.Env = os.Environ()
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run(sentinel, "init", "--quiet")
	config := filepath.Join(sentinel, ".git", "config")
	before, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(sentinel, ".git", "index")
	for name, value := range map[string]string{
		"GIT_DIR": filepath.Join(sentinel, ".git"), "GIT_WORK_TREE": sentinel,
		"GIT_COMMON_DIR": filepath.Join(sentinel, ".git"), "GIT_INDEX_FILE": index,
		"GIT_OBJECT_DIRECTORY":             filepath.Join(sentinel, ".git", "objects"),
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": filepath.Join(sentinel, ".git", "objects"),
		"GIT_CONFIG":                       config, "GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.worktree", "GIT_CONFIG_VALUE_0": sentinel,
	} {
		t.Setenv(name, value)
	}
	run(fixture, "init", "--quiet")
	run(fixture, "config", "user.name", "Fixture Test")
	run(fixture, "config", "core.worktree", fixture)
	after, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("ambient repository config changed:\n%s", after)
	}
	if err := os.WriteFile(filepath.Join(fixture, "tracked"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := Command("git", "-C", fixture, "add", "tracked")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	if _, err := os.Stat(index); !os.IsNotExist(err) {
		t.Fatalf("ambient index was touched: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture, ".git", "index")); err != nil {
		t.Fatalf("fixture index missing: %v", err)
	}
	for _, entry := range cmd.Environ() {
		if strings.HasPrefix(entry, "GIT_DIR=") {
			t.Fatalf("binding survived: %s", entry)
		}
	}
}
