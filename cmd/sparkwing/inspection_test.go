package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func inspectionCommand(command pipelineentry.Command, directory, pipeline string) (pipelineInspection, error) {
	identity, err := pipelineIdentityAt(filepath.Join(directory, ".sparkwing"), pipeline)
	if err != nil {
		return pipelineInspection{}, err
	}
	resolved := engine.Resolved{Identity: engine.Identity{Version: "fixture", ManifestDigest: "fixture", Pin: engine.Pin{SDK: engine.GoSDK, Version: "fixture", Replacement: "test fixture"}}, Target: releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}}
	base := procenv.Base{Local: true, Captured: os.Environ(), Masker: secrets.NewMasker()}
	values := map[string]string{}
	for _, item := range base.Captured {
		name, value, _ := strings.Cut(item, "=")
		values[name] = value
	}
	snapshot, err := runspec.EnvironmentDigest(values)
	if err != nil {
		return pipelineInspection{}, err
	}
	sessions := nodehost.ProtocolSessionConfig{Environments: nodehost.SDKEnvironment{Base: base}}
	return pipelineInspection{
		planner: nodehost.SDKPlanner{Start: sessions.StartPlanning, Masker: base.Masker, EnsureEngine: func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
			return resolved, nil
		}, Log: func(context.Context, nodeproto.FrameLog) error { return nil }},
		input: nodehost.PlanningProcessInput{Environment: snapshot, Entry: &source.Entry{Directory: directory, Command: command, Engine: resolved}, Entrypoint: identity.Entrypoint, Run: nodeproto.RunContext{RunID: "inspection", Pipeline: pipeline}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}}},
	}, nil
}

func planOverProtocol(ctx context.Context, command pipelineentry.Command, directory, pipeline string, args []string, runID string) (planned, error) {
	entry, err := inspectionCommand(command, directory, pipeline)
	if err != nil {
		return planned{}, err
	}
	entry.input.Run.RunID = runID
	return planPipelineEntry(ctx, entry, pipeline, args)
}

func acquireTestSource(directory string, uncached bool) error {
	options, err := hostSourceOptions(os.Getenv("SPARKWING_HOME"), os.Environ(), secrets.NewMasker(), true)
	if err != nil {
		return err
	}
	entry, err := source.Acquire(context.Background(), source.Spec{RepoKey: "local:" + filepath.Dir(directory), LocalCheckout: filepath.Dir(directory), PipelineDir: filepath.Base(directory), NoBincache: uncached}, options)
	if entry != nil {
		err = errors.Join(err, entry.Close())
	}
	return err
}
