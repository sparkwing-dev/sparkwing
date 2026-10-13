package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/repos"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
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
	if entry, release, ok := builtEntry(ctx, sparkwingDir, key); ok {
		defer release()
		if out, err := refreshDescribe(ctx, sparkwingDir, entry, key); err == nil && out != nil {
			return out, nil
		}
	}
	return readDescribeFile(byRepoDescribePath(sparkwingDir)), nil
}

// perf: a listing reads only an entry already built or installed, never
// compiling or running npm; release frees it once it has answered.
func builtEntry(ctx context.Context, sparkwingDir, key string) (pipelineentry.Command, func(), bool) {
	ts, err := pipelineentry.IsTypeScript(sparkwingDir)
	if err != nil {
		return nil, nil, false
	}
	if ts {
		entry, err := pipelineentry.TypeScript(sparkwingDir)
		return entry, func() {}, err == nil
	}
	cached, err := bincache.PipelineEntry(key)
	if err != nil {
		return nil, nil, false
	}
	lease, found, err := cached.Acquire(ctx)
	if err != nil || !found {
		return nil, nil, false
	}
	return pipelineentry.Binary(lease.Path()), func() { _ = lease.Release() }, true //nolint:errcheck // a lease left held only delays pruning
}

// safety: a document another protocol version wrote reads as a miss, so it
// is described again rather than trusted.
func readDescribeFile(path string) []sparkwing.DescribePipeline {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc sdkserve.DescribeDoc
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Protocol != nodeproto.Version || doc.Pipelines == nil {
		return nil
	}
	return doc.Pipelines
}

func refreshDescribe(ctx context.Context, sparkwingDir string, entry pipelineentry.Command, key string) ([]sparkwing.DescribePipeline, error) {
	doc, err := describeEntry(ctx, sparkwingDir, entry)
	if err != nil {
		return nil, err
	}
	// safety: a home that takes no writes still answers from the entry.
	if err := storeDescribe(sparkwingDir, key, doc); err != nil {
		slog.Default().Debug("describe cache write failed", "err", err, "hash", key)
	}
	return doc.Pipelines, nil
}

func writeDescribeCache(ctx context.Context, sparkwingDir string, entry pipelineentry.Command) error {
	key, err := bincache.PipelineCacheKey(sparkwingDir)
	if err != nil {
		return fmt.Errorf("cache key: %w", err)
	}
	doc, err := describeEntry(ctx, sparkwingDir, entry)
	if err != nil {
		return err
	}
	return storeDescribe(sparkwingDir, key, doc)
}

func storeDescribe(sparkwingDir, key string, doc sdkserve.DescribeDoc) error {
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	for _, path := range []string{describeCachePath(key), byRepoDescribePath(sparkwingDir)} {
		if err := fssecure.EnsureDir(filepath.Dir(path)); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
		}
		if err := fssecure.WriteFile(path, raw); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
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

func describeEntry(ctx context.Context, sparkwingDir string, entry pipelineentry.Command) (sdkserve.DescribeDoc, error) {
	doc, err := repos.Describe(ctx, entry, filepath.Dir(sparkwingDir))
	if err != nil {
		return doc, fmt.Errorf("describe %s: %w", entry[len(entry)-1], err)
	}
	return doc, nil
}
