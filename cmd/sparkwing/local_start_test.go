package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

func TestStartLocalControllerUsesCapturedLifecycle(t *testing.T) {
	home := t.TempDir()
	lifecycle := &daemonLifecycleFake{}
	options := wingd.LocalStartOptions{Home: home, Executable: "/captured/sparkwing", Version: "v0.71.0", Stay: true}
	err := startLocalController(t.Context(), filepath.Join(home, "."), func(context.Context) (dashboardServices, error) {
		return dashboardServices{lifecycle: lifecycle, options: options}, nil
	})
	if err != nil || len(lifecycle.calls) != 1 || lifecycle.calls[0] != "start" || lifecycle.options != options {
		t.Fatalf("startup: %+v, %v", lifecycle, err)
	}
}

func TestStartLocalControllerPreservesFailures(t *testing.T) {
	failure := errors.New("controller startup failed")
	for _, mode := range []string{"resolve", "missing", "home", "start", "stopped", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			lifecycle := &daemonLifecycleFake{}
			services := dashboardServices{lifecycle: lifecycle, options: wingd.LocalStartOptions{Home: home}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch mode {
			case "missing":
				services.lifecycle = nil
			case "home":
				services.options.Home = t.TempDir()
			case "start":
				lifecycle.failAt, lifecycle.err = "start", failure
			case "stopped":
				lifecycle.started = &wingd.LocalStatus{}
			case "cancelled":
				cancel()
			}
			resolved := false
			err := startLocalController(ctx, home, func(context.Context) (dashboardServices, error) {
				resolved = true
				if mode == "resolve" {
					return dashboardServices{}, failure
				}
				return services, nil
			})
			if err == nil {
				t.Fatal("startup failure was accepted")
			}
			if (mode == "resolve" || mode == "start") && !errors.Is(err, failure) {
				t.Fatalf("lost startup failure: %v", err)
			}
			if mode == "cancelled" && (!errors.Is(err, context.Canceled) || resolved) {
				t.Fatalf("cancelled startup resolved=%v: %v", resolved, err)
			}
			if (mode == "missing" || mode == "home" || mode == "cancelled") && len(lifecycle.calls) != 0 {
				t.Fatalf("invalid startup reached lifecycle: %v", lifecycle.calls)
			}
		})
	}
}

func TestSeparateConsumerCommandRemoved(t *testing.T) {
	for _, action := range []string{"start", "status", "stop"} {
		if err := runJobs([]string{"consumer", action}); err == nil {
			t.Fatalf("removed consumer %s accepted", action)
		}
	}
}
