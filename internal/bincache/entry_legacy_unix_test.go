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
	originalAvailable := legacyFilesystemAvailableBytes
	t.Cleanup(func() { legacyFilesystemAvailableBytes = originalAvailable })
	legacyFilesystemAvailableBytes = func(string) (int64, error) { return 100, nil }
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
	if result.Reclaimed != 1 || result.ReclaimedBytes != 0 || result.GoalSatisfied {
		t.Fatalf("live executable removal claimed unavailable capacity: %+v", result)
	}
	quarantine := filepath.Join(root, "legacy-retired", "11111111-11111111")
	if _, err := os.Stat(quarantine); !os.IsNotExist(err) {
		t.Fatalf("live executable namespace remains: %v", err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyRemovalCreditsOnlyObservedAllocatedCapacity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "pipelines"), make([]byte, 8192), 0o700); err != nil {
		t.Fatal(err)
	}
	allocated, err := legacyAllocatedBytes(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	originalAvailable := legacyFilesystemAvailableBytes
	t.Cleanup(func() { legacyFilesystemAvailableBytes = originalAvailable })
	read := 0
	legacyFilesystemAvailableBytes = func(string) (int64, error) {
		read++
		if read == 1 {
			return 100, nil
		}
		return 100 + allocated + 1, nil
	}
	reclaimed, err := removeLegacyCacheEntry(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed != allocated {
		t.Fatalf("reclaimed bytes = %d, want allocated cap %d", reclaimed, allocated)
	}
}

func TestManagedRemovalDoesNotCreditUnobservedCapacity(t *testing.T) {
	originalAvailable := legacyFilesystemAvailableBytes
	t.Cleanup(func() { legacyFilesystemAvailableBytes = originalAvailable })
	legacyFilesystemAvailableBytes = func(string) (int64, error) { return 100, nil }

	root := filepath.Join(t.TempDir(), pipelineCacheSchema)
	entry := testEntry(t, root, "11111111-11111111")
	seedEntry(t, entry, "managed", time.Unix(1, 0))

	result, err := Prune(context.Background(), PruneOptions{
		Root: root, ReclaimBytes: 1, MaxEntries: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reclaimed != 1 {
		t.Fatalf("removed entries = %d, want 1", result.Reclaimed)
	}
	if result.ReclaimedBytes != 0 || result.GoalSatisfied {
		t.Fatalf("managed removal claimed unobserved capacity: %+v", result)
	}
	if _, err := os.Stat(entry.entryDir()); !os.IsNotExist(err) {
		t.Fatalf("managed entry remains after removal: %v", err)
	}
}
