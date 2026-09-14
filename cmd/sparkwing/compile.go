package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/sparks"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
)

func compileAndExec(sparkwingDir string, args, env []string, opts compileOptions) error {
	run := newPipelineRun(sparkwingDir, opts)
	defer run.stop()
	if err := run.materialize(env); err != nil {
		return run.finish(err)
	}
	return run.finish(run.exec(args, env))
}

// safety: the dispatcher has to weigh what a pipeline declares before it
// starts, and only the compiled binary declares it, so building the binary and
// exec'ing it are two steps a caller sequences within one interrupt lifetime.
type pipelineRun struct {
	ctx            context.Context
	stopSignals    func()
	raiseInterrupt func()
	sparkwingDir   string
	opts           compileOptions
	lease          *bincache.Lease
	source         string
}

func newPipelineRun(sparkwingDir string, opts compileOptions) *pipelineRun {
	ctx, stopSignals, raiseInterrupt := interruptContext()
	return &pipelineRun{
		ctx:            ctx,
		stopSignals:    stopSignals,
		raiseInterrupt: raiseInterrupt,
		sparkwingDir:   sparkwingDir,
		opts:           opts,
	}
}

func (r *pipelineRun) materialize(env []string) error {
	if err := resolveSparks(r.ctx, r.sparkwingDir, r.opts); err != nil {
		return err
	}
	if os.Getenv("SPARKWING_NO_BINCACHE") != "" {
		return nil
	}
	// safety: a tree whose cache key cannot be computed runs through `go run .`,
	// which publishes no binary, so there is nothing to materialize or describe.
	if key, keyParts, keyErr := bincache.ExplainCacheKey(r.sparkwingDir); keyErr == nil {
		lease, source, err := pipelineBinary(r.ctx, r.sparkwingDir, key, keyParts, withWingdHost(env))
		if err != nil {
			return err
		}
		r.lease, r.source = lease, source
	}
	return nil
}

func (r *pipelineRun) exec(args, env []string) error {
	env = withWingdHost(env)
	if r.lease == nil {
		r.stopSignals()
		return runGo(r.sparkwingDir, append([]string{"run", "."}, args...), env, r.opts.AfterChild)
	}
	env = append(env, "SPARKWING_BINARY_SOURCE="+r.source)
	if err := r.ctx.Err(); err != nil {
		return err
	}
	// safety: the pipeline binary drives the terminal from here, so the CLI
	// must stop catching signals it can no longer act on for that program.
	r.stopSignals()
	if fleetExecutionEnv(env) {
		return runExec(r.lease.Path(), args, r.sparkwingDir, env, r.opts.AfterChild)
	}
	return r.lease.ExecReplace(args, r.sparkwingDir, env, r.opts.AfterChild)
}

func (r *pipelineRun) stop() {
	r.stopSignals()
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
) (*bincache.Lease, string, error) {
	entry, err := bincache.PipelineEntry(key)
	if err != nil {
		return nil, "", err
	}
	source := "cached"
	lease, published, err := entry.AcquireOrMaterialize(ctx, func(tempPath string) error {
		if cache, lookup := resolveBinaryCacheSpec(); cache != nil {
			if store, openErr := storeurl.OpenArtifactStoreFromSpec(ctx, *cache, lookup); openErr == nil {
				if fetchErr := bincache.FetchFromArtifactStore(ctx, store, key, tempPath); fetchErr == nil {
					source = "artifact-store"
					return nil
				} else if !bincache.IsNotFound(fetchErr) {
					slog.Default().Warn("artifact-store fetch failed", "err", fetchErr, "hash", key)
				}
			} else {
				slog.Default().Warn("artifact-store open failed", "err", openErr, "type", cache.Type)
			}
		}
		if gcURL := bincache.CacheURL(); gcURL != "" {
			if fetchErr := bincache.TryBinary(ctx, gcURL, bincache.CacheToken(), key, tempPath); fetchErr == nil {
				source = "gitcache"
				return nil
			} else if !errors.Is(fetchErr, bincache.ErrMiss) {
				slog.Default().Warn("bin cache fetch failed", "err", fetchErr, "hash", key)
			}
		}
		announceCompile()
		if compileErr := bincache.CompilePipeline(ctx, sparkwingDir, tempPath); compileErr != nil {
			if !errors.Is(compileErr, bincache.ErrMissingGoSum) {
				return compileErr
			}
			fmt.Fprintln(os.Stderr, color.Dim("==> populating go.sum (`go mod download`) and retrying compile..."))
			if dlErr := bincache.RunGo(ctx, sparkwingDir, []string{"mod", "download"}, env); dlErr != nil {
				return fmt.Errorf("recovery `go mod download` failed: %w", dlErr)
			}
			if compileErr := bincache.CompilePipeline(ctx, sparkwingDir, tempPath); compileErr != nil {
				return compileErr
			}
		}
		source = "compiled"
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	if published && source == "compiled" {
		if gcURL := bincache.CacheURL(); gcURL != "" {
			if err := bincache.UploadBinary(ctx, gcURL, bincache.CacheToken(), key, lease.Path()); err != nil {
				slog.Default().Warn("bin cache upload failed", "err", err, "hash", key)
			}
		}
	}
	lease.RecordUse(sparkwingDir, keyParts)
	ensureDescribeCache(ctx, sparkwingDir, key, lease.Path())
	return lease, source, nil
}

// safety: what a pipeline declares lives in its compiled binary, so a home
// that holds no build of it can answer for a declaration only after this.
func ensurePipelineDeclarations(ctx context.Context, sparkwingDir string, env []string, opts compileOptions) error {
	if err := resolveSparks(ctx, sparkwingDir, opts); err != nil {
		return err
	}
	if key, keyParts, keyErr := bincache.ExplainCacheKey(sparkwingDir); keyErr == nil {
		lease, _, err := pipelineBinary(ctx, sparkwingDir, key, keyParts, env)
		if err != nil {
			return err
		}
		return lease.Release()
	}
	// safety: an unkeyable tree runs through `go run .`, which publishes no
	// binary to read a declaration from; the caller proceeds without them.
	return nil
}

func ensureDescribeCache(ctx context.Context, sparkwingDir, key, binPath string) {
	if _, err := os.Stat(describeCachePath(key)); err == nil {
		return
	}
	if err := writeDescribeCache(ctx, sparkwingDir, binPath); err != nil {
		slog.Default().Debug("describe cache write failed", "err", err, "hash", key)
	}
}

func announceCompile() {
	cacheRoot := filepath.Join(bincache.SparkwingHome(), "cache", "pipelines", "v1", "entries")
	firstEver := true
	if entries, err := os.ReadDir(cacheRoot); err == nil && len(entries) > 0 {
		firstEver = false
	}
	var msg string
	if firstEver {
		msg = "==> compiling .sparkwing/ pipeline binary (first time on this machine; may download deps)"
	} else {
		msg = "==> recompiling .sparkwing/ binary (source changed since last run)"
	}
	fmt.Fprintln(os.Stderr, color.Dim(msg))
}

func runExec(bin string, args []string, dir string, env []string, afterChild func()) error {
	// #nosec G702 -- the pipeline binary this command just built, run as argv without a shell
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = env
	err := cmd.Run()
	if afterChild != nil {
		afterChild()
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if fleetExecutionEnv(env) {
				return exitError(ee.ExitCode(), err)
			}
			os.Exit(ee.ExitCode()) //nolint:forbidigo // foreground wrapper preserves the pipeline's exit status
		}
		return err
	}
	return nil
}

func fleetExecutionEnv(env []string) bool {
	for _, entry := range env {
		if entry == "SPARKWING_FLEET=1" {
			return true
		}
	}
	return false
}

func runGo(dir string, args, env []string, afterChild func()) error {
	if !goOnPath() {
		return fmt.Errorf(
			"go toolchain not on PATH: sparkwing compiles .sparkwing/ via the `go` command.\n" +
				"  Install Go 1.26+ from https://go.dev/dl/ and re-run",
		)
	}
	return runExec("go", args, dir, env, afterChild)
}

type compileOptions struct {
	NoUpdate bool

	// safety: every exec path ends in os.Exit to carry the pipeline's status,
	// which skips defers, so teardown has to travel with the call and run once
	// the pipeline binary has finished.
	AfterChild func()
}

func resolveSparks(ctx context.Context, sparkwingDir string, opts compileOptions) error {
	noUpdate := opts.NoUpdate || os.Getenv("SPARKWING_NO_SPARKS_RESOLVE") != ""
	if noUpdate {
		return nil
	}
	m, err := projectconfig.LoadSparksManifest(sparkwingDir)
	if err != nil {
		return fmt.Errorf("sparks resolve: %w", err)
	}
	if _, err := sparks.ResolveAndWrite(ctx, sparkwingDir, m); err != nil {
		return fmt.Errorf("sparks resolve: %w (use --sw-no-update to compile against existing go.mod pins)", err)
	}
	return nil
}
