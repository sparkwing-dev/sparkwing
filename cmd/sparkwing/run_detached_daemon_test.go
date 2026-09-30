package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

const detachedDaemonFixtureSource = `package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--describe" {
		fmt.Print("[{\"name\":\"warmfixture\"}]")
		return
	}
	_ = os.Getenv("SPARKWING_HOME")
}
`

func detachedDaemonRepo(t *testing.T) string {
	t.Helper()
	repoDir := t.TempDir()
	sparkwingDir := filepath.Join(repoDir, ".sparkwing")
	if err := os.MkdirAll(sparkwingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sparkwingDir, "go.mod"),
		[]byte("module warmfixture\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sparkwingDir, "main.go"),
		[]byte(detachedDaemonFixtureSource), 0o644); err != nil {
		t.Fatal(err)
	}
	return repoDir
}

func TestRunDetachedStartsConsumerWithoutPreWarmingDaemon(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the detached-consumer contract is exercised on POSIX process semantics")
	}
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_CONFIG", filepath.Join(home, "config.yaml"))
	t.Setenv("SPARKWING_NO_UPDATE", "1")

	repoDir := detachedDaemonRepo(t)

	warmed := 0
	consumerStarts := 0
	prevWarm, prevConsumer := ensureRunDaemonFn, ensureTriggerConsumerFn
	t.Cleanup(func() { ensureRunDaemonFn, ensureTriggerConsumerFn = prevWarm, prevConsumer })
	ensureRunDaemonFn = func() { warmed++ }
	ensureTriggerConsumerFn = func(string, time.Duration, time.Duration) error {
		consumerStarts++
		return nil
	}

	err := runDetached(context.Background(), "warmfixture",
		runFlags{detached: true, changeDir: repoDir, outputFormat: "json"}, nil)
	if err != nil {
		t.Fatalf("runDetached: %v", err)
	}
	if consumerStarts != 1 {
		t.Fatalf("consumer starts = %d, want 1", consumerStarts)
	}
	if warmed != 0 {
		t.Fatalf("daemon pre-warms = %d, want 0", warmed)
	}
}
