package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/toolchain"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

const toolchainModeEnv = "SPARKWING_TOOLCHAIN"

var (
	toolchainReplayArgs      []string
	toolchainReplayDirectory string
)

var (
	readEnginePin    = engine.ReadPin
	enginePinVersion = func(pin engineid.Pin) (string, error) {
		return engine.CachedEngineVersion(context.Background(), pin)
	}
)

type pipelineEngineSwitcher struct {
	readPin     func(string, engineid.SDKKind) (engineid.Pin, error)
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
		readPin: readEnginePin,
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
	selected, err := c.resolve(ctx, pin, c.target, mode)
	if err := errors.Join(err, ctx.Err()); err != nil {
		return err
	}
	if pin.SDK != sdk || selected.Identity.Pin != pin || selected.Identity.Version == "" ||
		selected.Identity.ManifestDigest == "" || selected.AssetDigest == "" ||
		selected.Target != c.target || !filepath.IsAbs(selected.Executable) {
		return &apierr.Error{Code: apierr.ProtocolMismatch, Message: "engine resolver returned a different pin or incomplete verified executable"}
	}
	if err := errors.Join(writeEngineNoticesOnce(c.diagnostics, selected.Notices), ctx.Err()); err != nil {
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
	if err := refuseRenamedRunFlags(selected.Identity.Version, args); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.execute(selected.Executable, args, environment)
}

// safety: engines before this release parse the run flags with a sw- prefix,
// so a renamed flag handed to one is refused with advice that names the old flag.
const runFlagNamesEngine = "v0.74.0"

var renamedRunFlags = map[string]bool{
	"route": true, "cache-from": true, "pause-before": true, "pause-after": true, "pause-on-failure": true,
	"ref": true, "pipeline-ref": true, "detach": true, "workers": true, "idempotency-key": true,
	"request-id": true, "verbose": true, "start-at": true, "stop-at": true, "only": true, "no-memo": true,
	"priority": true, "no-update": true, "no-bincache": true, "hash-all-files": true,
	"allow-unadmitted": true, "dry-run": true, "allow-risk": true, "index": true, "run-handle-file": true,
}

func refuseRenamedRunFlags(engineVersion string, args []string) error {
	if !semver.IsValid(engineVersion) || semver.Compare(engineVersion, runFlagNamesEngine) >= 0 {
		return nil
	}
	for _, arg := range args {
		if arg == "--" {
			return nil
		}
		name, ok := strings.CutPrefix(arg, "--")
		if name, _, _ = strings.Cut(name, "="); ok && renamedRunFlags[name] {
			return &apierr.Error{Code: apierr.InvalidArgs, Message: "run: this pipeline's SDK pin selects engine " + engineVersion +
				", which predates --" + name + " and the other run flag names; run sparkwing pipeline upgrade, then run it again"}
		}
	}
	return nil
}

func toolchainArgs() []string {
	if toolchainReplayArgs != nil {
		return toolchainReplayArgs
	}
	return os.Args[1:]
}
