package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

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

func readDescribeCache(sparkwingDir string) ([]sparkwing.DescribePipeline, error) {
	key, err := bincache.PipelineCacheKey(sparkwingDir)
	if err != nil {
		return readDescribeFile(byRepoDescribePath(sparkwingDir)), nil
	}
	if out := readDescribeFile(describeCachePath(key)); out != nil {
		return out, nil
	}
	entry, entryErr := bincache.PipelineEntry(key)
	if entryErr == nil {
		lease, found, acquireErr := entry.Acquire(context.Background())
		if acquireErr == nil && found {
			defer func() { _ = lease.Release() }()
			if out, err := refreshDescribeFromBinary(sparkwingDir, lease.Path(), key); err == nil && out != nil {
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

func writeDescribeFile(path string, raw []byte) {
	if err := fssecure.EnsureDir(filepath.Dir(path)); err != nil {
		return
	}
	_ = fssecure.WriteFile(path, raw)
}

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
	if err := fssecure.EnsureDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := fssecure.WriteFile(path, out); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
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
