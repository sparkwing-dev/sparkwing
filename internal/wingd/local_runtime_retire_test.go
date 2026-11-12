package wingd

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
)

type retiringRuntimeController struct {
	*runtimeControllerFixture
	LocalRunnerStore
}

type runtimeRetirement struct {
	runnerHTTPFixture
	controller *runtimeControllerFixture
	failure    error
	drained    bool
}

func (f *runtimeRetirement) DrainLocalRunner(ctx context.Context, subject, id string) error {
	if f.controller.closed.Load() {
		return errors.New("controller closed before runner retirement")
	}
	f.drained = true
	if f.failure != nil {
		return f.failure
	}
	return f.runnerHTTPFixture.DrainLocalRunner(ctx, subject, id)
}

func TestStandaloneCloseRetiresRunnerBeforeController(t *testing.T) {
	for _, standalone := range []bool{true, false} {
		t.Run(map[bool]string{true: "standalone", false: "replacement"}[standalone], func(t *testing.T) {
			f := &runtimeControllerFixture{}
			r := newRuntimeFixture(t, f)
			registry := &runtimeRetirement{controller: f, runnerHTTPFixture: runnerHTTPFixture{instance: localhostwire.LocalRunnerInstance{ID: "runner", PID: os.Getpid(), ProcessBirth: "retired-incarnation", State: "active"}}}
			r.cfg.Open = func(context.Context, string) (LocalController, error) {
				return &retiringRuntimeController{f, registry}, nil
			}
			lifecycle := NewLocalLifecycle(r, nil)
			if _, err := lifecycle.EnsureStarted(t.Context(), LocalStartOptions{Home: f.home, Version: "test", Standalone: standalone}); err != nil {
				t.Fatal(err)
			}
			if err := lifecycle.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
			if registry.drained != standalone {
				t.Fatalf("runner retirement=%v, standalone=%v", registry.drained, standalone)
			}
			if !f.closed.Load() {
				t.Fatal("controller did not close")
			}
		})
	}
}

func TestStandaloneCloseKeepsControllerForFailedRetirement(t *testing.T) {
	f := &runtimeControllerFixture{}
	r := newRuntimeFixture(t, f)
	failure := errors.New("runner retirement refused")
	registry := &runtimeRetirement{controller: f, failure: failure}
	r.cfg.Open = func(context.Context, string) (LocalController, error) {
		return &retiringRuntimeController{f, registry}, nil
	}
	lifecycle := NewLocalLifecycle(r, nil)
	if _, err := lifecycle.EnsureStarted(t.Context(), LocalStartOptions{Home: f.home, Version: "test", Standalone: true}); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Stop(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("retirement error=%v", err)
	}
	if f.closed.Load() {
		t.Fatal("controller closed before runner retirement succeeded")
	}
	registry.failure = nil
	registry.instance = localhostwire.LocalRunnerInstance{PID: os.Getpid(), ProcessBirth: "retired-incarnation"}
	if err := lifecycle.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}
