// Package services is the sparkwing SDK's sidecar-container helper:
// start sidecars for a function, wait for readiness, and clean up services
// whose startup succeeded on return, error, panic, or context cancellation.
// Declared ports bind to 127.0.0.1. Services without a port use host networking.
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
	"io"
	"net"
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

// AutoPort asks WithServices to publish a service on a free host port chosen by
// the operating system, rather than on a port the caller names.
const AutoPort = -1

const readyPollInterval = 500 * time.Millisecond

const fallbackReadyWait = 2 * time.Second

// Service describes a sidecar container started with docker run -d. A
// declared Port is published on 127.0.0.1 for host-side tests. Without
// a Port, the container uses host networking, which requires Linux.
type Service struct {
	// Image is the fully-qualified image reference, e.g. "postgres:15-alpine".
	// Required.
	Image string

	// Name is the container name. Optional; derived from the image's
	// last path segment plus a short random suffix to prevent
	// collisions when the same pipeline runs concurrently.
	Name string

	// Port is the container port the service listens on. When set, it is
	// published to 127.0.0.1 so a host process (the test) reaches it on
	// every platform incl. Docker Desktop.
	// When zero, the container uses host networking (Linux only).
	Port int

	// HostPort is the host port Port is published on. Zero publishes Port
	// itself. AutoPort asks the operating system for a free one, which
	// [WithServicesAddrs] reports back; a fixed port cannot be shared by two
	// runs on one machine, so concurrent callers pass AutoPort.
	HostPort int

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
//
// A service published on [AutoPort] needs the port it was given, which this
// signature cannot carry; call [WithServicesAddrs] instead.
func WithServices(ctx context.Context, services []Service, fn func(context.Context) error) error {
	return withServices(ctx, "services.WithServices", services, func(ctx context.Context, _ []Addr) error {
		return fn(ctx)
	})
}

// Addr is where one service ended up, in the order the services were given.
type Addr struct {
	// Name is the container name, which WithServicesAddrs derives when the
	// caller leaves [Service.Name] empty.
	Name string

	// HostPort is the port on 127.0.0.1 the service is published on, or zero
	// for a service using host networking.
	HostPort int
}

// WithServicesAddrs is [WithServices] that also hands fn where each service was
// published, one Addr per service in the order given. A service asking for
// [AutoPort] reads its port here, which is the only place it is reported.
func WithServicesAddrs(ctx context.Context, services []Service, fn func(context.Context, []Addr) error) error {
	return withServices(ctx, "services.WithServicesAddrs", services, fn)
}

func withServices(ctx context.Context, guard string, services []Service, fn func(context.Context, []Addr) error) error {
	planguard.Guard(ctx, guard)
	if len(services) == 0 {
		return fn(ctx, nil)
	}

	if _, err := exec.LookPath("docker"); err != nil {
		return ErrDockerUnavailable
	}

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

	release, err := resolveHostPorts(resolved)
	if err != nil {
		return err
	}
	defer release()

	addrs := make([]Addr, len(resolved))
	for i := range resolved {
		addrs[i] = Addr{Name: resolved[i].Name, HostPort: publishedPort(resolved[i])}
	}

	started := make([]string, 0, len(resolved))

	// hack: cleanup uses context.Background() so cancellation of the caller's ctx does not abort container removal.
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
		args := []string{"run", "-d", "--name", svc.Name}
		if svc.Port > 0 {
			// hack: bind to 127.0.0.1 so Docker Desktop (macOS/Windows) containers are reachable from the host.
			args = append(args, "-p", fmt.Sprintf("127.0.0.1:%d:%d", publishedPort(*svc), svc.Port))
		} else {
			// hack: no port declared; host networking only works on Linux.
			args = append(args, "--network=host")
		}
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

	// safety: the probe listeners are closed before fn runs, so a service that
	// failed to bind its port surfaces to fn rather than to the next caller.
	release()

	// safety: fn is the last statement so a panic unwinds through defer cleanup without re-wrapping the stack.
	return fn(ctx, addrs)
}

func publishedPort(svc Service) int {
	if svc.Port <= 0 {
		return 0
	}
	if svc.HostPort != 0 {
		return svc.HostPort
	}
	return svc.Port
}

// safety: each probe listener stays open until every port is chosen, so two
// services in one call cannot be handed the same port.
func resolveHostPorts(services []Service) (func(), error) {
	var probes []io.Closer
	release := func() {
		for _, probe := range probes {
			_ = probe.Close()
		}
		probes = nil
	}
	for i := range services {
		if services[i].HostPort != AutoPort {
			continue
		}
		if services[i].Port <= 0 {
			release()
			return nil, fmt.Errorf("services: %s asks for a host port without declaring a container Port", services[i].Name)
		}
		probe, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			release()
			return nil, fmt.Errorf("services: reserve a host port for %s: %w", services[i].Name, err)
		}
		probes = append(probes, probe)
		addr, ok := probe.Addr().(*net.TCPAddr)
		if !ok {
			release()
			return nil, fmt.Errorf("services: reserve a host port for %s: listener reported %T", services[i].Name, probe.Addr())
		}
		services[i].HostPort = addr.Port
	}
	return release, nil
}

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

func deriveName(image string) string {
	name := image
	if i := strings.Index(name, "@"); i >= 0 {
		name = name[:i]
	}
	// hack: LastIndex so "registry:port/image:tag" only strips the image tag, not the registry port.
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
