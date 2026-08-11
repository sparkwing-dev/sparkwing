package bincache

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestEntryLeaseHelper(t *testing.T) {
	root := os.Getenv("SPARKWING_ENTRY_HELPER_ROOT")
	key := os.Getenv("SPARKWING_ENTRY_HELPER_KEY")
	if root == "" || key == "" {
		return
	}
	entry, err := pipelineEntryAt(root, key)
	if err != nil {
		t.Fatal(err)
	}
	lease, found, err := entry.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("helper entry is absent")
	}
	defer func() { _ = lease.Release() }()
	if _, err := fmt.Fprintln(os.Stdout, "ready"); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func TestHolderLockDiesWithProcess(t *testing.T) {
	root := t.TempDir()
	key := "11111111-11111111"
	entry := testEntry(t, root, key)
	seedEntry(t, entry, "owned", time.Unix(1, 0))

	cmd := exec.Command(os.Args[0], "-test.run=^TestEntryLeaseHelper$")
	cmd.Env = append(os.Environ(),
		"SPARKWING_ENTRY_HELPER_ROOT="+root,
		"SPARKWING_ENTRY_HELPER_KEY="+key,
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("helper acknowledgement = %q, %v", line, err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("killed helper exited successfully")
	}
	_ = stdin.Close()

	result, err := Prune(context.Background(), PruneOptions{Root: root, ReclaimBytes: 1, MaxEntries: 1})
	if err != nil {
		t.Fatalf("Prune after holder death: %v", err)
	}
	if result.Active != 0 || result.Reclaimed != 1 {
		t.Fatalf("stale holder result: %+v", result)
	}
}
