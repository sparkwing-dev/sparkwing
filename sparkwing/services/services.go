// Package services manages host-owned sidecars for a callback.
package services

import (
	"context"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/sparkwing/docker"
)

var ErrDockerUnavailable = docker.ErrDockerUnavailable

const DefaultReadyTimeout = 30 * time.Second
const AutoPort = -1

type Service struct {
	// Image is the fully-qualified image reference, e.g. "postgres:15-alpine".
	// Required.
	Image string `json:"image"`
	// Name is the container name. Optional; derived from the image's
	// last path segment plus a short random suffix to prevent
	// collisions when the same pipeline runs concurrently.
	Name string `json:"name"`
	// Port is the container port the service listens on. When zero, the
	// container uses host networking (Linux only).
	Port int `json:"port"`
	// HostPort is the host port on 127.0.0.1 that Port is published on. Zero
	// publishes Port itself; AutoPort takes a free one, which only
	// [WithServicesAddrs] reports back.
	HostPort int `json:"hostPort"`
	// Env is the set of environment variables to pass to the container.
	Env map[string]string `json:"env"`
	// ReadyCmd is a shell command run inside the container via
	// `docker exec`. The service is ready when this exits 0. If
	// empty, WithServices falls back to a fixed 2s sleep.
	ReadyCmd string `json:"readyCmd"`
	// ReadyTimeout bounds how long WithServices will wait for ReadyCmd
	// to succeed. Zero means DefaultReadyTimeout (30s).
	ReadyTimeout time.Duration `json:"readyTimeout"`
}

type Addr struct {
	Name string `json:"name"`
	// HostPort is zero for a service using host networking.
	HostPort int `json:"hostPort"`
}

// WithServices starts sidecars, runs fn, and joins host cleanup.
func WithServices(ctx context.Context, specs []Service, fn func(context.Context) error) error {
	return WithServicesAddrs(ctx, specs, func(ctx context.Context, _ []Addr) error { return fn(ctx) })
}

// WithServicesAddrs supplies host-selected addresses to fn.
func WithServicesAddrs(ctx context.Context, specs []Service, fn func(context.Context, []Addr) error) (err error) {
	var owned []struct {
		Addr
		CleanupID string `json:"cleanup_id"`
	}
	if err = sdkserve.Helper(ctx, "services.Start", []any{specs}, &owned); err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, sdkserve.Helper(context.WithoutCancel(ctx), "services.Stop", []any{owned}, nil))
	}()
	addrs := make([]Addr, len(owned))
	for i, value := range owned {
		addrs[i] = value.Addr
	}
	return fn(ctx, addrs)
}
