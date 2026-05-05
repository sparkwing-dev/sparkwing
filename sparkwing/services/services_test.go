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
	// Also verify the daemon is actually reachable. `docker version`
	// against a down daemon exits non-zero; avoid flaky timeouts.
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
	// No services -> no docker interaction at all. Should run fn and
	// return its error verbatim.
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
	// If docker isn't on PATH, WithServices should return
	// ErrDockerUnavailable without calling fn. Simulate by shadowing
	// PATH to an empty dir.
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker already missing; the real-docker path can't test this branch either")
	}

	// t.Setenv restores the original value on test end automatically.
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

	// alpine:latest sleeping forever: lightweight, widely cached in
	// local docker installs, and doesn't hit the network if already
	// pulled. The test tolerates a first-run pull.
	svc := Service{
		Image: "alpine:latest",
		Name:  "", // let WithServices derive + suffix
		Port:  0,
		// Override entrypoint via env? No - alpine's default shell
		// exits immediately. Use ReadyCmd that literally calls `true`
		// so we don't rely on the container staying up beyond startup.
		// Actually we DO need it to stay up during fn. Run `sleep 3600`
		// via a docker arg. That's not expressible through our Service
		// struct today. Workaround: use a real long-running image.
	}
	// Use a minimal always-up image instead: `alpine:latest` with a
	// cmd override isn't supported by our API, so use a service that
	// stays up by itself. `alpine/socat` is heavy; a better choice is
	// to run alpine with a sleep command by stuffing it in ReadyCmd?
	// No - ReadyCmd runs INSIDE the container, so the container must
	// already be up. Drop down to busybox+entry override... we don't
	// have that either.
	//
	// Pragmatic fix: extend Service with Cmd later. For now, use
	// `alpine:latest` and let docker immediately restart-less exit;
	// ReadyCmd will fail and we'll test the cleanup path. Split the
	// "stays up" test into one that uses nginx:alpine or similar -
	// small, stays up, no config needed.
	svc.Image = "nginx:alpine"
	svc.ReadyCmd = "wget -q -O /dev/null http://localhost/ || true"

	var capturedName string
	err := WithServices(context.Background(), []Service{svc}, func(ctx context.Context) error {
		// Inspect currently-running containers to find ours. The
		// derived name starts with "nginx" (from "nginx:alpine").
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

	// nginx:alpine serves :80 almost immediately. `nc` is not present
	// in the minimal image; use wget which IS present.
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
	// Sanity: ReadyCmd path should finish well under its own timeout.
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("ReadyCmd path took %s, suspiciously slow", elapsed)
	}
}

func TestWithServices_ReadyCmdTimesOut(t *testing.T) {
	requireDocker(t)

	// ReadyCmd that always fails. 1s timeout so the test returns fast.
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
	// Give cleanup a brief moment; docker rm -f is synchronous but we
	// may be racing docker's own state machine on slow runners.
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
		// Return fnCtx.Err to simulate a ctx-aware fn that exits on
		// cancellation. WithServices must still run cleanup.
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

	// Same image, two WithServices calls in parallel. Derived names
	// must include distinct random suffixes; docker would reject the
	// second `run` on a name collision.
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
				// Hold briefly so both containers overlap.
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
