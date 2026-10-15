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
	"syscall"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
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
	buildEnv    func(procenv.Kind, procenv.Base, procenv.Runtime) ([]string, error)
	clear       func(context.Context) (bincache.PruneResult, error)
	prepare     func(context.Context, string, []string) (cacheWarmResult, error)
}

func runCacheWarm(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runCacheWarmCommand(ctx, args, os.Stdout, cacheWarmDependencies{
		locate: defaultSparkwingDir, environment: os.Environ(), buildEnv: procenv.Build,
		clear: func(ctx context.Context) (bincache.PruneResult, error) {
			return bincache.PruneToLimits(ctx, 0, 0, true)
		},
		prepare: warmLocalPipeline,
	})
}

func runCacheWarmCommand(ctx context.Context, args []string, out io.Writer, deps cacheWarmDependencies) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"cache", "warm"}, args...))
	if err != nil {
		return err
	}
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
	env, err := deps.buildEnv(procenv.Install, procenv.Base{Local: true, Captured: deps.environment}, procenv.Runtime{})
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
	result, err := deps.prepare(ctx, dir, env)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
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

func warmLocalPipeline(ctx context.Context, dir string, env []string) (result cacheWarmResult, retErr error) {
	result.Kind = "cache_warm"
	ts, err := pipelineentry.IsTypeScript(dir)
	if err != nil {
		return result, err
	}
	if ts {
		if err := pipelineentry.InstallTypeScript(ctx, dir, env, os.Stderr); err != nil {
			return result, err
		}
		result.Language, result.Status, result.Path = "typescript", "prepared", filepath.Join(dir, "node_modules")
		return result, nil
	}
	ctx = gotoolchain.WithSession(ctx, env, nil)
	if err := resolveSparks(ctx, dir, compileOptions{}); err != nil {
		return result, err
	}
	key, parts, err := bincache.ExplainCacheKey(dir)
	if err != nil {
		return result, err
	}
	lease, source, err := pipelineBinary(ctx, dir, key, parts, env)
	if err != nil {
		return result, err
	}
	defer func() { retErr = errors.Join(retErr, lease.Release()) }()
	result.Language, result.Status, result.Path, result.Key = "go", source, lease.Path(), key
	return result, nil
}
