//go:build !windows

package jobs

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	ready := bufio.NewScanner(stdout)
	if !ready.Scan() || ready.Text() != "ready" {
		t.Fatalf("missing cancellation readiness: %v", ready.Err())
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	cancel()
	select {
	case <-done:
	case <-bound.Done():
		t.Fatal("Kubernetes command did not honor cancellation")
	}
	if got, err := os.ReadFile(cleanupMarker); err != nil || string(got) != "cleanup" {
		t.Fatalf("Kubernetes cancellation cleanup marker = %q, %v", got, err)
	}
}
