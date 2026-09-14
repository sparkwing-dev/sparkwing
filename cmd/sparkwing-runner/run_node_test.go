package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The Kubernetes Job fallback starts this image's only binary with the
// run-node verb, so the built executable has to reach the shared
// implementation rather than reporting an unknown subcommand.
func TestRunNodeDispatchesToTheSharedImplementation(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the runner binary")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "sparkwing-runner")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/sparkwing-runner")
	build.Dir = root
	build.Env = append(os.Environ(), "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build sparkwing-runner: %v\n%s", err, out)
	}

	home := t.TempDir()
	cmd := exec.CommandContext(ctx, bin, "run-node")
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"SPARKWING_HOME="+filepath.Join(home, "sparkwing"),
		"SPARKWING_CONTROLLER_URL=",
		"SPARKWING_RUN_ID=",
		"SPARKWING_NODE_ID=",
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("run-node with no run or node succeeded:\n%s", out)
	}
	text := string(out)
	if strings.Contains(text, "unknown subcommand") {
		t.Fatalf("run-node is not a runner subcommand:\n%s", text)
	}
	if !strings.Contains(text, "<runID> + <nodeID> are required") {
		t.Fatalf("run-node did not reach the shared implementation:\n%s", text)
	}
}
