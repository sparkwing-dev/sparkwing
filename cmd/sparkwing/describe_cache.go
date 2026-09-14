package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func describeCachePath(key string) string {
	return filepath.Join(bincache.SparkwingHome(),
		"cache", "describe", key+".json")
}

func byRepoDescribePath(sparkwingDir string) string {
	abs, err := filepath.Abs(sparkwingDir)
	if err != nil {
		abs = sparkwingDir
	}
	sum := sha256.Sum256([]byte(abs))
	return filepath.Join(bincache.SparkwingHome(),
		"cache", "describe", "by-repo", hex.EncodeToString(sum[:16])+".json")
}

func readDescribeCache(ctx context.Context, sparkwingDir string) ([]sparkwing.DescribePipeline, error) {
	key, err := bincache.PipelineCacheKey(sparkwingDir)
	if err != nil {
		return readDescribeFile(byRepoDescribePath(sparkwingDir)), nil
	}
	if out := readDescribeFile(describeCachePath(key)); out != nil {
		return out, nil
	}
	entry, entryErr := bincache.PipelineEntry(key)
	if entryErr == nil {
		lease, found, acquireErr := entry.Acquire(ctx)
		if acquireErr == nil && found {
			defer func() { _ = lease.Release() }()
			if out, err := refreshDescribeFromBinary(ctx, sparkwingDir, lease.Path(), key); err == nil && out != nil {
				return out, nil
			}
		}
	}
	return readDescribeFile(byRepoDescribePath(sparkwingDir)), nil
}

func readDescribeFile(path string) []sparkwing.DescribePipeline {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []sparkwing.DescribePipeline
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func refreshDescribeFromBinary(ctx context.Context, sparkwingDir, binPath, key string) ([]sparkwing.DescribePipeline, error) {
	raw, err := runDescribeBinary(ctx, sparkwingDir, binPath)
	if err != nil {
		return nil, fmt.Errorf("run %s --describe: %w", binPath, err)
	}
	var schemas []sparkwing.DescribePipeline
	if err := json.Unmarshal(raw, &schemas); err != nil {
		return nil, fmt.Errorf("parse --describe output: %w", err)
	}
	writeDescribeFile(describeCachePath(key), raw)
	writeDescribeFile(byRepoDescribePath(sparkwingDir), raw)
	return schemas, nil
}

func writeDescribeFile(path string, raw []byte) {
	if err := fssecure.EnsureDir(filepath.Dir(path)); err != nil {
		return
	}
	_ = fssecure.WriteFile(path, raw)
}

func writeDescribeCache(ctx context.Context, sparkwingDir, binPath string) error {
	key, err := bincache.PipelineCacheKey(sparkwingDir)
	if err != nil {
		return fmt.Errorf("cache key: %w", err)
	}

	out, err := runDescribeBinary(ctx, sparkwingDir, binPath)
	if err != nil {
		return fmt.Errorf("run %s --describe: %w", binPath, err)
	}
	return storeDescribe(sparkwingDir, key, out)
}

// safety: the binary cache holds no build when the operator asked for `go run
// .`, so the declarations come from the source the same way the run will. A
// binary that cannot describe itself is tolerated here exactly as it is on the
// cached path.
func ensureDescribeFromSource(ctx context.Context, sparkwingDir, key string, env []string) {
	if _, err := os.Stat(describeCachePath(key)); err == nil {
		return
	}
	cmd := exec.CommandContext(ctx, "go", "run", ".", "--describe")
	cmd.Dir = sparkwingDir
	cmd.Env = env
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		slog.Default().Debug("describe from source failed", "err", err, "hash", key)
		return
	}
	if err := storeDescribe(sparkwingDir, key, out); err != nil {
		slog.Default().Debug("describe cache write failed", "err", err, "hash", key)
	}
}

func storeDescribe(sparkwingDir, key string, raw []byte) error {
	var schemas []sparkwing.DescribePipeline
	if err := json.Unmarshal(raw, &schemas); err != nil {
		return fmt.Errorf("parse --describe output: %w", err)
	}
	path := describeCachePath(key)
	if err := fssecure.EnsureDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := fssecure.WriteFile(path, raw); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	writeDescribeFile(byRepoDescribePath(sparkwingDir), raw)
	return nil
}

func pipelineFlagsFromCache(ctx context.Context, sparkwingDir, pipelineName string) ([]sparkwing.DescribeArg, error) {
	schemas, err := readDescribeCache(ctx, sparkwingDir)
	if err != nil {
		return nil, err
	}
	for _, s := range schemas {
		if s.Name == pipelineName {
			return s.Args, nil
		}
	}
	return nil, nil
}

func runDescribeBinary(ctx context.Context, sparkwingDir, binPath string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, "--describe")
	cmd.Dir = filepath.Dir(sparkwingDir)
	// safety: inherited output pipes must not extend the metadata deadline.
	cmd.WaitDelay = 100 * time.Millisecond
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return out, err
}
