package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/sparks"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

// safety: the dispatcher has to weigh what a pipeline declares before it
// starts, and only the pipeline's entry declares it, so materializing the
// entry and running it are two steps a caller sequences within one interrupt
// lifetime.
type pipelineRun struct {
	ctx            context.Context
	stopSignals    func()
	raiseInterrupt func()
	sparkwingDir   string
	opts           compileOptions
	uncached       bool
	lease          *bincache.Lease
	source         string
	uncachedBinary string
	entry          pipelineentry.Command
	typescript     bool
	pipeline       nodehost.PipelineIdentity
}

func newPipelineRun(sparkwingDir string, opts compileOptions) *pipelineRun {
	ctx, stopSignals, raiseInterrupt := interruptContext()
	return &pipelineRun{
		ctx:            ctx,
		stopSignals:    stopSignals,
		raiseInterrupt: raiseInterrupt,
		sparkwingDir:   sparkwingDir,
		opts:           opts,
		uncached:       opts.NoBincache,
	}
}

func (r *pipelineRun) materialize(env []string) error {
	ts, err := pipelineentry.IsTypeScript(r.sparkwingDir)
	if err != nil {
		return err
	}
	if ts {
		return r.materializeTypeScript(env)
	}
	r.ctx = gotoolchain.WithSession(r.ctx, env, nil)
	if err := resolveSparks(r.ctx, r.sparkwingDir, r.opts); err != nil {
		return err
	}
	if r.uncached {
		dir, err := os.MkdirTemp("", "sparkwing-source-bin-")
		if err != nil {
			return err
		}
		r.uncachedBinary = filepath.Join(dir, "pipeline")
		if err := bincache.CompileUncachedPipeline(r.ctx, r.sparkwingDir, r.uncachedBinary, bincache.CompileOptions{Environment: env, Diagnostics: os.Stderr}); err != nil {
			return err
		}
		r.entry, r.source = pipelineentry.Binary(r.uncachedBinary), "source"
		return nil
	}

	lease, source, err := pipelineBinary(r.ctx, r.sparkwingDir, bincache.CompileOptions{Environment: env, Diagnostics: os.Stderr, HashAllFiles: r.opts.HashAllFiles})
	if err != nil {
		return err
	}
	r.lease, r.source, r.entry = lease, source, pipelineentry.Binary(lease.Path())
	ensureDescribeCache(r.ctx, r.sparkwingDir, lease.Key(), r.entry)
	return nil
}

func (r *pipelineRun) materializeTypeScript(env []string) error {
	entry, err := pipelineentry.Prepare(r.ctx, r.sparkwingDir, env, os.Stderr)
	if err != nil {
		return err
	}
	r.entry, r.source, r.typescript = entry, "typescript", true
	return nil
}

func (r *pipelineRun) stop() {
	r.stopSignals()
	if r.uncachedBinary != "" {
		// #nosec G703 -- uncachedBinary is the fixed pipeline filename inside a MkdirTemp directory.
		if err := os.RemoveAll(filepath.Dir(r.uncachedBinary)); err != nil {
			slog.Default().Warn("remove temporary pipeline executable", "error", err)
		}
	}
	if r.lease == nil {
		return
	}
	if err := r.lease.Release(); err != nil {
		slog.Default().Debug("pipeline binary lease release failed", "err", err)
	}
}

func (r *pipelineRun) finish(err error) error {
	if r.ctx.Err() != nil {
		r.stopSignals()
		r.raiseInterrupt()
	}
	return err
}

// safety: the returned lease pins the cache entry the binary lives in, so the
// caller releases it once the binary has run or been read.
func pipelineBinary(ctx context.Context, sparkwingDir string, options bincache.CompileOptions) (*bincache.Lease, string, error) {
	lease, published, err := bincache.MaterializePipeline(ctx, sparkwingDir, options)
	if err != nil {
		return nil, "", err
	}
	source := "cached"
	if published {
		source = "compiled"
	}
	return lease, source, nil
}

func ensureDescribeCache(ctx context.Context, sparkwingDir, key string, entry pipelineentry.Command) {
	if readDescribeFile(describeCachePath(key)) != nil {
		return
	}
	if err := writeDescribeCache(ctx, sparkwingDir, key, entry); err != nil {
		slog.Default().Debug("describe cache write failed", "err", err, "hash", key)
	}
}

type compileOptions struct {
	ExecutionDir string
	NoUpdate     bool
	NoBincache   bool
	HashAllFiles bool
}

func resolveSparks(ctx context.Context, sparkwingDir string, opts compileOptions) error {
	if opts.NoUpdate {
		return nil
	}
	m, err := projectconfig.LoadSparksManifest(sparkwingDir)
	if err != nil {
		return fmt.Errorf("sparks resolve: %w", err)
	}
	if _, err := sparks.ResolveAndWrite(ctx, sparkwingDir, m); err != nil {
		var toolchainErr *gotoolchain.Error
		if errors.As(err, &toolchainErr) {
			return fmt.Errorf("sparks resolve: %w", err)
		}
		if explanation := gotoolchain.ExplainOutput(ctx, err.Error(), nil); explanation != nil {
			return fmt.Errorf("sparks resolve: %w", explanation)
		}
		return fmt.Errorf("sparks resolve: %w (use --sw-no-update to compile against existing go.mod pins)", err)
	}
	return nil
}
