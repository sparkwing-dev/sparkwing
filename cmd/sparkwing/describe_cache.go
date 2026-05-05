// Describe cache: per-repo typed-flag schemas for completion.
// Two layers: content-keyed (exact, fragile under edits) and per-repo
// "last known" (stale-but-present) so tab never blocks on a recompile.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/bincache"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func describeCachePath(key string) string {
	return filepath.Join(bincache.SparkwingHome(),
		"cache", "describe", key+".json")
}

// byRepoDescribePath returns the "last known schema for this repo"
// fallback path, keyed by sha256 of the absolute sparkwing-dir.
func byRepoDescribePath(sparkwingDir string) string {
	abs, err := filepath.Abs(sparkwingDir)
	if err != nil {
		abs = sparkwingDir
	}
	sum := sha256.Sum256([]byte(abs))
	return filepath.Join(bincache.SparkwingHome(),
		"cache", "describe", "by-repo", hex.EncodeToString(sum[:16])+".json")
}

// readDescribeCache: content-key hit -> binary --describe refresh ->
// per-repo fallback. (nil, nil) on miss; never blocks on compile.
func readDescribeCache(sparkwingDir string) ([]sparkwing.DescribePipeline, error) {
	key, err := bincache.PipelineCacheKey(sparkwingDir)
	if err != nil {
		return readDescribeFile(byRepoDescribePath(sparkwingDir)), nil
	}
	if out := readDescribeFile(describeCachePath(key)); out != nil {
		return out, nil
	}
	// Binary present at current key -> regenerate via --describe (~50ms);
	// otherwise fall through (compile too slow for tab).
	if binPath := bincache.CachedBinaryPath(key); fileExists(binPath) {
		if out, err := refreshDescribeFromBinary(sparkwingDir, binPath, key); err == nil && out != nil {
			return out, nil
		}
	}
	return readDescribeFile(byRepoDescribePath(sparkwingDir)), nil
}

// readDescribeFile: nil on miss/corruption (completion wants silent fallthrough).
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

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

// refreshDescribeFromBinary execs --describe and persists both cache files.
func refreshDescribeFromBinary(sparkwingDir, binPath, key string) ([]sparkwing.DescribePipeline, error) {
	cmd := exec.Command(binPath, "--describe")
	cmd.Dir = filepath.Dir(sparkwingDir)
	raw, err := cmd.Output()
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

// writeDescribeFile is silent on failure; completion mustn't crash mid-tab.
func writeDescribeFile(path string, raw []byte) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, raw, 0o644)
}

// writeDescribeCache persists the schema after a successful build.
// Cache is perf, not correctness: caller treats failures as non-fatal.
func writeDescribeCache(sparkwingDir, binPath string) error {
	key, err := bincache.PipelineCacheKey(sparkwingDir)
	if err != nil {
		return fmt.Errorf("cache key: %w", err)
	}

	cmd := exec.Command(binPath, "--describe")
	cmd.Dir = filepath.Dir(sparkwingDir)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("run %s --describe: %w", binPath, err)
	}
	var schemas []sparkwing.DescribePipeline
	if err := json.Unmarshal(out, &schemas); err != nil {
		return fmt.Errorf("parse --describe output: %w", err)
	}

	path := describeCachePath(key)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	// Mirror to per-repo so tab survives content-key shifts mid-edit.
	writeDescribeFile(byRepoDescribePath(sparkwingDir), out)
	return nil
}

func pipelineFlagsFromCache(sparkwingDir, pipelineName string) ([]sparkwing.DescribeArg, error) {
	schemas, err := readDescribeCache(sparkwingDir)
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
