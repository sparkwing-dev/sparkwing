//go:build unix

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func burstPublicPersonalRunner(t *testing.T, ctx context.Context, cli, repo string, state *store.Store) string {
	t.Helper()
	session, _, _, err := state.AsOperator().CreateAccountSession(ctx, store.Account{ID: "burst-user"}, "default", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	runnerHome := t.TempDir()
	runnerState := filepath.Join(runnerHome, "state")
	config := filepath.Join(runnerHome, "runner.yaml")
	enroll := exec.CommandContext(ctx, cli, "runners", "add", "--personal", "--session-stdin", "--no-service", "--profile", "cloud", "--runner", "burst-desktop", "--max-concurrent", "1", "--config", config, "-o=json")
	enroll.Dir, enroll.Stdin = repo, strings.NewReader(session+"\n")
	if output, err := enroll.CombinedOutput(); err != nil {
		t.Fatalf("public personal-machine enrollment: %v\n%s", err, output)
	}
	environment := append(os.Environ(), "HOME="+runnerHome, "SPARKWING_HOME="+runnerState,
		"XDG_CONFIG_HOME="+filepath.Join(runnerHome, "config"), "XDG_CACHE_HOME="+filepath.Join(runnerHome, "cache"), "SPARKWING_CONFIG="+config)
	offlineStopDaemon(t, runnerState) //nolint:contextcheck // Cleanup must outlive runner cancellation.
	ready := exec.CommandContext(ctx, cli, "runs", "list", "-o=json")
	ready.Env, ready.Dir = environment, repo
	if output, err := ready.CombinedOutput(); err != nil {
		t.Fatalf("personal machine controller startup: %v\n%s", err, output)
	}
	runner := exec.CommandContext(ctx, cli, "__test-runner", "start", "--config", config)
	runner.Env = environment
	stream := &burstCLIOutput{}
	runner.Stdout, runner.Stderr = stream, stream
	if err := runner.Start(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- runner.Wait() }()
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = runner.Process.Signal(os.Interrupt)
		select {
		case <-finished:
		case <-cleanup.Done():
			_ = runner.Process.Kill()
			<-finished
			t.Error("registered runner did not drain after interrupt")
		}
		t.Logf("registered runner output: %s", stream.String())
	})
	return config
}

type burstCLIOutput struct {
	mu     sync.Mutex
	once   sync.Once
	bytes  bytes.Buffer
	ready  chan struct{}
	marker string
}

func (w *burstCLIOutput) Write(body []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.bytes.Write(body)
	if w.ready != nil && strings.Contains(w.bytes.String(), w.marker) {
		w.once.Do(func() { close(w.ready) })
	}
	return n, err
}

func (w *burstCLIOutput) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.bytes.String()
}
