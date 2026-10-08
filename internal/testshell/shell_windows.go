//go:build windows

package testshell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func init() {
	exe, _ := os.Executable()
	marker := os.Getenv("SPARKWING_TEST_SHELL_EXE")
	if marker == "" || filepath.Clean(exe) != filepath.Clean(marker) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Getenv("SPARKWING_TEST_SHELL_BASH"), append([]string{filepath.ToSlash(exe + ".sh")}, os.Args[1:]...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := procgroup.RunCommand(cmd); err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			os.Exit(e.ExitCode())
		}
		os.Exit(2)
	}
	os.Exit(0)
}

func nativeInstall(t *testing.T, name, script string) (string, bool) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bash := filepath.Clean(filepath.Join(filepath.Dir(git), "bash.exe"))
	if _, err := os.Stat(bash); err != nil {
		bash = filepath.Clean(filepath.Join(filepath.Dir(git), "..", "bin", "bash.exe"))
	}
	if _, err := os.Stat(bash); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	bin := name + ".exe"
	if err := os.WriteFile(bin, raw, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin+".sh", []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_TEST_SHELL_EXE", bin)
	t.Setenv("SPARKWING_TEST_SHELL_BASH", bash)
	return bin, true
}
