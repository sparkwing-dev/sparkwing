package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"syscall"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/toolchain"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

type cacheWarmResult struct {
	Kind     string                `json:"kind"`
	Language string                `json:"language"`
	Status   string                `json:"status"`
	Path     string                `json:"path"`
	Key      string                `json:"key,omitempty"`
	Cleared  *bincache.PruneResult `json:"cleared,omitempty"`
}

type cacheWarmDependencies struct {
	locate      func() (string, error)
	environment []string
	buildEnv    func(procenv.Kind, procenv.Base, procenv.Runtime) (procenv.Env, error)
	diagnostics io.Writer
	clear       func(context.Context) (bincache.PruneResult, error)
	prepare     func(context.Context, string, []string, io.Writer) (cacheWarmResult, error)
}

func runCacheWarm(inv clisurface.Invocation) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runCacheWarmCommand(ctx, inv, os.Stdout, cacheWarmDependencies{
		locate: defaultSparkwingDir, environment: os.Environ(), buildEnv: procenv.Build,
		diagnostics: os.Stderr,
		clear: func(ctx context.Context) (bincache.PruneResult, error) {
			return bincache.PruneToLimits(ctx, 0, 0, true)
		},
		prepare: warmLocalPipeline,
	})
}

func runCacheWarmCommand(ctx context.Context, inv clisurface.Invocation, out io.Writer, deps cacheWarmDependencies) error {
	format := inv.Output(color.IsInteractiveStdout())
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), out, format)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := deps.locate()
	if err != nil {
		return err
	}
	masker := secrets.NewMasker()
	env, err := deps.buildEnv(procenv.Install, procenv.Base{Local: true, Captured: deps.environment, Masker: masker}, procenv.Runtime{})
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var cleared *bincache.PruneResult
	if inv.Bool("clear-cache") {
		result, err := deps.clear(ctx)
		if err != nil {
			return err
		}
		cleared = &result
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	diagnostics := deps.diagnostics
	if diagnostics == nil {
		diagnostics = io.Discard
	}
	masked := &installDiagnostics{out: diagnostics, stream: masker.Stream()}
	result, prepareErr := deps.prepare(ctx, dir, slices.Collect(env.Values()), masked)
	if err := errors.Join(prepareErr, masked.close(), ctx.Err()); err != nil {
		return &maskedCLIError{err: err, masker: masker}
	}
	result.Cleared = cleared
	switch format {
	case "json":
		return json.NewEncoder(out).Encode(result)
	case "plain":
		_, err = fmt.Fprintln(out, result.Path)
	default:
		_, err = fmt.Fprintf(out, "%s pipeline %s: %s\n", result.Language, result.Status, result.Path)
	}
	return err
}

func warmLocalPipeline(ctx context.Context, dir string, env []string, diagnostics io.Writer) (cacheWarmResult, error) {
	return warmLocalPipelineWith(ctx, dir, env, diagnostics, resolveWarmEngine, func(ctx context.Context, selected engine.Resolved, request engine.SDKInstallRequest) (engine.SDKCommand, error) {
		return selected.InstallSDK(ctx, request)
	})
}

func resolveWarmEngine(ctx context.Context, dir string, sdk engine.SDKKind) (engine.Resolved, error) {
	pin, err := engine.ReadPin(dir, sdk)
	if err != nil {
		return engine.Resolved{}, err
	}
	mode, err := toolchain.ParseMode(os.Getenv(toolchainModeEnv))
	if err != nil {
		return engine.Resolved{}, err
	}
	resolver, err := engine.NewResolver(engine.Config{BuildVersion: installedVersion(), Mode: mode})
	if err != nil {
		return engine.Resolved{}, err
	}
	return resolver.Resolve(ctx, pin, releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH})
}

func warmLocalPipelineWith(ctx context.Context, dir string, env []string, diagnostics io.Writer, resolve func(context.Context, string, engine.SDKKind) (engine.Resolved, error), install func(context.Context, engine.Resolved, engine.SDKInstallRequest) (engine.SDKCommand, error)) (result cacheWarmResult, err error) {
	result.Kind = "cache_warm"
	ts, err := pipelineentry.IsTypeScript(dir)
	if err != nil {
		return result, err
	}
	sdk := engine.GoSDK
	if ts {
		sdk = engine.TypeScriptSDK
	}
	selected, err := resolve(ctx, dir, sdk)
	if err != nil {
		return result, err
	}
	if err := writeEngineNotices(diagnostics, selected.Notices); err != nil {
		return result, err
	}
	prepared, err := install(ctx, selected, engine.SDKInstallRequest{Directory: filepath.Dir(dir), PipelineDir: filepath.Base(dir), SDK: sdk, Environment: env})
	defer func() { err = errors.Join(err, prepared.Close()) }()
	if err != nil {
		return result, err
	}
	if len(prepared.Command) == 0 || !filepath.IsAbs(prepared.Command[0]) {
		return result, errors.New("SDK preparation returned an invalid command")
	}
	result.Language, result.Status, result.Path = string(sdk), "prepared", prepared.Command[0]
	if prepared.CachedExecutable != "" {
		result.Path = prepared.CachedExecutable
	}
	if ts {
		result.Path = filepath.Join(dir, "node_modules")
	}
	return result, nil
}
