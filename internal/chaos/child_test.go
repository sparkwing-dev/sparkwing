package chaos

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
		_ = parent.Process.Kill()
		_, _ = parent.Process.Wait()
	})

	readOpts := client.Options{Home: home, Version: "v1.0.0", DialTimeout: 500 * time.Millisecond, Backoff: 30 * time.Millisecond}

	readyCtx, cancelReady := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancelReady()
	readyPoll := time.NewTicker(100 * time.Millisecond)
	defer readyPoll.Stop()
	var sawFamily bool
	for !sawFamily {
		qs, err := client.Query(readyCtx, readOpts)
		if err == nil && len(qs.Holders) != 0 {
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
			if parents > 1 || children > 2 {
				t.Fatalf("want 1 parent and 2 children, got %d parents and %d children: %+v", parents, children, qs.Holders)
			}
			if parents != 1 || children != 2 {
				select {
				case <-readyCtx.Done():
					t.Fatalf("complete parent-child family never appeared; last state: %+v", qs.Holders)
				case <-readyPoll.C:
				}
				continue
			}
			if held := resourceHeld(qs, "cores"); held != 1 {
				t.Fatalf("cores held %g, want 1 (children must not double-charge)", held)
			}
			sawFamily = true
			break
		}
		select {
		case <-readyCtx.Done():
			t.Fatal("complete parent-child family never appeared")
		case <-readyPoll.C:
		}
	}

	convergeCtx, cancelConverge := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancelConverge()
	convergePoll := time.NewTicker(150 * time.Millisecond)
	defer convergePoll.Stop()
	for {
		qs, err := client.Query(convergeCtx, readOpts)
		if errors.Is(err, client.ErrNoDaemon) {
			return
		}
		if err == nil && len(qs.Holders) == 0 && len(qs.Waiters) == 0 {
			return
		}
		select {
		case <-convergeCtx.Done():
			t.Fatal("family did not converge after parent and children exited")
		case <-convergePoll.C:
		}
	}
}

func resourceHeld(qs wingwire.QueueState, key string) float64 {
	for _, r := range qs.Resources {
		if r.Key == key {
			return r.Held
		}
	}
	return -1
}
