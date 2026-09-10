//go:build !windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestSDKCheckRejectsFIFO(t *testing.T) {
	if root := os.Getenv("SPARKWING_UPDATE_FIFO_FIXTURE"); root != "" {
		isolateUpdateTests(t)
		t.Chdir(root)
		report, code := checkUpdate(t, "--sdk", "--check")
		if report.Status != "unknown" || code != 2 || report.Installed.Path != filepath.Join(root, ".sparkwing", "go.mod") {
			t.Fatalf("FIFO module misreported: %+v %d", report, code)
		}
		if _, err := os.Stat(os.Getenv("SPARKWING_HOME")); !os.IsNotExist(err) {
			t.Fatal("FIFO check wrote state")
		}
		return
	}
	dir := sdkUpdateFixture(t, "")
	path := filepath.Join(dir, "go.mod")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSDKCheckRejectsFIFO$")
	cmd.Env = append(os.Environ(), "SPARKWING_UPDATE_FIFO_FIXTURE="+filepath.Dir(dir))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("FIFO check blocked or failed: %v %s", err, out)
	}
}

func TestInstalledArtifactInspectionRejectsFIFO(t *testing.T) {
	if path := os.Getenv("SPARKWING_UPDATE_ARTIFACT_FIFO"); path != "" {
		identity := installedArtifactIdentity(path)
		if identity.Version != "" || identity.Revision != "" || identity.Dirty != nil {
			t.Fatalf("special file has invented identity: %+v", identity)
		}
		return
	}
	path := filepath.Join(t.TempDir(), "artifact")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestInstalledArtifactInspectionRejectsFIFO$")
	cmd.Env = append(os.Environ(), "SPARKWING_UPDATE_ARTIFACT_FIFO="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("artifact FIFO inspection blocked: %v %s", err, out)
	}
}
