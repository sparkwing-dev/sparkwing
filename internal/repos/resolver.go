package repos

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
)

var ErrNotFound = errors.New("repos: no registered repo provides that pipeline")

// DescribeRepo builds the pipeline binary a repository declares, or installs
// its TypeScript pipeline's dependencies, and returns the schemas the entry
// emits: each pipeline's name, arguments, and the risk labels its steps
// declare. A caller that needs only the names, and one that has to weigh what
// a pipeline declares, read the same entry.
func DescribeRepo(absPath string) ([]nodeproto.PipelineDescription, error) {
	sparkwingDir := filepath.Join(absPath, ".sparkwing")
	if _, err := os.Stat(sparkwingDir); err != nil {
		return nil, fmt.Errorf("no .sparkwing/ at %s: %w", sparkwingDir, err)
	}
	ts, err := pipelineentry.IsTypeScript(sparkwingDir)
	if err != nil {
		return nil, err
	}
	if ts {
		entry, err := pipelineentry.Prepare(context.Background(), sparkwingDir, os.Environ(), os.Stderr)
		if err != nil {
			return nil, err
		}
		return describePipelines(entry, absPath)
	}
	hash, err := bincache.PipelineCacheKey(sparkwingDir)
	if err != nil {
		return nil, fmt.Errorf("hash %s: %w", sparkwingDir, err)
	}
	entry, err := bincache.PipelineEntry(hash)
	if err != nil {
		return nil, fmt.Errorf("cache entry: %w", err)
	}
	lease, _, err := entry.AcquireOrMaterialize(context.Background(), func(tempPath string) error {
		return bincache.CompilePipeline(context.Background(), sparkwingDir, tempPath)
	})
	if err != nil {
		return nil, fmt.Errorf("compile %s: %w", sparkwingDir, err)
	}
	defer func() { _ = lease.Release() }()
	return describePipelines(pipelineentry.Binary(lease.Path()), absPath)
}

func PipelineNamesForRepo(absPath string) ([]string, error) {
	schemas, err := DescribeRepo(absPath)
	if err != nil {
		return nil, err
	}
	return pipelineNames(schemas), nil
}

func pipelineNamesIfBuilt(absPath string) (names []string, ok bool) {
	sparkwingDir := filepath.Join(absPath, ".sparkwing")
	if _, err := os.Stat(sparkwingDir); err != nil {
		return nil, false
	}
	ts, err := pipelineentry.IsTypeScript(sparkwingDir)
	if err != nil {
		return nil, false
	}
	if ts {
		entry, err := pipelineentry.TypeScript(sparkwingDir)
		if err != nil {
			return nil, false
		}
		got, err := describePipelines(entry, absPath)
		return pipelineNames(got), err == nil
	}
	hash, err := bincache.PipelineCacheKey(sparkwingDir)
	if err != nil {
		return nil, false
	}
	entry, err := bincache.PipelineEntry(hash)
	if err != nil {
		return nil, false
	}
	lease, found, err := entry.Acquire(context.Background())
	if err != nil || !found {
		return nil, false
	}
	defer func() { _ = lease.Release() }()
	got, err := describePipelines(pipelineentry.Binary(lease.Path()), absPath)
	if err != nil {
		return nil, false
	}
	return pipelineNames(got), true
}

func PipelineNamesIfBuilt(absPath string) (names []string, ok bool) {
	return pipelineNamesIfBuilt(absPath)
}

func describePipelines(entry pipelineentry.Command, workDir string) ([]nodeproto.PipelineDescription, error) {
	doc, err := Describe(context.Background(), entry, workDir)
	if err != nil {
		return nil, fmt.Errorf("describe %s: %w", entry[len(entry)-1], err)
	}
	return doc.Pipelines, nil
}

func pipelineNames(schemas []nodeproto.PipelineDescription) []string {
	names := make([]string, 0, len(schemas))
	for _, s := range schemas {
		if s.Name != "" {
			names = append(names, s.Name)
		}
	}
	return names
}

func ResolveRepoForPipelineCached(name string) (string, error) {
	if name == "" {
		return "", errors.New("ResolveRepoForPipelineCached: empty name")
	}
	cands, err := CandidatePaths()
	if err != nil {
		return "", err
	}
	for _, pass := range []bool{false, true} {
		for _, c := range cands {
			if c.Worktree != pass {
				continue
			}
			names, ok := pipelineNamesIfBuilt(c.Path)
			if !ok {
				continue
			}
			for _, n := range names {
				if n == name {
					return c.Path, nil
				}
			}
		}
	}
	return "", ErrNotFound
}
