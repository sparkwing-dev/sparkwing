package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/buildinfo"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/toolchain"
)

const toolchainModeEnv = "SPARKWING_TOOLCHAIN"

var toolchainReplayArgs []string

var (
	readEnginePin    = engine.ReadPin
	enginePinVersion = engine.VersionForPin
)

type pipelineEngineSwitcher struct {
	readPin     func(string, engine.SDKKind) (engine.Pin, error)
	versionFor  func(engine.Pin) (string, error)
	resolve     func(context.Context, engine.Pin, releaseasset.Target, toolchain.Mode) (engine.Resolved, error)
	execute     func(string, []string, []string) error
	current     string
	target      releaseasset.Target
	diagnostics io.Writer
}

func switchToolchain(dir string) error {
	ctx, stop, _ := interruptContext()
	defer stop()
	typescript, err := pipelineentry.IsTypeScript(dir)
	if err != nil {
		return err
	}
	sdk := engine.GoSDK
	if typescript {
		sdk = engine.TypeScriptSDK
	}
	current, err := os.Executable()
	if err != nil {
		return err
	}
	command := pipelineEngineSwitcher{
		readPin: readEnginePin, versionFor: enginePinVersion,
		current: current, target: releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH},
		execute: execToolchain, diagnostics: os.Stderr,
		resolve: func(ctx context.Context, pin engine.Pin, target releaseasset.Target, mode toolchain.Mode) (engine.Resolved, error) {
			resolver, err := engine.NewResolver(engine.Config{Mode: mode})
			if err != nil {
				return engine.Resolved{}, err
			}
			return resolver.Resolve(ctx, pin, target)
		},
	}
	return command.run(ctx, dir, sdk, os.Getenv(toolchainModeEnv), toolchainArgs(), os.Environ())
}

func (c pipelineEngineSwitcher) run(ctx context.Context, dir string, sdk engine.SDKKind, rawMode string, args, environment []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	mode, err := toolchain.ParseMode(rawMode)
	if err != nil {
		return &apierr.Error{Code: apierr.InvalidArgs, Message: err.Error()}
	}
	pin, err := c.readPin(filepath.Dir(dir), sdk)
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

func isReleaseTag(version string) bool { return buildinfo.IsReleaseVersion(version) }

func tildePath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	prefix := home + string(os.PathSeparator)
	if strings.HasPrefix(path, prefix) {
		return "~" + string(os.PathSeparator) + strings.TrimPrefix(path, prefix)
	}
	return path
}
