//go:build windows

package testshell_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/testleak"
	"github.com/sparkwing-dev/sparkwing/internal/testshell"
)

func TestMain(m *testing.M) { testleak.Main(m) }

func TestWindowsShellLauncherDoesNotProbeGoWhileInitializing(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	bin := testshell.Install(t, filepath.Join(dir, "go"), "#!/bin/sh\nprintf 'call\\n' >> '"+filepath.ToSlash(log)+"'\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, name := range []string{bin, "go"} {
		if out, err := exec.Command(name, "env").CombinedOutput(); err != nil {
			t.Fatalf("launcher %s: %v: %s", name, err, out)
		}
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(calls), "call") != 2 {
		t.Fatalf("launcher calls = %q", calls)
	}
}
