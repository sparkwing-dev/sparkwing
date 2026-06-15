package services

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// requireDocker skips the test if the docker binary is not on PATH.
// Tests that never shell out (empty-services, name-derivation, etc.)
// don't need this.
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

// containerRunning reports whether a container with the given name
// exists in the running state. Used by cleanup-verification tests.
func containerRunning(name string) bool {
	out, err := exec.Command("docker", "ps", "--filter", "name=^"+name+"$", "--format", "{{.Names}}").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == name
}

// forceRemove best-effort removes a container; used as belt-and-suspenders
// in tests that might leave one behind if the assertion fires before
// WithServices' own cleanup.
func forceRemove(name string) {
	_ = exec.Command("docker", "rm", "-f", name).Run()
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
		Image:    "nginx:alpine",
		ReadyCmd: "wget -q -O /dev/null http://localhost/ || true",
	}

	var capturedName string
	err := WithServices(context.Background(), []Service{svc}, func(ctx context.Context) error {
		out, err := exec.Command("docker", "ps", "--format", "{{.Names}}").Output()
		if err != nil {
			return err
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if strings.HasPrefix(line, "nginx-") {
				capturedName = line
				break
			}
		}
		if capturedName == "" {
			t.Fatalf("no nginx- container found while fn running; docker ps:\n%s", out)
		}
		return nil
	})
	if err != nil {
		forceRemove(capturedName)
		t.Fatalf("WithServices: %v", err)
	}
	if capturedName == "" {
		t.Fatalf("fn never saw the container")
	}
	if containerRunning(capturedName) {
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
			out, err := exec.Command("docker", "ps", "--format", "{{.Names}}").Output()
			if err != nil {
				t.Fatalf("docker ps: %v", err)
			}
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if strings.HasPrefix(line, "nginx-") {
					capturedName = line
					break
				}
			}
			panic("boom")
		})
	}()

	if capturedName == "" {
		t.Fatalf("fn never captured the container name")
	}
	for range 20 {
		if !containerRunning(capturedName) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	forceRemove(capturedName)
	t.Fatalf("container %s still running after panic through WithServices", capturedName)
}

func TestWithServices_CtxCancelCleansUp(t *testing.T) {
	requireDocker(t)

	svc := Service{
		Image:    "nginx:alpine",
		ReadyCmd: "wget -q -O /dev/null http://localhost/",
	}

	ctx, cancel := context.WithCancel(context.Background())
	var capturedName string

	err := WithServices(ctx, []Service{svc}, func(fnCtx context.Context) error {
		out, err := exec.Command("docker", "ps", "--format", "{{.Names}}").Output()
		if err != nil {
			return err
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if strings.HasPrefix(line, "nginx-") {
				capturedName = line
				break
			}
		}
		cancel()
		return fnCtx.Err()
	})

	if err == nil {
		t.Fatalf("expected ctx error, got nil")
	}
	if capturedName == "" {
		t.Fatalf("fn never captured container")
	}
	for range 20 {
		if !containerRunning(capturedName) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	forceRemove(capturedName)
	t.Fatalf("container %s still running after ctx cancel", capturedName)
}

func TestWithServices_ConcurrentNoCollision(t *testing.T) {
	requireDocker(t)

	svc := Service{
		Image:    "nginx:alpine",
		ReadyCmd: "wget -q -O /dev/null http://localhost/",
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- WithServices(context.Background(), []Service{svc}, func(ctx context.Context) error {
				time.Sleep(500 * time.Millisecond)
				return nil
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent WithServices: %v", err)
		}
	}
}
