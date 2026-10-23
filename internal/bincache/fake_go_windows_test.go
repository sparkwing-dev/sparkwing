//go:build windows

package bincache

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func init() {
	executable, _ := os.Executable()
	if strings.EqualFold(filepath.Base(executable), "ssh.exe") {
		if os.Getenv("SPARKWING_TEST_SSH_SHIM") != "1" {
			os.Exit(2)
		}
		for i, arg := range os.Args {
			if arg == "-i" && i+1 < len(os.Args) {
				info, err := os.Stat(os.Args[i+1])
				if err != nil {
					os.Exit(2)
				}
				if err := fssecure.VerifyPrivateConfig(os.Args[i+1], info); err != nil {
					os.Exit(2)
				}
				if err := os.WriteFile(filepath.Join(os.Getenv("SPARKWING_TEST_SSH_RECORD"), "private"), []byte("verified"), 0o600); err != nil {
					os.Exit(2)
				}
			}
		}
		cmd := exec.Command(os.Getenv("SPARKWING_TEST_SSH_BASH"), append([]string{filepath.ToSlash(strings.TrimSuffix(executable, ".exe") + ".sh")}, os.Args[1:]...)...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				os.Exit(exit.ExitCode())
			}
			os.Exit(2)
		}
		os.Exit(0)
	}
	if !strings.EqualFold(filepath.Base(executable), "go.exe") {
		return
	}
	if os.Getenv("SPARKWING_TEST_FAKE_GO") != "1" {
		os.Exit(2)
	}
	if len(os.Args) > 1 && os.Args[1] == "env" {
		fmt.Fprint(os.Stdout, "auto\ngo1.26.6\noff\n")
		os.Exit(0)
	}
	if log := os.Getenv("SPARKWING_TEST_GO_LOG"); log != "" {
		line := strings.Join(os.Args[1:], " ") + "\n"
		if os.Getenv("SPARKWING_TEST_GO_ENV") == "1" {
			line = "ARGV " + line + "GOWORK " + os.Getenv("GOWORK") + "\n"
		}
		if err := os.WriteFile(log, []byte(line), 0o600); err != nil {
			os.Exit(2)
		}
	}
	if out, errout := os.Getenv("SPARKWING_TEST_GO_STDOUT"), os.Getenv("SPARKWING_TEST_GO_STDERR"); out != "" || errout != "" {
		fmt.Fprintln(os.Stdout, out)
		fmt.Fprintln(os.Stderr, errout)
		os.Exit(1)
	}
	for i, arg := range os.Args[1:] {
		if arg == "-o" && i+2 < len(os.Args) {
			if err := os.WriteFile(os.Args[i+2], nil, 0o600); err != nil {
				os.Exit(2)
			}
			break
		}
	}
	os.Exit(0)
}

func installNativeFakeGo(t *testing.T, binDir, log string, logEnv bool, stdout, stderr string) bool {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "go.exe"), raw, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_TEST_FAKE_GO", "1")
	t.Setenv("SPARKWING_TEST_GO_LOG", log)
	t.Setenv("SPARKWING_TEST_GO_ENV", fmt.Sprint(boolToInt(logEnv)))
	t.Setenv("SPARKWING_TEST_GO_STDOUT", stdout)
	t.Setenv("SPARKWING_TEST_GO_STDERR", stderr)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return true
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestNativeFakeGoDispatchUsesExecutableAndFailsClosed(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "argv")
	installNativeFakeGo(t, dir, log, false, "", "")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	dest := filepath.Join(dir, "output")
	command := func(marker string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, filepath.Join(dir, "go.exe"), "build", "-o", dest)
		cmd.Args[0] = "go"
		cmd.Env = append(os.Environ(), "SPARKWING_TEST_FAKE_GO="+marker)
		return cmd
	}
	if _, err := procgroup.CommandOutput(command("1"), true); err != nil {
		t.Fatal("native fake Go did not dispatch with Go's argv name")
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatal("native fake Go did not produce its output")
	}
	if err := os.Remove(dest); err != nil {
		t.Fatal(err)
	}
	_, err := procgroup.CommandOutput(command(""), true)
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 2 || ctx.Err() != nil {
		t.Fatal("native fake Go did not reject a missing marker before TestMain")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("markerless native fake Go created output")
	}
}

func installNativeSSHShim(t *testing.T, binDir, script string) bool {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if git, gitErr := exec.LookPath("git"); gitErr == nil {
		for _, candidate := range []string{filepath.Join(filepath.Dir(git), "bash.exe"), filepath.Join(filepath.Dir(git), "..", "bin", "bash.exe")} {
			if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
				bash, err = filepath.Clean(candidate), nil
				break
			}
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "ssh.exe"), raw, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "ssh.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_TEST_SSH_SHIM", "1")
	t.Setenv("SPARKWING_TEST_SSH_BASH", bash)
	return true
}
