package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/toolchain"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

const toolchainModeEnv = "SPARKWING_TOOLCHAIN"

var (
	toolchainReplayArgs      []string
	toolchainReplayDirectory string
)

var (
	readEnginePin    = engine.ReadPin
	enginePinVersion = engine.VersionForPin
)

type pipelineEngineSwitcher struct {
	readPin     func(string, engineid.SDKKind) (engineid.Pin, error)
	versionFor  func(engineid.Pin) (string, error)
	resolve     func(context.Context, engineid.Pin, releaseasset.Target, toolchain.Mode) (engine.Resolved, error)
	execute     func(string, []string, []string) error
	current     string
	target      releaseasset.Target
	diagnostics io.Writer
}

func switchToolchain(dir string, beforeExec func()) error {
	ctx, stop, _ := interruptContext()
	defer stop()
	typescript, err := pipelineentry.IsTypeScript(dir)
	if err != nil {
		return err
	}
	sdk := engineid.GoSDK
	if typescript {
		sdk = engineid.TypeScriptSDK
	}
	current, err := os.Executable()
	if err != nil {
		return err
	}
	command := pipelineEngineSwitcher{
		readPin: readEnginePin, versionFor: enginePinVersion,
		current: current, target: releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH},
		execute: func(binary string, args, environment []string) error {
			if beforeExec != nil {
				beforeExec()
			}
			return replayToolchain(binary, args, environment)
		}, diagnostics: os.Stderr,
		resolve: func(ctx context.Context, pin engineid.Pin, target releaseasset.Target, mode toolchain.Mode) (engine.Resolved, error) {
			resolver, err := engine.NewResolver(engine.Config{BuildVersion: installedVersion(), Mode: mode, LocalOnly: true})
			if err != nil {
				return engine.Resolved{}, err
			}
			return resolver.Resolve(ctx, pin, target)
		},
	}
	return command.run(ctx, dir, sdk, os.Getenv(toolchainModeEnv), toolchainArgs(), os.Environ())
}

func replayToolchain(binary string, args, environment []string) (result error) {
	if toolchainReplayDirectory != "" {
		previous, err := os.Getwd()
		if err != nil {
			return err
		}
		if err := os.Chdir(toolchainReplayDirectory); err != nil {
			return err
		}
		defer func() { result = errors.Join(result, os.Chdir(previous)) }()
	}
	return execToolchain(binary, args, environment)
}

func (c pipelineEngineSwitcher) run(ctx context.Context, dir string, sdk engineid.SDKKind, rawMode string, args, environment []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	mode, err := toolchain.ParseMode(rawMode)
	if err != nil {
		return &apierr.Error{Code: apierr.InvalidArgs, Message: err.Error()}
	}
	pin, err := c.readPin(dir, sdk)
	if err := errors.Join(err, ctx.Err()); err != nil {
		return err
	}
	version, err := c.versionFor(pin)
	if err := errors.Join(err, ctx.Err()); err != nil {
		return err
	}
	selected, err := c.resolve(ctx, pin, c.target, mode)
	if err := errors.Join(err, ctx.Err()); err != nil {
		return err
	}
	if pin.SDK != sdk || selected.Identity.Pin != pin || selected.Identity.Version != version ||
		selected.Identity.ManifestDigest == "" || selected.AssetDigest == "" ||
		selected.Target != c.target || !filepath.IsAbs(selected.Executable) {
		return &apierr.Error{Code: apierr.ProtocolMismatch, Message: "engine resolver returned a different pin or incomplete verified executable"}
	}
	if err := errors.Join(writeEngineNotices(c.diagnostics, selected.Notices), ctx.Err()); err != nil {
		return err
	}
	current, err := os.Stat(c.current)
	if err != nil {
		return err
	}
	target, err := os.Stat(selected.Executable)
	if err != nil {
		return err
	}
	if os.SameFile(current, target) {
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.execute(selected.Executable, args, environment)
}

func toolchainArgs() []string {
	if toolchainReplayArgs != nil {
		return toolchainReplayArgs
	}
	return os.Args[1:]
}
