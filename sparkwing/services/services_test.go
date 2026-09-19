package services

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH; skipping container smoke test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}").Run(); err != nil {
		t.Skip("docker daemon not reachable; skipping container smoke test")
	}
}

func containerRunning(ctx context.Context, name string) (bool, error) {
	out, err := exec.CommandContext(ctx, "docker", "ps", "--filter", "name=^"+name+"$", "--format", "{{.Names}}").Output()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == name, nil
}

func testServiceName(t *testing.T) string {
	t.Helper()
	suffix, err := randomSuffix()
	if err != nil {
		t.Fatal(err)
	}
	return "services-test-" + sanitize(t.Name()) + "-" + suffix
}

func captureRunningService(t *testing.T, parent context.Context, name string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	running, err := containerRunning(ctx, name)
	if err != nil || !running {
		t.Fatalf("owned container %s is not running: %v", name, err)
	}
	return name
}

func forceRemove(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
}

func waitForContainerStopped(t *testing.T, name, cause string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	poll := time.NewTicker(100 * time.Millisecond)
	defer poll.Stop()
	for {
		running, err := containerRunning(ctx, name)
		if err != nil {
			forceRemove(name)
			t.Fatalf("inspect container %s after %s: %v", name, cause, err)
		}
		if !running {
			return
		}
		select {
		case <-poll.C:
		case <-ctx.Done():
			forceRemove(name)
			t.Fatalf("container %s still running after %s", name, cause)
		}
	}
}

func TestDeriveName(t *testing.T) {
	cases := []struct {
		image string
		want  string
	}{
		{"postgres:15", "postgres"},
		{"postgres:15-alpine", "postgres"},
		{"redis:7", "redis"},
		{"ghcr.io/owner/repo:v1.2.3", "repo"},
		{"localhost:5000/my-svc:latest", "my-svc"},
		{"alpine", "alpine"},
		{"alpine@sha256:abcdef", "alpine"},
		{"registry.example.com/team/name:tag@sha256:deadbeef", "name"},
	}
	for _, c := range cases {
		t.Run(c.image, func(t *testing.T) {
			got := deriveName(c.image)
			if got != c.want {
				t.Fatalf("deriveName(%q) = %q, want %q", c.image, got, c.want)
			}
		})
	}
}

func TestWithServices_Empty(t *testing.T) {
	calls := 0
	err := WithServices(context.Background(), nil, func(ctx context.Context) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("WithServices(nil) = %v, want nil", err)
	}
	if calls != 1 {
		t.Fatalf("fn called %d times, want 1", calls)
	}

	sentinel := errors.New("boom")
	err = WithServices(context.Background(), []Service{}, func(ctx context.Context) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}
}

func TestWithServices_DockerMissing(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker already missing; the real-docker path can't test this branch either")
	}

	t.Setenv("PATH", t.TempDir())

	called := false
	err := WithServices(context.Background(), []Service{{Image: "alpine:latest"}}, func(ctx context.Context) error {
		called = true
		return nil
	})

	if !errors.Is(err, ErrDockerUnavailable) {
		t.Fatalf("err = %v, want ErrDockerUnavailable", err)
	}
	if called {
		t.Fatalf("fn was called despite missing docker")
	}
}

func TestWithServices_StartAndCleanup(t *testing.T) {
	requireDocker(t)

	svc := Service{
		Name:     testServiceName(t),
		Image:    "nginx:alpine",
		ReadyCmd: "wget -q -O /dev/null http://localhost/ || true",
	}

	var capturedName string
	err := WithServices(context.Background(), []Service{svc}, func(ctx context.Context) error {
		capturedName = captureRunningService(t, ctx, svc.Name)
		return nil
	})
	if err != nil {
		forceRemove(capturedName)
		t.Fatalf("WithServices: %v", err)
	}
	if capturedName == "" {
		t.Fatalf("fn never saw the container")
	}
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelProbe()
	running, probeErr := containerRunning(probeCtx, capturedName)
	if probeErr != nil {
		forceRemove(capturedName)
		t.Fatalf("inspect container %s after WithServices returned: %v", capturedName, probeErr)
	}
	if running {
		forceRemove(capturedName)
		t.Fatalf("container %s still running after WithServices returned", capturedName)
	}
}

func TestWithServices_ReadyCmdSucceeds(t *testing.T) {
	requireDocker(t)

	svc := Service{
		Image:        "nginx:alpine",
		ReadyCmd:     "wget -q -O /dev/null http://localhost/",
		ReadyTimeout: 15 * time.Second,
	}
	start := time.Now()
	err := WithServices(context.Background(), []Service{svc}, func(ctx context.Context) error {
		return nil
	})
	if err != nil {
		t.Fatalf("WithServices: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("ReadyCmd path took %s, suspiciously slow", elapsed)
	}
}

func TestWithServices_ReadyCmdTimesOut(t *testing.T) {
	requireDocker(t)

	svc := Service{
		Image:        "nginx:alpine",
		ReadyCmd:     "false",
		ReadyTimeout: 1 * time.Second,
	}
	err := WithServices(context.Background(), []Service{svc}, func(ctx context.Context) error {
		t.Fatalf("fn should not run when ReadyCmd never succeeds")
		return nil
	})
	if err == nil {
		t.Fatalf("expected readiness timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("error %q does not mention readiness", err)
	}
}

func TestWithServices_PanicStillCleansUp(t *testing.T) {
	requireDocker(t)

	svc := Service{
		Name:     testServiceName(t),
		Image:    "nginx:alpine",
		ReadyCmd: "wget -q -O /dev/null http://localhost/",
	}

	var capturedName string
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatalf("expected panic, got none")
			}
		}()
		_ = WithServices(context.Background(), []Service{svc}, func(ctx context.Context) error {
			capturedName = captureRunningService(t, ctx, svc.Name)
			panic("boom")
		})
	}()

	if capturedName == "" {
		t.Fatalf("fn never captured the container name")
	}
	waitForContainerStopped(t, capturedName, "panic through WithServices")
}

func TestWithServices_CtxCancelCleansUp(t *testing.T) {
	requireDocker(t)

	svc := Service{
		Name:     testServiceName(t),
		Image:    "nginx:alpine",
		ReadyCmd: "wget -q -O /dev/null http://localhost/",
	}

	ctx, cancel := context.WithCancel(context.Background())
	var capturedName string

	err := WithServices(ctx, []Service{svc}, func(fnCtx context.Context) error {
		capturedName = captureRunningService(t, fnCtx, svc.Name)
		cancel()
		return fnCtx.Err()
	})

	if err == nil {
		t.Fatalf("expected ctx error, got nil")
	}
	if capturedName == "" {
		t.Fatalf("fn never captured container")
	}
	waitForContainerStopped(t, capturedName, "context cancellation")
}

func TestWithServices_ConcurrentNoCollision(t *testing.T) {
	requireDocker(t)

	svc := Service{
		Image:    "nginx:alpine",
		ReadyCmd: "wget -q -O /dev/null http://localhost/",
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseCallbacks := func() { releaseOnce.Do(func() { close(release) }) }
	defer wg.Wait()
	defer releaseCallbacks()
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- WithServices(context.Background(), []Service{svc}, func(ctx context.Context) error {
				entered <- struct{}{}
				<-release
				return nil
			})
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case err := <-errs:
			t.Fatalf("service failed before its callback entered: %v", err)
		}
	}
	releaseCallbacks()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent WithServices: %v", err)
		}
	}
}

func TestCleanupFixturesDoNotRemoveUnrelatedContainers(t *testing.T) {
	stubDir := t.TempDir()
	state := t.TempDir()
	t.Setenv("SERVICE_TEST_STATE", state)
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	stub := `#!/bin/sh
set -eu
case "$1" in
version) exit 0 ;;
run)
  shift
  while [ "$1" != "--name" ]; do shift; done
  printf '%s' "$2" > "$SERVICE_TEST_STATE/owned"
  ;;
exec) exit 0 ;;
ps)
  filter=""
  shift
  while [ "$#" -gt 0 ]; do
    if [ "$1" = "--filter" ]; then filter="$2"; shift; fi
    shift
  done
  if [ -z "$filter" ] || [ "$filter" = 'name=^nginx-unrelated$' ]; then
    printf '%s\n' nginx-unrelated
  fi
  if [ -f "$SERVICE_TEST_STATE/owned" ]; then
    owned=$(cat "$SERVICE_TEST_STATE/owned")
    if [ -z "$filter" ] || [ "$filter" = "name=^$owned\$" ]; then
      printf '%s\n' "$owned"
    fi
  fi
  ;;
rm)
  if [ "$3" = nginx-unrelated ]; then
    touch "$SERVICE_TEST_STATE/unrelated-removed"
  elif [ -f "$SERVICE_TEST_STATE/owned" ] && [ "$3" = "$(cat "$SERVICE_TEST_STATE/owned")" ]; then
    rm "$SERVICE_TEST_STATE/owned"
  fi
  ;;
*) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(stubDir, "docker"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, run := range map[string]func(*testing.T){
		"return": TestWithServices_StartAndCleanup,
		"panic":  TestWithServices_PanicStillCleansUp,
		"cancel": TestWithServices_CtxCancelCleansUp,
	} {
		t.Run(name, run)
	}
	if _, err := os.Stat(filepath.Join(state, "unrelated-removed")); !os.IsNotExist(err) {
		t.Fatalf("fixture removed an unrelated container: %v", err)
	}
}
