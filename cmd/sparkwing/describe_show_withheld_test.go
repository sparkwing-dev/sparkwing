package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestPipelineDescribeRevealsWithheldErrorOnlyWithTheFlag(t *testing.T) {
	const cause = "private planning cause"
	checkout := t.TempDir()
	if err := os.MkdirAll(filepath.Join(checkout, ".sparkwing"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, filepath.Join(checkout, ".sparkwing", "sparkwing.yaml"), "pipelines:\n  - name: build\n    entrypoint: Build\n")
	withEntry := func(_ string, inspect func(context.Context, pipelineInspection, string) error) error {
		return inspect(t.Context(), pipelineInspection{
			planner: nodehost.SDKPlanner{
				Masker: secrets.NewMasker(),
				Log:    func(context.Context, nodewire.FrameLog) error { return nil },
				EnsureEngine: func(context.Context, engineid.Identity, releaseasset.Target) (engine.Resolved, error) {
					return engine.Resolved{}, nil
				},
				Start: func(context.Context, nodehost.PlanningProcessInput, func(context.Context, nodewire.FrameLog) error) (nodehost.ExecutorProcess, error) {
					return nil, errors.New(cause)
				},
			},
			input: nodehost.PlanningProcessInput{Entry: &source.Entry{}, Entrypoint: "Build", Run: nodewire.RunContext{RunID: "inspection", Pipeline: "build"}},
		}, checkout)
	}
	for _, show := range []bool{false, true} {
		args := []string{"pipeline", "describe", "--pipeline", "build", "-o", "json"}
		if show {
			args = append(args, "--show-withheld")
		}
		inv, err := clisurface.Parse("sparkwing", args)
		if err != nil {
			t.Fatal(err)
		}
		err = runPipelineDescribeCommand(inv, withEntry)
		var failure *apierr.Error
		if !errors.As(err, &failure) {
			t.Fatalf("show=%t: describe error = %v", show, err)
		}
		if show != strings.Contains(failure.Message, cause) {
			t.Fatalf("show=%t: cause shown=%t: %+v", show, strings.Contains(failure.Message, cause), failure)
		}
		if !show && strings.Count(failure.Message+failure.Hint, "sparkwing pipeline describe --pipeline build --show-withheld") != 1 {
			t.Fatalf("withheld error does not name the reveal command: %+v", failure)
		}
	}
}
