package nodehost

import (
	"context"
	"errors"
	"testing"
)

func TestProcessComputeEndsAfterCleanupAndKeepsOtherProcesses(t *testing.T) {
	var compute processCompute
	refused := errors.New("cleanup refused")
	start := func(run string) ExecutorProcess {
		t.Helper()
		process, err := compute.start(run, func() (ExecutorProcess, error) {
			if !compute.active(run) {
				t.Error("startup is not counted as compute")
			}
			return &executorProcess{cleanupErr: refused}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return process
	}
	first, second, foreign := start("run"), start("run"), start("other")
	if err := first.Close(); err != nil || !compute.active("run") {
		t.Fatal("closing the SDK ended compute before cleanup:", err)
	}
	if err := first.Cleanup(t.Context()); !errors.Is(err, refused) || !compute.active("run") {
		t.Fatal("one cleanup ended another process's compute:", err)
	}
	if err := second.Cleanup(t.Context()); !errors.Is(err, refused) || compute.active("run") || !compute.active("other") {
		t.Fatal("cleanup changed another run's compute:", err)
	}
	if err := foreign.Cleanup(t.Context()); !errors.Is(err, refused) || compute.active("other") {
		t.Fatal("failed cleanup retained compute:", err)
	}
	process, err := compute.start("run", func() (ExecutorProcess, error) { return nil, refused })
	if process != nil || !errors.Is(err, refused) || compute.active("run") {
		t.Fatal("failed startup retained compute:", err)
	}
}

func TestProcessComputeCountsPartialStartupUntilCleanup(t *testing.T) {
	var compute processCompute
	refused := errors.New("startup refused")
	process, err := compute.start("run", func() (ExecutorProcess, error) { return &executorProcess{}, refused })
	if process == nil || !errors.Is(err, refused) || !compute.active("run") {
		t.Fatal("partial startup lost its compute lifetime:", err)
	}
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
	if err := process.Cleanup(context.Background()); err != nil || compute.active("run") {
		t.Fatal("partial startup cleanup retained compute:", err)
	}
}
