// Package services is the sparkwing SDK's sidecar-container helper:
// spin up postgres/redis/etc. on --network=host for the duration of
// a function, wait for readiness, and guarantee cleanup on every
// exit path (normal return, error, panic, ctx cancellation).
//
//	err := services.WithServices(ctx, []services.Service{
//	    {
//	        Image:    "postgres:15",
//	        Port:     5432,
//	        Env:      map[string]string{"POSTGRES_PASSWORD": "test"},
//	        ReadyCmd: "pg_isready -h localhost -U postgres",
//	    },
//	}, func(ctx context.Context) error {
//	    return runTests(ctx)
//	})
//
// Leaf package: does not import sparkwing/ proper. Shell-outs are
// context-aware.
package services

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing/planguard"

	"github.com/sparkwing-dev/sparkwing/sparkwing/docker"
)

// ErrDockerUnavailable is returned when the `docker` binary is not on
// PATH. Re-exported from the sibling sparkwing/docker package so
// callers can errors.Is-check one sentinel regardless of helper.
var ErrDockerUnavailable = docker.ErrDockerUnavailable

// DefaultReadyTimeout is used when a Service leaves ReadyTimeout zero.
const DefaultReadyTimeout = 30 * time.Second

// readyPollInterval is how often WithServices re-runs ReadyCmd while
// waiting for a service to come up.
const readyPollInterval = 500 * time.Millisecond

// fallbackReadyWait is the crude sleep used when no ReadyCmd is set.
const fallbackReadyWait = 2 * time.Second

// Service describes a sidecar container to spin up via `docker run -d
// --network=host`. Because all services share the host network, tests
// reach each one at `localhost:<Port>`.
type Service struct {
	// Image is the fully-qualified image reference, e.g. "postgres:15-alpine".
	// Required.
	Image string

	// Name is the container name. Optional; derived from the image's
	// last path segment plus a short random suffix to prevent
	// collisions when the same pipeline runs concurrently.
	Name string

	// Port is the container port the service listens on. Informational
	// only -- with --network=host we don't publish ports explicitly.
	Port int

	// Env is the set of environment variables to pass to the container.
	Env map[string]string

	// ReadyCmd is a shell command run inside the container via
	// `docker exec`. The service is ready when this exits 0. If
	// empty, WithServices falls back to a fixed 2s sleep.
	ReadyCmd string

	// ReadyTimeout bounds how long WithServices will wait for ReadyCmd
	// to succeed. Zero means DefaultReadyTimeout (30s).
	ReadyTimeout time.Duration
}

// WithServices starts every given Service, waits for each to become
// ready, invokes fn, and then tears the services down. Cleanup runs on
// every exit path, including panic and context cancellation. The
// returned error is whichever of (startup error, readiness error, fn
// error) occurred first; cleanup errors are swallowed because the
// caller cannot act on them usefully.
//
// If services is empty, fn runs once with no docker interaction.
func WithServices(ctx context.Context, services []Service, fn func(context.Context) error) error {
	planguard.Guard(ctx, "services.WithServices")
	if len(services) == 0 {
		return fn(ctx)
	}

	if _, err := exec.LookPath("docker"); err != nil {
		return ErrDockerUnavailable
	}

	// Resolve container names up front so cleanup can target them
	// even if `docker run` fails mid-list.
	resolved := make([]Service, len(services))
	copy(resolved, services)
	for i := range resolved {
		if resolved[i].Name == "" {
			suffix, err := randomSuffix()
			if err != nil {
				return fmt.Errorf("services: random suffix: %w", err)
			}
			resolved[i].Name = deriveName(resolved[i].Image) + "-" + suffix
		}
	}

	// Track which containers we actually started so cleanup only
	// touches real ones.
	started := make([]string, 0, len(resolved))

	// Uses context.Background() deliberately: even if ctx is
	// cancelled, we still want to stop the containers we spawned.
	cleanup := func() {
		for _, name := range started {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = runDocker(cleanupCtx, "rm", "-f", name)
			cancel()
		}
	}
	defer cleanup()

	for i := range resolved {
		svc := &resolved[i]
		args := []string{"run", "-d", "--network=host", "--name", svc.Name}
		for k, v := range svc.Env {
			args = append(args, "-e", k+"="+v)
		}
		args = append(args, svc.Image)
		if err := runDocker(ctx, args...); err != nil {
			return fmt.Errorf("services: start %s (%s): %w", svc.Name, svc.Image, err)
		}
		started = append(started, svc.Name)
	}

	for i := range resolved {
		svc := &resolved[i]
		if err := waitReady(ctx, svc); err != nil {
			return fmt.Errorf("services: %s not ready: %w", svc.Name, err)
		}
	}

	// On panic the defer cleanup fires during unwind; we want the
	// panic to reach the caller with its original stack.
	return fn(ctx)
}

// waitReady polls svc.ReadyCmd (via docker exec) until it exits 0 or
// ReadyTimeout is hit. Empty ReadyCmd falls back to a fixed short
// sleep.
func waitReady(ctx context.Context, svc *Service) error {
	if svc.ReadyCmd == "" {
		timer := time.NewTimer(fallbackReadyWait)
		defer timer.Stop()
		select {
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	timeout := svc.ReadyTimeout
	if timeout <= 0 {
		timeout = DefaultReadyTimeout
	}

	deadline := time.Now().Add(timeout)
	for {
		attemptCtx, cancel := context.WithTimeout(ctx, readyPollInterval+time.Second)
		err := runDocker(attemptCtx, "exec", svc.Name, "sh", "-c", svc.ReadyCmd)
		cancel()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("ready command did not succeed within %s: %w", timeout, err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(readyPollInterval):
		}
	}
}

// deriveName produces a stable base name from an image reference,
// sanitized to the charset docker accepts in container names.
func deriveName(image string) string {
	name := image
	if i := strings.Index(name, "@"); i >= 0 {
		name = name[:i]
	}
	// LastIndex so registry:port style (e.g. "localhost:5000/foo:tag")
	// still trims correctly: only treat colon as a tag separator if
	// it's past any /.
	if i := strings.LastIndex(name, ":"); i >= 0 {
		if slash := strings.LastIndex(name, "/"); slash < 0 || i > slash {
			name = name[:i]
		}
	}
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = sanitize(name)
	if name == "" {
		name = "service"
	}
	return name
}

// sanitize keeps only characters docker allows in container names
// ([a-zA-Z0-9_.-]); anything else becomes a dash.
func sanitize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// randomSuffix returns a 6-char hex string from crypto/rand.
func randomSuffix() (string, error) {
	var buf [3]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

func runDocker(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return fmt.Errorf("docker %s: %w", strings.Join(args, " "), err)
		}
		return fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return nil
}
