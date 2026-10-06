//go:build windows

package cache

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func init() {
	executable, _ := os.Executable()
	if !strings.EqualFold(filepath.Base(executable), "git.exe") || os.Getenv("SPARKWING_TEST_GIT_FORK_COUNTER") != "1" {
		return
	}
	tally, err := os.OpenFile(os.Getenv("SPARKWING_TEST_GIT_FORK_TALLY"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(2)
	}
	_, writeErr := tally.WriteString("x")
	if closeErr := tally.Close(); writeErr != nil || closeErr != nil {
		os.Exit(2)
	}
	cmd := exec.Command(os.Getenv("SPARKWING_TEST_REAL_GIT"), os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			os.Exit(exit.ExitCode())
		}
		os.Exit(2)
	}
	os.Exit(0)
}

func installNativeGitForkCounter(t *testing.T, dir, tally, real string) bool {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "git.exe"), raw, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_TEST_GIT_FORK_COUNTER", "1")
	t.Setenv("SPARKWING_TEST_GIT_FORK_TALLY", tally)
	t.Setenv("SPARKWING_TEST_REAL_GIT", real)
	return true
}
