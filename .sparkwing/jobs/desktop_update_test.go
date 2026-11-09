package jobs

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwingtest"
)

func TestDesktopUpdateHandsOffOutsideThePipelineAndPreviewDoesNotActivate(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux systemd handoff")
	}
	for _, mode := range []string{"handoff", "dry", "failure"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			bin := t.TempDir()
			call := filepath.Join(home, "call")
			t.Setenv("HOME", home)
			t.Setenv("SPARKWING_HOME", filepath.Join(home, "delivery"))
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("DESKTOP_TEST_CALL", call)
			t.Setenv("DESKTOP_TEST_MODE", mode)
			writeDeliveryTool(t, filepath.Join(bin, "systemd-run"), `#!/bin/sh
set -eu
printf '%s\n' "$@" > "$DESKTOP_TEST_CALL"
[ "$DESKTOP_TEST_MODE" != failure ]
`)
			ctx := newTestHost(t, sparkwingtest.Options{}).Context(t.Context())
			err := (&desktopUpdateJob{RepoDir: "/private/repo"}).run(ctx, mode == "dry")
			if (err != nil) != (mode == "failure") {
				t.Fatalf("handoff=%v", err)
			}
			body, readErr := os.ReadFile(call)
			if mode == "dry" {
				if !os.IsNotExist(readErr) {
					t.Fatalf("preview activated updater: %s", body)
				}
				return
			}
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, want := range []string{
				"--property=KillMode=control-group\n",
				"--user\n", "--collect\n", "--unit=sparkwing-desktop-update\n", "--setenv=HOME=" + home + "\n",
				"--\nbash\n/private/repo/bin/desktop-update.sh\n/private/repo\n", filepath.Join(home, ".local/bin") + "\n1800\n",
				filepath.Join(home, ".sparkwing") + "\n" + filepath.Join(home, "delivery") + "\n",
			} {
				if !strings.Contains(string(body), want) {
					t.Fatalf("handoff lacks %q: %s", want, body)
				}
			}
		})
	}
}
