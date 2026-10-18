package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func inspectTriggerSource(ctx context.Context, selection preparedTriggerSource, captured []string, options source.Options) (described []nodeproto.PipelineDescription, resultErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	masker := options.Base.Masker
	if masker == nil {
		masker = secrets.NewMasker()
	}
	defer func() {
		if resultErr != nil {
			resultErr = &maskedCLIError{err: resultErr, masker: masker}
		}
	}()
	captured = slices.DeleteFunc(slices.Clone(captured), func(item string) bool { return strings.HasPrefix(item, "SPARKWING_GATE_INDEX=") })
	options.Base = procenv.Base{Local: true, Captured: captured, Masker: masker}
	spec, err := source.ForRequest(runspec.SealedRequest{Request: runspec.Request{Git: selection.git, Source: selection.source}})
	if err != nil {
		return nil, err
	}
	if selection.checkout.directory != "" {
		spec.LocalCheckout, spec.RemoteURL, spec.CommittedCheckout = selection.checkout.directory, "", true
	}
	entry, err := source.Acquire(ctx, spec, options)
	if entry != nil {
		defer func() { resultErr = errors.Join(resultErr, entry.Close(), ctx.Err()) }()
	}
	if err := errors.Join(err, ctx.Err()); err != nil {
		return nil, err
	}
	values := make(map[string]string, len(captured))
	for _, item := range captured {
		name, value, _ := strings.Cut(item, "=")
		values[name] = value
	}
	environment, err := runspec.EnvironmentDigest(values)
	if err != nil {
		return nil, err
	}
	masked := &installDiagnostics{out: os.Stderr, stream: masker.Stream()}
	if err := errors.Join(writeEngineNotices(masked, entry.Engine.Notices), masked.close(), ctx.Err()); err != nil {
		return nil, err
	}
	processes := nodehost.ProtocolSessionConfig{Environments: nodehost.SDKEnvironment{Base: options.Base}}
	planner := nodehost.SDKPlanner{
		Start: processes.StartPlanning, EnsureEngine: options.Engines.Ensure, Masker: masker,
		Log: func(ctx context.Context, frame nodeproto.FrameLog) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			_, err := fmt.Fprintln(os.Stderr, frame.Message)
			return err
		},
	}
	prepared, err := planner.Prepare(ctx, nodehost.PlanningProcessInput{
		Entry: entry, Entrypoint: selection.source.Entrypoint, Environment: environment, Args: map[string]string{},
		Run:    nodeproto.RunContext{RunID: rand.Text(), Pipeline: selection.pipeline},
		Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}},
	}, nodehost.PlanningInput{})
	if err != nil {
		return nil, err
	}
	return []nodeproto.PipelineDescription{prepared.Description}, nil
}
