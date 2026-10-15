package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/sparks"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
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
	env = withWingdHost(env)
	key, keyParts, err := bincache.ExplainCacheKey(r.sparkwingDir, env)
	if err != nil {
		return fmt.Errorf("read .sparkwing/ to weigh what it declares: %w", err)
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
		ensureDescribeCache(r.ctx, r.sparkwingDir, key, r.entry)
		return nil
	}

	lease, source, err := pipelineBinary(r.ctx, r.sparkwingDir, key, keyParts, env, os.Stderr)
	if err != nil {
		return err
	}
	r.lease, r.source, r.entry = lease, source, pipelineentry.Binary(lease.Path())
	ensureDescribeCache(r.ctx, r.sparkwingDir, key, r.entry)
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
		// safety: raiseInterrupt does not return on a platform that can
		// re-raise, so teardown has to happen before it, not in a defer.
		if r.opts.AfterChild != nil {
			r.opts.AfterChild()
		}
		r.raiseInterrupt()
	}
	return err
}

// safety: the returned lease pins the cache entry the binary lives in, so the
// caller releases it once the binary has run or been read.
func pipelineBinary(
	ctx context.Context,
	sparkwingDir, key string,
	keyParts []bincache.KeyPart,
	env []string,
	diagnostics io.Writer,
) (*bincache.Lease, string, error) {
	entry, err := bincache.PipelineEntry(key)
	if err != nil {
		return nil, "", err
	}
	source := "cached"
	lease, _, err := entry.AcquireOrMaterialize(ctx, func(tempPath string) error {
		if err := announceCompile(diagnostics); err != nil {
			return err
		}
		options := bincache.CompileOptions{Environment: env, Diagnostics: diagnostics}
		if compileErr := bincache.CompilePipeline(ctx, sparkwingDir, tempPath, options); compileErr != nil {
			if !errors.Is(compileErr, bincache.ErrMissingGoSum) {
				return compileErr
			}
			if _, err := fmt.Fprintln(diagnostics, color.Dim("==> populating go.sum (`go mod download`) and retrying compile...")); err != nil {
				return err
			}
			if dlErr := bincache.RunGo(ctx, sparkwingDir, []string{"mod", "download"}, options); dlErr != nil {
				return fmt.Errorf("recovery `go mod download` failed: %w", dlErr)
			}
			if compileErr := bincache.CompilePipeline(ctx, sparkwingDir, tempPath, options); compileErr != nil {
				return compileErr
			}
		}
		source = "compiled"
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	lease.RecordUse(sparkwingDir, keyParts)
	return lease, source, nil
}

func ensureDescribeCache(ctx context.Context, sparkwingDir, key string, entry pipelineentry.Command) {
	if readDescribeFile(describeCachePath(key)) != nil {
		return
	}
	if err := writeDescribeCache(ctx, sparkwingDir, entry); err != nil {
		slog.Default().Debug("describe cache write failed", "err", err, "hash", key)
	}
}

func announceCompile(out io.Writer) error {
	firstEver := true
	if entries, err := os.ReadDir(bincache.CacheRoot()); err == nil && len(entries) > 0 {
		firstEver = false
	}
	var msg string
	if firstEver {
		msg = "==> compiling .sparkwing/ pipeline binary (first time on this machine; may download deps)"
	} else {
		msg = "==> recompiling .sparkwing/ binary (source changed since last run)"
	}
	_, err := fmt.Fprintln(out, color.Dim(msg))
	return err
}

type compileOptions struct {
	ExecutionDir string
	NoUpdate     bool
	NoBincache   bool

	// safety: every exec path ends in os.Exit to carry the pipeline's status,
	// which skips defers, so teardown has to travel with the call and run once
	// the pipeline binary has finished.
	AfterChild func()
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
