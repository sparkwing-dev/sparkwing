package repos

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

var ErrNotFound = errors.New("repos: no registered repo provides that pipeline")

// DescribeRepo builds the pipeline binary a repository declares, or installs
// its TypeScript pipeline's dependencies, and returns gated discovery.
func DescribeRepo(absPath string) (*runspec.DeclarationGate, error) {
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
		return bincache.CompilePipeline(context.Background(), sparkwingDir, tempPath, bincache.CompileOptions{Environment: os.Environ(), Diagnostics: os.Stderr})
	})
	if err != nil {
		return nil, fmt.Errorf("compile %s: %w", sparkwingDir, err)
	}
	defer func() { _ = lease.Release() }()
	binary, remove, err := installVerified(lease)
	if err != nil {
		return nil, err
	}
	defer remove()
	return describePipelines(pipelineentry.Binary(binary.Path()), absPath)
}

func PipelineNamesForRepo(absPath string) ([]string, error) {
	schemas, err := DescribeRepo(absPath)
	if err != nil {
		return nil, err
	}
	return pipelineNames(schemas.Presentation().Pipelines), nil
}

func describeIfBuilt(absPath string) (*runspec.DeclarationGate, bool) {
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
		return got, err == nil
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
	binary, remove, err := installVerified(lease)
	if err != nil {
		return nil, false
	}
	defer remove()
	got, err := describePipelines(pipelineentry.Binary(binary.Path()), absPath)
	if err != nil {
		return nil, false
	}
	return got, true
}

func PipelineNamesIfBuilt(absPath string) (names []string, ok bool) {
	doc, ok := describeIfBuilt(absPath)
	if !ok {
		return nil, false
	}
	return pipelineNames(doc.Presentation().Pipelines), true
}

func describePipelines(entry pipelineentry.Command, workDir string) (*runspec.DeclarationGate, error) {
	doc, err := Describe(context.Background(), entry, workDir)
	if err != nil {
		return nil, fmt.Errorf("describe %s: %w", entry[len(entry)-1], err)
	}
	return doc, nil
}

func pipelineNames(schemas []runspec.PipelinePresentation) []string {
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
			doc, ok := describeIfBuilt(c.Path)
			if !ok {
				continue
			}
			if _, found := doc.Declarations().Pipeline(name); found {
				return c.Path, nil
			}
		}
	}
	return "", ErrNotFound
}

func installVerified(lease *bincache.Lease) (bincache.VerifiedBinary, func(), error) {
	dir, err := os.MkdirTemp("", "sparkwing-describe-")
	if err != nil {
		return bincache.VerifiedBinary{}, nil, err
	}
	remove := func() { _ = os.RemoveAll(dir) }
	binary, err := lease.Install(filepath.Join(dir, "pipeline"))
	if err != nil {
		remove()
		return bincache.VerifiedBinary{}, nil, err
	}
	return binary, remove, nil
}
