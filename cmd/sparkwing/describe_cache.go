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
)

func describeCachePath(key string) string {
	return filepath.Join(bincache.SparkwingHome(),
		"cache", "describe-v3", key+".json")
}

func byRepoDescribePath(sparkwingDir string) string {
	abs, err := filepath.Abs(sparkwingDir)
	if err != nil {
		abs = sparkwingDir
	}
	sum := sha256.Sum256([]byte(abs))
	return filepath.Join(bincache.SparkwingHome(),
		"cache", "describe-v3", "by-repo", hex.EncodeToString(sum[:16])+".json")
}

func readDescribeCache(ctx context.Context, sparkwingDir string) ([]nodeproto.PipelineDescription, error) {
	ts, err := pipelineentry.IsTypeScript(sparkwingDir)
	if err != nil {
		return nil, err
	}
	// safety: a TypeScript entry can import any file in the repository, so no
	// key over .sparkwing alone stays fresh; it is described every time.
	if ts {
		entry, err := pipelineentry.TypeScript(sparkwingDir)
		if err != nil {
			return nil, err
		}
		doc, err := describeEntry(ctx, sparkwingDir, entry)
		return doc.Pipelines, err
	}
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

// perf: a listing reads only a binary already built, never compiling;
// release frees it once it has answered.
func builtEntry(ctx context.Context, sparkwingDir, key string) (pipelineentry.Command, func(), bool) {
	cached, err := bincache.PipelineEntry(key)
	if err != nil {
		return nil, nil, false
	}
	lease, found, err := cached.Acquire(ctx)
	if err != nil || !found {
		return nil, nil, false
	}
	defer func() { _ = lease.Release() }() //nolint:errcheck // a lease left held only delays pruning
	dir, err := os.MkdirTemp("", "sparkwing-describe-")
	if err != nil {
		return nil, nil, false
	}
	binary, err := lease.Install(filepath.Join(dir, "pipeline"))
	if err != nil {
		_ = os.RemoveAll(dir) //nolint:errcheck // a leftover temporary copy is harmless
		return nil, nil, false
	}
	return pipelineentry.Binary(binary.Path()), func() { _ = os.RemoveAll(dir) }, true //nolint:errcheck // a leftover temporary copy is harmless
}

// safety: the versioned cache directory separates protocol document shapes.
func readDescribeFile(path string) []nodeproto.PipelineDescription {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc nodeproto.DescribeDoc
	if err := (nodeproto.Reply{ID: "cache", OK: true, Result: raw}).Decode(&doc); err != nil || doc.Pipelines == nil {
		return nil
	}
	return doc.Pipelines
}

func refreshDescribe(ctx context.Context, sparkwingDir string, entry pipelineentry.Command, key string) ([]nodeproto.PipelineDescription, error) {
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

func writeDescribeCache(ctx context.Context, sparkwingDir, key string, entry pipelineentry.Command) error {
	doc, err := describeEntry(ctx, sparkwingDir, entry)
	if err != nil {
		return err
	}
	return storeDescribe(sparkwingDir, key, doc)
}

func storeDescribe(sparkwingDir, key string, doc nodeproto.DescribeDoc) error {
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

func pipelineFlagsFromCache(ctx context.Context, sparkwingDir, pipelineName string) ([]nodeproto.ArgumentDescription, error) {
	identity, err := pipelineIdentityAt(sparkwingDir, pipelineName)
	if err != nil {
		return nil, err
	}
	schemas, err := readDescribeCache(ctx, sparkwingDir)
	if err != nil {
		return nil, err
	}
	schema, err := boundPipelineSchema(schemas, identity)
	return schema.Args, err
}

func describeEntry(ctx context.Context, sparkwingDir string, entry pipelineentry.Command) (nodeproto.DescribeDoc, error) {
	doc, err := repos.Describe(ctx, entry, filepath.Dir(sparkwingDir))
	if err != nil {
		return doc, fmt.Errorf("describe %s: %w", entry[len(entry)-1], err)
	}
	return doc, nil
}
