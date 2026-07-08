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

// compileAndExec compiles the .sparkwing/ Go module to a cache
// directory keyed on a fingerprint of the module plus every local
// `replace` target, then execs the cached binary with the given
// args. Subsequent invocations with no source changes skip the
// compile entirely.
func compileAndExec(sparkwingDir string, args, env []string, opts compileOptions) error {
	if err := resolveSparks(context.Background(), sparkwingDir, opts); err != nil {
		return err
	}

	if os.Getenv("SPARKWING_NO_BINCACHE") != "" {
		return runGo(sparkwingDir, append([]string{"run", "."}, args...), env)
	}

	key, err := bincache.PipelineCacheKey(sparkwingDir)
	if err != nil {
		return runGo(sparkwingDir, append([]string{"run", "."}, args...), env)
	}

	binPath := bincache.CachedBinaryPath(key)

	if _, err := os.Stat(binPath); err == nil {
		ensureDescribeCache(sparkwingDir, binPath)
		env = append(env, "SPARKWING_BINARY_SOURCE=cached")
		return bincache.ExecReplace(binPath, args, sparkwingDir, env)
	}

	if cache, lookup := resolveEffectiveCacheSpec(sparkwingDir); cache != nil {
		if as, err := storeurl.OpenArtifactStoreFromSpec(context.Background(), *cache, lookup); err == nil {
			if err := bincache.FetchFromArtifactStore(context.Background(), as, key, binPath); err == nil {
				ensureDescribeCache(sparkwingDir, binPath)
				env = append(env, "SPARKWING_BINARY_SOURCE=artifact-store")
				return bincache.ExecReplace(binPath, args, sparkwingDir, env)
			} else if !bincache.IsNotFound(err) {
				slog.Default().Warn("artifact-store fetch failed", "err", err, "hash", key)
			}
		} else {
			slog.Default().Warn("artifact-store open failed", "err", err, "type", cache.Type)
		}
	}

	if gcURL := bincache.CacheURL(); gcURL != "" {
		if err := bincache.TryBinary(gcURL, key, binPath); err == nil {
			ensureDescribeCache(sparkwingDir, binPath)
			env = append(env, "SPARKWING_BINARY_SOURCE=gitcache")
			return bincache.ExecReplace(binPath, args, sparkwingDir, env)
		}
	}

	announceCompile(binPath)
	if err := bincache.CompilePipeline(sparkwingDir, binPath); err != nil {
		if errors.Is(err, bincache.ErrMissingGoSum) {
			fmt.Fprintln(os.Stderr, color.Dim("==> populating go.sum (`go mod download`) and retrying compile..."))
			if dlErr := runGo(sparkwingDir, []string{"mod", "download"}, env); dlErr != nil {
				return fmt.Errorf("recovery `go mod download` failed: %w", dlErr)
			}
			if err := bincache.CompilePipeline(sparkwingDir, binPath); err != nil {
				return err
			}
		} else {
			return err
		}
	}

	if gcURL := bincache.CacheURL(); gcURL != "" {
		if err := bincache.UploadBinary(gcURL, bincache.CacheToken(), key, binPath); err != nil {
			slog.Default().Warn("bin cache upload failed", "err", err, "hash", key)
		}
	}

	ensureDescribeCache(sparkwingDir, binPath)
	env = append(env, "SPARKWING_BINARY_SOURCE=compiled")
	return bincache.ExecReplace(binPath, args, sparkwingDir, env)
}

// ensureDescribeCache writes the describe-cache file if it's missing
// for the current PipelineCacheKey. Failures are logged at debug-
// level and swallowed -- the cache is a perf optimization, not a
// correctness gate on the pipeline run.
func ensureDescribeCache(sparkwingDir, binPath string) {
	key, err := bincache.PipelineCacheKey(sparkwingDir)
	if err != nil {
		return
	}
	if _, err := os.Stat(describeCachePath(key)); err == nil {
		return
	}
	if err := writeDescribeCache(sparkwingDir, binPath); err != nil {
		slog.Default().Debug("describe cache write failed", "err", err, "hash", key)
	}
}

// announceCompile prints a one-line stderr message before a local
// compile so the user knows why this run is slower than steady-state.
// Distinguishes "first time ever" (no other cached pipeline binaries
// on this laptop) from "source changed since last run" (cache root
// has entries, just not for this hash). Stays silent when stderr
// isn't a TTY (agents and pipes get clean logs already).
func announceCompile(binPath string) {
	cacheRoot := filepath.Dir(filepath.Dir(binPath))
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

// runExec runs a binary with the given args/env and propagates its
// exit code to the current process on non-zero termination. Used by
// runGo for the `go run .` fallback.
func runExec(bin string, args []string, dir string, env []string) error {
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

// runGo shells out to the `go` toolchain. Mirrors the pre-flight
// check in bincache.CompilePipeline so the SPARKWING_NO_BINCACHE
// (`go run .`) escape hatch and the cache-miss compile path
// produce the same actionable error message when Go is missing.
func runGo(dir string, args, env []string) error {
	if !goOnPath() {
		return fmt.Errorf(
			"go toolchain not on PATH: sparkwing compiles .sparkwing/ via the `go` command.\n" +
				"  Install Go 1.26+ from https://go.dev/dl/ and re-run",
		)
	}
	return runExec("go", args, dir, env)
}

// compileOptions bundles the subset of sparkwing flags that affects how we
// prepare the module graph before compile. Today only `--no-update`
// (gate on sparks auto-resolve); extend here rather than threading
// booleans one at a time through compileAndExec.
type compileOptions struct {
	// NoUpdate skips the sparks auto-resolve step. Set when the
	// operator passed --no-update or when SPARKWING_NO_SPARKS_RESOLVE=1
	// is exported. Absent sparks.yaml is already a no-op regardless of
	// this flag.
	NoUpdate bool
}

// resolveSparks invokes sparks.ResolveAndWrite unless the operator
// opted out. When the sparks manifest is absent ResolveAndWrite is a
// single stat call, so the fast-path cost is negligible. Errors bubble
// up as compile failures by default -- an agent wanting `latest`
// should fail loudly rather than silently pin to stale `go.mod`
// versions. `--no-update` (or SPARKWING_NO_SPARKS_RESOLVE=1) flips to
// the "warn and fall back" path for offline work.
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
