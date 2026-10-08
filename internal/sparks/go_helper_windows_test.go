//go:build windows

package sparks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func init() {
	exe, _ := os.Executable()
	if filepath.Base(exe) != "sparks-go-helper.exe" || os.Getenv("SPARKWING_SPARKS_GO_HELPER") != "1" {
		return
	}
	if real := os.Getenv("SPARKWING_SPARKS_REAL_GO"); real != "" {
		f, err := os.OpenFile(os.Getenv("SPARKWING_SPARKS_GO_COUNTER"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(2)
		}
		_, err = f.WriteString("x\n")
		if err != nil || f.Close() != nil {
			os.Exit(2)
		}
		cmd := exec.Command(real, os.Args[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			if e, ok := err.(*exec.ExitError); ok {
				os.Exit(e.ExitCode())
			}
			os.Exit(2)
		}
	} else {
		for _, arg := range os.Args[1:] {
			if mod, ok := strings.CutPrefix(arg, "-modfile="); ok {
				if err := os.WriteFile(strings.TrimSuffix(mod, ".mod")+".sum", nil, 0o600); err != nil {
					os.Exit(2)
				}
			}
		}
	}
	os.Exit(0)
}

func nativeSparksGo(t *testing.T, real, counter string) (string, bool) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "sparks-go-helper.exe")
	if err := os.WriteFile(bin, raw, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_SPARKS_GO_HELPER", "1")
	t.Setenv("SPARKWING_SPARKS_REAL_GO", real)
	t.Setenv("SPARKWING_SPARKS_GO_COUNTER", counter)
	return bin, true
}
