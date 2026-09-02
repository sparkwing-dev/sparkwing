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
	if err := resolveSparks(context.Background(), sparkwingDir, opts); err != nil {
		return err
	}

	env = withWingdHost(env)

	if os.Getenv("SPARKWING_NO_BINCACHE") != "" {
		return runGo(sparkwingDir, append([]string{"run", "."}, args...), env)
	}

	key, keyParts, err := bincache.ExplainCacheKey(sparkwingDir)
	if err != nil {
		return runGo(sparkwingDir, append([]string{"run", "."}, args...), env)
	}
	entry, err := bincache.PipelineEntry(key)
	if err != nil {
		return err
	}
	ctx := context.Background()
	source := "cached"
	lease, published, err := entry.AcquireOrMaterialize(ctx, func(tempPath string) error {
		if cache, lookup := resolveEffectiveCacheSpec(sparkwingDir); cache != nil {
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
			if fetchErr := bincache.TryBinary(gcURL, bincache.CacheToken(), key, tempPath); fetchErr == nil {
				source = "gitcache"
				return nil
			} else if !errors.Is(fetchErr, bincache.ErrMiss) {
				slog.Default().Warn("bin cache fetch failed", "err", fetchErr, "hash", key)
			}
		}
		announceCompile()
		if compileErr := bincache.CompilePipeline(sparkwingDir, tempPath); compileErr != nil {
			if !errors.Is(compileErr, bincache.ErrMissingGoSum) {
				return compileErr
			}
			fmt.Fprintln(os.Stderr, color.Dim("==> populating go.sum (`go mod download`) and retrying compile..."))
			if dlErr := runGo(sparkwingDir, []string{"mod", "download"}, env); dlErr != nil {
				return fmt.Errorf("recovery `go mod download` failed: %w", dlErr)
			}
			if compileErr := bincache.CompilePipeline(sparkwingDir, tempPath); compileErr != nil {
				return compileErr
			}
		}
		source = "compiled"
		return nil
	})
	if err != nil {
		return err
	}
	defer func() { _ = lease.Release() }()
	if published && source == "compiled" {
		if gcURL := bincache.CacheURL(); gcURL != "" {
			if err := bincache.UploadBinary(gcURL, bincache.CacheToken(), key, lease.Path()); err != nil {
				slog.Default().Warn("bin cache upload failed", "err", err, "hash", key)
			}
		}
	}
	lease.RecordUse(sparkwingDir, keyParts)
	ensureDescribeCache(sparkwingDir, key, lease.Path())
	env = append(env, "SPARKWING_BINARY_SOURCE="+source)
	return lease.ExecReplace(args, sparkwingDir, env)
}

func ensureDescribeCache(sparkwingDir, key, binPath string) {
	if _, err := os.Stat(describeCachePath(key)); err == nil {
		return
	}
	if err := writeDescribeCache(sparkwingDir, binPath); err != nil {
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

func runExec(bin string, args []string, dir string, env []string) error {
	// #nosec G702 -- the pipeline binary this command just built, run as argv without a shell
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		return err
	}
	return nil
}

func runGo(dir string, args, env []string) error {
	if !goOnPath() {
		return fmt.Errorf(
			"go toolchain not on PATH: sparkwing compiles .sparkwing/ via the `go` command.\n" +
				"  Install Go 1.26+ from https://go.dev/dl/ and re-run",
		)
	}
	return runExec("go", args, dir, env)
}

type compileOptions struct {
	NoUpdate bool
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
