//go:build !windows

package bincache

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestPruneQuarantinesRunningLegacyExecutable(t *testing.T) {
	if os.Getenv("SPARKWING_LEGACY_EXEC_HELPER") == "1" {
		fmt.Println("ready")
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	originalNow := cacheNow
	t.Cleanup(func() { cacheNow = originalNow })
	now := time.Unix(100, 0)
	cacheNow = func() time.Time { return now }
	cacheRoot := t.TempDir()
	root := filepath.Join(cacheRoot, pipelineCacheSchema)
	legacyDir := filepath.Join(cacheRoot, "11111111-11111111")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacyBinary := filepath.Join(legacyDir, "pipelines")
	if err := os.Link(os.Args[0], legacyBinary); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(legacyBinary, "-test.run=^TestPruneQuarantinesRunningLegacyExecutable$")
	cmd.Env = append(os.Environ(), "SPARKWING_LEGACY_EXEC_HELPER=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("helper readiness = (%q, %v)", line, err)
	}

	if _, err := Prune(context.Background(), PruneOptions{Root: root, ReclaimBytes: 1, MaxEntries: 1}); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("legacy process did not survive quarantine: %v", err)
	}
	now = now.Add(legacyRetirementGrace)
	result, err := Prune(context.Background(), PruneOptions{Root: root, ReclaimBytes: 1, MaxEntries: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reclaimed != 0 || result.GoalSatisfied {
		t.Fatalf("live executable was reported reclaimed: %+v", result)
	}
	quarantine := filepath.Join(root, "legacy-retired", "11111111-11111111")
	if _, err := os.Stat(quarantine); err != nil {
		t.Fatalf("live executable quarantine removed: %v", err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	result, err = Prune(context.Background(), PruneOptions{Root: root, ReclaimBytes: 1, MaxEntries: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reclaimed != 1 || !result.GoalSatisfied {
		t.Fatalf("retired executable was not reclaimed: %+v", result)
	}
}
