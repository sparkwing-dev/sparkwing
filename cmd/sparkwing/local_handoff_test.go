package main

import (
	"context"
	"io"
	"testing"
)

func TestLocalRunRetainsInstallationThroughObservation(t *testing.T) {
	var calls []string
	deps := localRuntimeFor(t, localTestAward(t), false, &calls)
	resolve := deps.resolve
	released := false
	deps.resolve = func(ctx context.Context, options localRunOptions) (localRunRuntime, error) {
		runtime, err := resolve(ctx, options)
		prepare := runtime.prepare
		runtime.prepare = func(ctx context.Context, pipeline string, flags runFlags) (localRunPreparation, error) {
			prepared, err := prepare(ctx, pipeline, flags)
			prepared.close = func() error { released = true; return nil }
			return prepared, err
		}
		follow := runtime.follow
		runtime.follow = func(ctx context.Context, run, format string, out io.Writer, hosted <-chan struct{}) error {
			if released {
				t.Error("installation released before observation finished")
			}
			return follow(ctx, run, format, out, hosted)
		}
		return runtime, err
	}
	if err := runLocalCommand(t.Context(), "ci", runFlags{}, []string{"--password", "private-value"}, io.Discard, deps); err != nil {
		t.Fatal(err)
	}
	if !released {
		t.Fatal("installation survived its invocation")
	}
}
