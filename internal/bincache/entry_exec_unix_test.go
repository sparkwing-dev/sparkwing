//go:build !windows

package bincache

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
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
	if mode == "child" {
		for {
			time.Sleep(time.Hour)
		}
	}
	if mode == "adopt-and-spawn" {
		if err := AdoptExecLeaseFromEnv(); err != nil {
			t.Fatal(err)
		}
		child := exec.Command(os.Args[0], "-test.run=^TestEntryExecHelper$")
		child.Env = replaceEnv(os.Environ(), "SPARKWING_ENTRY_EXEC_HELPER", "child")
		child.Stdin = nil
		child.Stdout = io.Discard
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintln(os.Stdout, child.Process.Pid); err != nil {
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
	nextMode := "hold"
	if mode == "acquire-and-spawn" {
		nextMode = "adopt-and-spawn"
	}
	env := replaceEnv(os.Environ(), "SPARKWING_ENTRY_EXEC_HELPER", nextMode)
	if err := lease.ExecReplace([]string{"-test.run=^TestEntryExecHelper$"}, "", env); err != nil {
		t.Fatal(err)
	}
}

func TestExecLeaseDoesNotSurviveInPersistentChild(t *testing.T) {
	root := t.TempDir()
	key := "11111111-11111111"
	entry := copyTestBinaryIntoEntry(t, root, key)

	cmd := exec.Command(os.Args[0], "-test.run=^TestEntryExecHelper$")
	cmd.Env = append(os.Environ(),
		"SPARKWING_ENTRY_EXEC_HELPER=acquire-and-spawn",
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
	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("persistent child pid %q: %v", line, err)
	}
	cleanupProcess(t, childPID)

	result, err := Prune(context.Background(), PruneOptions{Root: root, ReclaimBytes: 1, MaxEntries: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.ActiveSkippedEntries != 1 || result.ReclaimedEntries != 0 {
		t.Fatalf("foreground execution did not retain lease: %+v", result)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("killed foreground exited successfully")
	}
	_ = stdin.Close()
	if err := syscall.Kill(childPID, 0); err != nil {
		t.Fatalf("persistent child died with foreground: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		result, err = Prune(context.Background(), PruneOptions{Root: root, ReclaimBytes: 1, MaxEntries: 1})
		if err == nil && result.ActiveSkippedEntries == 0 && result.ReclaimedEntries == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("persistent child retained foreground lease: result=%+v err=%v", result, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(entry.binaryPath()); !os.IsNotExist(err) {
		t.Fatalf("pruned entry still exists: %v", err)
	}
}

func TestAdoptExecLeaseFailsClosedOnInvalidCoordinate(t *testing.T) {
	t.Setenv("SPARKWING_INTERNAL_CACHE_LEASE", "not-a-coordinate")
	if err := AdoptExecLeaseFromEnv(); err == nil || !strings.Contains(err.Error(), "invalid inherited") {
		t.Fatalf("invalid coordinate error = %v", err)
	}
	if _, exists := os.LookupEnv("SPARKWING_INTERNAL_CACHE_LEASE"); exists {
		t.Fatal("invalid coordinate remained available to descendants")
	}
}

func TestAdoptExecLeaseFailsClosedOnMismatchedDescriptor(t *testing.T) {
	root := t.TempDir()
	entry := testEntry(t, root, "11111111-11111111")
	lease, err := entry.openLock("lease", cacheLockShared)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cacheUnlock(lease)
		_ = lease.Close()
	}()
	wrong, err := os.CreateTemp(t.TempDir(), "not-a-lease-")
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.Close()
	coordinate := strconv.FormatUint(uint64(wrong.Fd()), 10) + ":" + entry.key + ":" +
		base64.RawURLEncoding.EncodeToString([]byte(entry.root))
	t.Setenv("SPARKWING_INTERNAL_CACHE_LEASE", coordinate)
	if err := AdoptExecLeaseFromEnv(); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched descriptor error = %v", err)
	}
	runtime.GC()
	runtime.GC()
	if _, err := wrong.WriteString("descriptor still owned by caller"); err != nil {
		t.Fatalf("rejected coordinate retained descriptor ownership: %v", err)
	}
}

func copyTestBinaryIntoEntry(t *testing.T, root, key string) Entry {
	t.Helper()
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
	return entry
}

func cleanupProcess(t *testing.T, pid int) {
	t.Helper()
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Errorf("persistent child %d was not reaped before the cleanup deadline", pid)
	})
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
