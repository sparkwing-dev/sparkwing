package testmanager

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "guard-child" {
		os.Exit(Run(func() int { _ = exec.Command("systemctl", "--user", "show-environment").Run(); return 0 }))
	}
	Main(m)
}

func TestGuardRejectsUnexpectedManagerEvenIfCallerIgnoresError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("systemd and launchd are Unix user managers")
	}
	for _, optin := range []bool{false, true} {
		t.Run(fmt.Sprint("optin=", optin), func(t *testing.T) {
			root := t.TempDir()
			marker := filepath.Join(root, "real-manager-called")
			script := "#!/bin/sh\nprintf called > " + shellWord(marker) + "\n"
			if err := os.WriteFile(filepath.Join(root, "systemctl"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			gate := ""
			if optin {
				gate = "1"
			}
			cmd := exec.CommandContext(t.Context(), os.Args[0], "guard-child")
			cmd.Env = append(os.Environ(), OptInEnv+"="+gate, guardEnv+"=", "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			_, statErr := os.Stat(marker)
			if optin {
				if err != nil || statErr != nil {
					t.Fatalf("opted-in canary refused: %v %v %s", err, statErr, out)
				}
				return
			}
			if err == nil || !os.IsNotExist(statErr) || !strings.Contains(string(out), "unexpected user-manager command systemctl") {
				t.Fatalf("guard failed: error=%v canary=%v output=%s", err, statErr, out)
			}
		})
	}
}
