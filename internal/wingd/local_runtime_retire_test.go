package wingd

import (
	"context"
	"errors"
	"os"
	"testing"
	"testing/synctest"

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
	registry := &runtimeRetirement{controller: f, failure: failure, runnerHTTPFixture: runnerHTTPFixture{instance: localhostwire.LocalRunnerInstance{ID: "runner", PID: os.Getpid(), ProcessBirth: "retired-incarnation", State: "active"}}}
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
	if err := lifecycle.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestStandaloneStopRetryKeepsPersistedDrainingRunnerAlive(t *testing.T) {
	birth, err := admissionProcessBirth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	f := &runtimeControllerFixture{}
	r := newRuntimeFixture(t, f)
	registry := &runnerSetFixture{instances: map[string]localhostwire.LocalRunnerInstance{
		"held": {ID: "held", PID: os.Getpid(), ProcessBirth: birth, State: "active"},
	}}
	r.cfg.Open = func(context.Context, string) (LocalController, error) {
		return &retiringRuntimeController{f, registry}, nil
	}
	lifecycle := NewLocalLifecycle(r, nil)
	if _, err := lifecycle.EnsureStarted(t.Context(), LocalStartOptions{Home: f.home, Version: "test", Standalone: true}); err != nil {
		t.Fatal(err)
	}
	defer func() { r.run.cancel(); <-r.run.done }()
	synctest.Test(t, func(t *testing.T) {
		for attempt := 1; attempt <= 2; attempt++ {
			err := lifecycle.Stop(t.Context())
			if registry.state("held") != "draining" {
				t.Fatal("drain was not persisted")
			}
			if err == nil || f.closed.Load() {
				t.Fatalf("stop attempt %d closed controller while draining runner lived: %v", attempt, err)
			}
		}
	})
}
