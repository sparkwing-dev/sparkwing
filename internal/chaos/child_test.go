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
		"--cores", "1", "--children", "2", "--run-ms", "0")
	if err := parent.Start(); err != nil {
		t.Fatalf("start parent: %v", err)
	}
	parentResult := make(chan error, 1)
	parentFinished := make(chan struct{})
	go func() {
		parentResult <- parent.Wait()
		close(parentFinished)
	}()
	readOpts := client.Options{Home: home, Version: "v1.0.0", DialTimeout: 500 * time.Millisecond, Backoff: 30 * time.Millisecond}
	t.Cleanup(func() {
		stopCrashdummyFamily(t, parent, parentFinished, readOpts)
	})

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
	if !stopCrashdummyFamily(t, parent, parentFinished, readOpts) {
		t.Fatal("crashdummy family did not stop after release")
	}
	if err := <-parentResult; err != nil {
		t.Fatalf("parent exit: %v", err)
	}
}

func stopCrashdummyFamily(t *testing.T, parent *exec.Cmd, parentFinished <-chan struct{}, opts client.Options) bool {
	t.Helper()
	if err := parent.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Errorf("release crashdummy parent: %v", err)
	}
	if !waitForCrashdummyParent(parentFinished, 2*time.Second) {
		_ = parent.Process.Kill()
		if !waitForCrashdummyParent(parentFinished, 2*time.Second) {
			t.Error("crashdummy parent did not exit after kill escalation")
			return false
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	poll := time.NewTicker(25 * time.Millisecond)
	defer poll.Stop()
	for {
		qs, err := client.Query(ctx, opts)
		if errors.Is(err, client.ErrNoDaemon) || err == nil && len(qs.Holders) == 0 && len(qs.Waiters) == 0 {
			return true
		}
		select {
		case <-poll.C:
		case <-ctx.Done():
			t.Error("crashdummy family did not converge within cleanup bound")
			return false
		}
	}
}

func waitForCrashdummyParent(finished <-chan struct{}, bound time.Duration) bool {
	timer := time.NewTimer(bound)
	defer timer.Stop()
	select {
	case <-finished:
		return true
	case <-timer.C:
		return false
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
