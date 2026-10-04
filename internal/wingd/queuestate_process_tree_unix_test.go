//go:build darwin || linux

package wingd_test

import (
	"io"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func TestQueueState_ActiveChildProcessPreventsStalledHolder(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.5s of real work; the fast class runs under -short")
	}
	home := shortHome(t)
	startDaemon(t, wingd.Config{
		Home:             home,
		Sampler:          newFakeSampler(4, 8<<30),
		HeadroomFraction: -1,
		StallInterval:    20 * time.Millisecond,
		StallWindow:      60 * time.Millisecond,
	})

	holderProcess := exec.Command("sh", "-c", "{ printf r; while :; do :; done; } & child=$!; wait $child")
	holderProcess.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	ready, err := holderProcess.StdoutPipe()
	if err != nil {
		t.Fatalf("create child readiness pipe: %v", err)
	}
	if err := holderProcess.Start(); err != nil {
		t.Fatalf("start holder process: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-holderProcess.Process.Pid, syscall.SIGKILL)
		_ = holderProcess.Wait()
	})

	if _, err := io.ReadFull(ready, make([]byte, 1)); err != nil {
		t.Fatalf("wait for active child: %v", err)
	}

	holder := ensure(t, home, "")
	mustAcquire(t, holder, semHostReq("active-child", "worker", holderProcess.Process.Pid, "deploy"))

	waiter := ensure(t, home, "")
	positions, _ := acquireAsync(waiter, semHostReq("waiting", "builder", holderProcess.Process.Pid+1, "deploy"))
	waitForQueue(t, positions)

	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	observation := time.NewTimer(500 * time.Millisecond)
	defer observation.Stop()
	for {
		qs, err := client.Query(t.Context(), client.Options{Home: home, Version: "v1.0.0"})
		if err != nil {
			t.Fatalf("queue state: %v", err)
		}
		if len(qs.Holders) != 1 {
			t.Fatalf("holders = %+v, want one", qs.Holders)
		}
		if qs.Holders[0].Stalled {
			t.Fatalf("holder with active child process was flagged stalled: %+v", qs.Holders[0])
		}
		select {
		case <-poll.C:
		case <-observation.C:
			return
		}
	}
}
