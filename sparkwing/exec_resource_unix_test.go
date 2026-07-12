//go:build unix

package sparkwing_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type sampleCollector struct {
	mu      sync.Mutex
	samples []sparkwing.ResourceSample
}

func (c *sampleCollector) report(s sparkwing.ResourceSample) {
	c.mu.Lock()
	c.samples = append(c.samples, s)
	c.mu.Unlock()
}

func (c *sampleCollector) peak() (cpu, mem int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.samples {
		if s.CPUMillicores > cpu {
			cpu = s.CPUMillicores
		}
		if s.MemoryBytes > mem {
			mem = s.MemoryBytes
		}
	}
	return cpu, mem
}

// A shell-step busy loop consumes CPU in a spawned process, not the
// orchestrator goroutine, so its cost is invisible to RUSAGE_SELF. The
// per-command rusage must surface a true nonzero CPU peak.
func TestExec_ShellBurnerRecordsNonzeroCPU(t *testing.T) {
	col := &sampleCollector{}
	ctx := sparkwingruntime.WithLogger(context.Background(), &recordingLogger{})
	ctx = sparkwing.WithResourceReporter(ctx, col.report)

	_, err := sparkwing.Bash(ctx, `i=0; while [ $i -lt 1000000 ]; do i=$((i+1)); done`).Run()
	if err != nil {
		t.Fatalf("Bash burner: %v", err)
	}
	cpu, mem := col.peak()
	if cpu <= 0 {
		t.Fatalf("peak CPU millicores = %d, want > 0 for a shell busy loop", cpu)
	}
	if mem <= 0 {
		t.Fatalf("peak memory bytes = %d, want > 0 for a spawned process", mem)
	}
}

// A spawned binary that burns CPU (awk arithmetic loop) is a separate
// process; its cost must be measured through the command's rusage.
func TestExec_SpawnedBinaryBurnerRecordsNonzeroCPU(t *testing.T) {
	if _, err := exec.LookPath("awk"); err != nil {
		t.Skip("awk not on PATH")
	}
	col := &sampleCollector{}
	ctx := sparkwingruntime.WithLogger(context.Background(), &recordingLogger{})
	ctx = sparkwing.WithResourceReporter(ctx, col.report)

	_, err := sparkwing.Exec(ctx, "awk", "BEGIN{for(i=0;i<40000000;i++)s+=i; print s}").Run()
	if err != nil {
		t.Fatalf("awk burner: %v", err)
	}
	cpu, _ := col.peak()
	if cpu <= 0 {
		t.Fatalf("peak CPU millicores = %d, want > 0 for a spawned binary burner", cpu)
	}
}

// A command with no resource reporter installed runs normally: reporting
// is a no-op, never a failure.
func TestExec_NoReporterIsHarmless(t *testing.T) {
	ctx := sparkwingruntime.WithLogger(context.Background(), &recordingLogger{})
	if _, err := sparkwing.Bash(ctx, "true").Run(); err != nil {
		t.Fatalf("Run without reporter: %v", err)
	}
}

// Cancelling a node whose shell backgrounds a child must tear down the
// whole process group: the grandchild is signalled via the negative pgid,
// so no burner is orphaned after cancel.
func TestExec_CancelKillsProcessTree(t *testing.T) {
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "child.pid")
	ctx, cancel := context.WithCancel(sparkwingruntime.WithLogger(context.Background(), &recordingLogger{}))

	done := make(chan struct{})
	go func() {
		defer close(done)
		script := fmt.Sprintf(`sleep 120 & echo $! > %q; wait`, pidfile)
		_, _ = sparkwing.Bash(ctx, script).Run()
	}()

	childPID := waitForPID(t, pidfile)
	if !pidAlive(childPID) {
		t.Fatalf("backgrounded child %d never came alive", childPID)
	}

	cancel()

	deadline := time.Now().Add(5 * time.Second)
	for pidAlive(childPID) {
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d still alive 5s after cancel; process tree was not killed", childPID)
		}
		time.Sleep(20 * time.Millisecond)
	}
	<-done
}

func waitForPID(t *testing.T, pidfile string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(pidfile)
		if err == nil {
			if s := strings.TrimSpace(string(data)); s != "" {
				pid, err := strconv.Atoi(s)
				if err == nil && pid > 0 {
					return pid
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("child pidfile %s never populated", pidfile)
	return 0
}

// pidAlive reports whether pid names a live process; signal 0 probes
// existence without delivering anything.
func pidAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
