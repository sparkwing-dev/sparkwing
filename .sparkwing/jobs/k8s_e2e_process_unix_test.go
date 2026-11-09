//go:build !windows

package jobs

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

func TestKubernetesE2ECommandCancellationRunsCleanup(t *testing.T) {
	bound, stop := context.WithTimeout(t.Context(), 10*time.Second)
	defer stop()
	ctx, cancel := context.WithCancel(bound)
	defer cancel()
	cleanupMarker := filepath.Join(t.TempDir(), "cleanup")
	cmd := exec.CommandContext(ctx, kubernetesTestBash(t), "-c", `
trap 'printf cleanup >"$CLEANUP_MARKER"; exit 143' TERM INT
printf 'ready\n'
read -r hold
`)
	cmd.Env = append(os.Environ(), "CLEANUP_MARKER="+cleanupMarker)
	configureKubernetesE2ECommand(cmd)
	if cmd.Cancel == nil || cmd.WaitDelay != 90*time.Second {
		t.Fatal("Kubernetes command cancellation is not bounded and graceful")
	}
	signaled := make(chan error, 1)
	signal := cmd.Cancel
	cmd.Cancel = func() error {
		err := signal()
		signaled <- err
		return err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var done chan struct{}
	var waitErr error
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		if done == nil {
			_ = cmd.Wait()
		} else {
			<-done
		}
	})
	ready := bufio.NewScanner(stdout)
	if !ready.Scan() || ready.Text() != "ready" {
		t.Fatalf("missing cancellation readiness: %v", ready.Err())
	}
	done = make(chan struct{})
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-bound.Done():
		select {
		case err := <-signaled:
			t.Logf("cancellation signal for PID %d: %v", cmd.Process.Pid, err)
		default:
			t.Logf("cancellation callback has not returned for PID %d", cmd.Process.Pid)
		}
		if runtime.GOOS == "linux" {
			status, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid), "status"))
			t.Logf("cancellation signal masks and process status (%v):\n%s", err, status)
		}
		probe, stopProbe := context.WithTimeout(context.Background(), time.Second)
		defer stopProbe()
		state, err := exec.CommandContext(probe, "ps", "-o", "pid,ppid,pgid,sid,stat,wchan,args", "-p", strconv.Itoa(cmd.Process.Pid)).CombinedOutput()
		t.Logf("cancellation process state (%v):\n%s", err, state)
		stacks := make([]byte, 1<<20)
		t.Logf("cancellation goroutines:\n%s", stacks[:runtime.Stack(stacks, true)])
		t.Fatal("Kubernetes command did not honor cancellation")
	}
	if got, err := os.ReadFile(cleanupMarker); err != nil || string(got) != "cleanup" {
		t.Fatalf("Kubernetes cancellation cleanup marker = %q, %v (wait: %v)", got, err, waitErr)
	}
}
