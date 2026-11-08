//go:build darwin || linux

package wingd_test

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
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

	holderProcess := exec.Command("sh", "-c", `exec 3<&0; "$0" -test.run=^TestProcSamplerCPUChild$ <&3 & wait`, os.Args[0])
	holderProcess.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	input, err := holderProcess.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
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

	reader := bufio.NewReader(ready)
	if line, err := reader.ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("child readiness = %q, %v", line, err)
	}

	holder := ensure(t, home, "")
	mustAcquire(t, holder, semHostReq("active-child", "worker", holderProcess.Process.Pid, "deploy"))

	waiter := ensure(t, home, "")
	positions, _ := acquireAsync(t, waiter, semHostReq("waiting", "builder", os.Getpid(), "deploy"))
	waitForQueue(t, positions)

	const bursts = 4
	work := make(chan error, bursts)
	go func() {
		for range bursts {
			if _, err := input.Write([]byte{1}); err != nil {
				work <- err
				return
			}
			var cpu int64
			_, err := fmt.Fscanln(reader, &cpu)
			if err == nil && cpu < int64(150*time.Millisecond) {
				err = fmt.Errorf("child consumed only %d CPU nanoseconds", cpu)
			}
			work <- err
			if err != nil {
				return
			}
		}
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	q := ensure(t, home, "")
	// safety: measured CPU work must outlast the extended descendant stall window.
	for range bursts {
		select {
		case err := <-work:
			if err != nil {
				t.Fatalf("child CPU oracle: %v", err)
			}
		case <-ctx.Done():
			t.Fatal("child never completed its CPU work")
		}
		qs, err := q.QueueState(ctx)
		if err != nil {
			t.Fatalf("queue state: %v", err)
		}
		if len(qs.Holders) != 1 {
			t.Fatalf("holders = %+v, want one", qs.Holders)
		}
		if qs.Holders[0].Stalled {
			t.Fatalf("holder with active child process was flagged stalled: %+v", qs.Holders[0])
		}
	}
}
