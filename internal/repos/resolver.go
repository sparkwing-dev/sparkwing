package repos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
)

var ErrNotFound = errors.New("repos: no registered repo provides that pipeline")

var ErrAmbiguous = errors.New("repos: pipeline name is ambiguous across registered repos")

type describeOutput struct {
	Name string `json:"name"`
}

type resolver struct {
	mu    sync.Mutex
	built bool

	nameToPath map[string]string
}

var defaultResolver = &resolver{}

func ResolveRepoForPipeline(name string) (string, error) {
	if name == "" {
		return "", errors.New("ResolveRepoForPipeline: empty name")
	}
	defaultResolver.mu.Lock()
	defer defaultResolver.mu.Unlock()
	if !defaultResolver.built {
		if err := defaultResolver.build(); err != nil {
			return "", err
		}
	}
	if p, ok := defaultResolver.nameToPath[name]; ok {
		return p, nil
	}
	return "", ErrNotFound
}

func InvalidateCache() {
	defaultResolver.mu.Lock()
	defaultResolver.built = false
	defaultResolver.nameToPath = nil
	defaultResolver.mu.Unlock()
}

func (r *resolver) build() error {
	cands, err := CandidatePaths()
	if err != nil {
		return err
	}
	r.nameToPath = map[string]string{}

	for _, pass := range []bool{false, true} {
		for _, c := range cands {
			if c.Worktree != pass {
				continue
			}
			names, err := PipelineNamesForRepo(c.Path)
			if err != nil {
				continue
			}
			for _, n := range names {
				if _, exists := r.nameToPath[n]; exists {
					continue
				}
				r.nameToPath[n] = c.Path
			}
		}
	}
	r.built = true
	return nil
}

func PipelineNamesForRepo(absPath string) ([]string, error) {
	sparkwingDir := filepath.Join(absPath, ".sparkwing")
	if _, err := os.Stat(sparkwingDir); err != nil {
		return nil, fmt.Errorf("no .sparkwing/ at %s: %w", sparkwingDir, err)
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
	return describePipelineNames(lease.Path(), absPath)
}

func pipelineNamesIfBuilt(absPath string) (names []string, ok bool) {
	sparkwingDir := filepath.Join(absPath, ".sparkwing")
	if _, err := os.Stat(sparkwingDir); err != nil {
		return nil, false
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
	got, err := describePipelineNames(lease.Path(), absPath)
	if err != nil {
		return nil, false
	}
	return got, true
}

func PipelineNamesIfBuilt(absPath string) (names []string, ok bool) {
	return pipelineNamesIfBuilt(absPath)
}

func describePipelineNames(binPath, workDir string) ([]string, error) {
	cmd := exec.Command(binPath, "--describe")
	cmd.Dir = workDir
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("describe %s: %w", binPath, err)
	}
	var schemas []describeOutput
	if err := json.Unmarshal(out, &schemas); err != nil {
		return nil, fmt.Errorf("parse describe output from %s: %w", binPath, err)
	}
	names := make([]string, 0, len(schemas))
	for _, s := range schemas {
		if s.Name != "" {
			names = append(names, s.Name)
		}
	}
	return names, nil
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
