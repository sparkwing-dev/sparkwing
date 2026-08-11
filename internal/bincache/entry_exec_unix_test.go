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
	"strings"
	"testing"
)

func TestEntryExecHelper(t *testing.T) {
	mode := os.Getenv("SPARKWING_ENTRY_EXEC_HELPER")
	if mode == "" {
		return
	}
	if mode == "hold" {
		if _, err := fmt.Fprintln(os.Stdout, "ready"); err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	root := os.Getenv("SPARKWING_ENTRY_HELPER_ROOT")
	key := os.Getenv("SPARKWING_ENTRY_HELPER_KEY")
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
	env := replaceEnv(os.Environ(), "SPARKWING_ENTRY_EXEC_HELPER", "hold")
	if err := lease.ExecReplace([]string{"-test.run=^TestEntryExecHelper$"}, "", env); err != nil {
		t.Fatal(err)
	}
}

func replaceEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			out = append(out, item)
		}
	}
	return append(out, prefix+value)
}

func TestExecReplaceRetainsHolderLock(t *testing.T) {
	root := t.TempDir()
	key := "11111111-11111111"
	entry := testEntry(t, root, key)
	if err := os.MkdirAll(filepath.Dir(entry.binaryPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	target, err := os.OpenFile(entry.binaryPath(), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(target, source); err != nil {
		_ = target.Close()
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	sequence, err := enqueueCacheEntry(context.Background(), root, key)
	if err != nil {
		t.Fatal(err)
	}
	entryInfo, err := os.Stat(entry.entryDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := markCacheQueueRecordCurrent(root, sequence, entryInfo.ModTime()); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestEntryExecHelper$")
	cmd.Env = append(os.Environ(),
		"SPARKWING_ENTRY_EXEC_HELPER=acquire",
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
		t.Fatalf("exec helper acknowledgement = %q, %v", line, err)
	}

	result, err := Prune(context.Background(), PruneOptions{Root: root, ReclaimBytes: 1, MaxEntries: 1})
	if err != nil {
		t.Fatalf("Prune while exec holder runs: %v", err)
	}
	if result.ActiveSkippedEntries != 1 || result.ReclaimedEntries != 0 {
		t.Fatalf("exec replacement lost its lease: %+v", result)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("killed exec helper exited successfully")
	}
	_ = stdin.Close()

	result, err = Prune(context.Background(), PruneOptions{Root: root, ReclaimBytes: 1, MaxEntries: 1})
	if err != nil {
		t.Fatalf("Prune after exec holder death: %v", err)
	}
	if result.ActiveSkippedEntries != 0 || result.ReclaimedEntries != 1 {
		t.Fatalf("dead exec holder retained its lease: %+v", result)
	}
}
