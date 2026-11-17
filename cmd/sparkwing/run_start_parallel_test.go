package main

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestLocalRunOverlapsControllerStartupAndPreparation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls []string
		deps := localRuntimeFor(t, localTestAward(t), true, &calls)
		deps.interrupts = make(chan os.Signal)
		resolve := deps.resolve
		preparing, starting := make(chan struct{}), make(chan struct{})
		prepared, ready, submitted := make(chan struct{}), make(chan struct{}), make(chan struct{})
		deps.resolve = func(ctx context.Context, options localRunOptions) (localRunRuntime, error) {
			runtime, err := resolve(ctx, options)
			prepare := runtime.prepare
			runtime.prepare = func(ctx context.Context, pipeline string, flags runFlags) (localRunPreparation, error) {
				close(preparing)
				<-prepared
				return prepare(ctx, pipeline, flags)
			}
			starts := 0
			runtime.ensureController = func(context.Context) error {
				starts++
				if starts == 1 {
					close(starting)
					<-ready
				}
				return nil
			}
			controller := runtime.controller.(localTestController)
			create := controller.create
			controller.create = func(ctx context.Context, request controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
				close(submitted)
				return create(ctx, request)
			}
			runtime.controller = controller
			return runtime, err
		}
		done := make(chan error, 1)
		go func() {
			done <- runLocalCommand(t.Context(), "ci", runFlags{detached: true}, []string{"--password", "private-value"}, io.Discard, deps)
		}()
		<-preparing
		synctest.Wait()
		select {
		case <-starting:
		default:
			t.Error("controller startup waited for source preparation")
		}
		close(prepared)
		synctest.Wait()
		select {
		case <-submitted:
			t.Error("run submitted before controller readiness")
		default:
		}
		close(ready)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestLocalPreparationFailureJoinsControllerStartupBeforeClose(t *testing.T) {
	var calls []string
	deps := localRuntimeFor(t, localTestAward(t), true, &calls)
	deps.interrupts = make(chan os.Signal)
	resolve := deps.resolve
	starting, joined := make(chan struct{}), make(chan struct{})
	failure := errors.New("source preparation refused")
	deps.resolve = func(ctx context.Context, options localRunOptions) (localRunRuntime, error) {
		runtime, err := resolve(ctx, options)
		runtime.ensureController = func(ctx context.Context) error {
			close(starting)
			defer close(joined)
			<-ctx.Done()
			return ctx.Err()
		}
		runtime.prepare = func(context.Context, string, runFlags) (localRunPreparation, error) {
			select {
			case <-starting:
			case <-ctx.Done():
				return localRunPreparation{}, ctx.Err()
			}
			return localRunPreparation{}, failure
		}
		runtime.close = func() error {
			select {
			case <-joined:
			default:
				t.Error("runtime closed before controller startup joined")
			}
			return nil
		}
		return runtime, err
	}
	if err := runLocalCommand(t.Context(), "ci", runFlags{detached: true}, nil, io.Discard, deps); !errors.Is(err, failure) {
		t.Fatalf("preparation failure lost: %v", err)
	}
}

func TestLocalRunRechecksControllerAfterPreparation(t *testing.T) {
	var calls []string
	deps := localRuntimeFor(t, localTestAward(t), true, &calls)
	deps.interrupts = make(chan os.Signal)
	resolve := deps.resolve
	started := make(chan struct{})
	retired := errors.New("controller retired during source preparation")
	deps.resolve = func(ctx context.Context, options localRunOptions) (localRunRuntime, error) {
		runtime, err := resolve(ctx, options)
		starts := 0
		runtime.ensureController = func(context.Context) error {
			starts++
			if starts == 1 {
				close(started)
				return nil
			}
			return retired
		}
		prepare := runtime.prepare
		runtime.prepare = func(ctx context.Context, pipeline string, flags runFlags) (localRunPreparation, error) {
			<-started
			return prepare(ctx, pipeline, flags)
		}
		return runtime, err
	}
	err := runLocalCommand(t.Context(), "ci", runFlags{detached: true}, []string{"--password", "private-value"}, io.Discard, deps)
	if !errors.Is(err, retired) {
		t.Fatalf("readiness after preparation was not checked: %v", err)
	}
	for _, call := range calls {
		if call == "create" {
			t.Fatal("submitted to a controller that lost readiness during preparation")
		}
	}
}
