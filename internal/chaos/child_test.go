package chaos

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// TestCrashdummy_ChildrenAttachToParentLease drives a real crashdummy
// parent that spawns children attaching to its lease, and asserts through
// the daemon's queue view that the children share the one lease without
// double-charging host cores, then that the whole family converges.
func TestCrashdummy_ChildrenAttachToParentLease(t *testing.T) {
	if testing.Short() {
		t.Skip("crashdummy process test skipped in -short")
	}
	home, err := os.MkdirTemp("/tmp", "chaoschild")
	if err != nil {
		t.Fatalf("temp home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })

	bin := filepath.Join(home, "crashdummy")
	build := exec.Command("go", "build", "-o", bin,
		"github.com/sparkwing-dev/sparkwing/internal/chaos/crashdummy")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build crashdummy: %v", err)
	}

	parent := exec.Command(bin, "hold", "--home", home, "--run", "p",
		"--cores", "1", "--children", "2", "--run-ms", "4000")
	if err := parent.Start(); err != nil {
		t.Fatalf("start parent: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(parent.Process.Pid, syscall.SIGKILL)
		_, _ = parent.Process.Wait()
	})

	readOpts := client.Options{Home: home, DialTimeout: 500 * time.Millisecond, Backoff: 30 * time.Millisecond}

	deadline := time.Now().Add(6 * time.Second)
	var sawHolder bool
	for time.Now().Before(deadline) {
		qs, err := client.Query(context.Background(), readOpts)
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if len(qs.Holders) == 0 {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		sawHolder = true
		var parents, children int
		for _, h := range qs.Holders {
			if h.Parent == "" {
				parents++
				if h.RunID != "p" {
					t.Fatalf("top-level holder run id %q, want parent p", h.RunID)
				}
				continue
			}
			children++
			if h.Parent != "p" {
				t.Fatalf("attached child %q names parent %q, want p", h.RunID, h.Parent)
			}
			if h.Resources.Cores != 0 || h.Resources.MemoryBytes != 0 {
				t.Fatalf("attached child %q charged %+v, want zero", h.RunID, h.Resources)
			}
		}
		if parents != 1 {
			t.Fatalf("want exactly 1 top-level holder (children share the lease), got %d: %+v", parents, qs.Holders)
		}
		if held := resourceHeld(qs, "cores"); held != 1 {
			t.Fatalf("cores held %g, want 1 (children must not double-charge)", held)
		}
		break
	}
	if !sawHolder {
		t.Fatal("parent never appeared as a holder")
	}

	convDeadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(convDeadline) {
		qs, err := client.Query(context.Background(), readOpts)
		if errors.Is(err, client.ErrNoDaemon) {
			return
		}
		if err == nil && len(qs.Holders) == 0 && len(qs.Waiters) == 0 {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatal("family did not converge after parent and children exited")
}

func resourceHeld(qs wingwire.QueueState, key string) float64 {
	for _, r := range qs.Resources {
		if r.Key == key {
			return r.Held
		}
	}
	return -1
}
