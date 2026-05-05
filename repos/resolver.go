// Pipeline-name -> repo-path resolution. The piece that lets
// AwaitPipelineJob(ctx, "lint", "") work without a per-call
// WithAwaitRepo("owner/name") annotation: we iterate the registry
// (explicit repos first, then fallback_paths/*), shell out
// `<binary> --describe` against each candidate's compiled pipeline
// binary, and return the first path whose registered pipelines
// include the requested name.
package repos

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/sparkwing-dev/sparkwing/bincache"
)

// ErrNotFound is returned by ResolveRepoForPipeline when no
// registered repo (and no fallback path) declares a pipeline by
// the given name.
var ErrNotFound = errors.New("repos: no registered repo provides that pipeline")

// ErrAmbiguous is reserved for the future "multiple registered
// repos provide the same pipeline name and we can't pick" case. v1
// uses a deterministic priority order (explicit-before-fallback,
// non-worktree-before-worktree, declaration-order tiebreak) and
// just picks; if that turns out to be wrong, we'll surface this.
var ErrAmbiguous = errors.New("repos: pipeline name is ambiguous across registered repos")

// describeOutput mirrors the JSON shape emitted by
// `<binary> --describe`. We only need the names; everything else
// (Args, Examples, Help) is ignored.
type describeOutput struct {
	Name string `json:"name"`
}

// resolver memoizes describe lookups for a single process. Built
// lazily on first call; reused for every subsequent ResolveRepoForPipeline.
// Concurrent-safe so parallel awaits in the same run don't race
// on the rebuild.
type resolver struct {
	mu    sync.Mutex
	built bool
	// nameToPath is keyed by pipeline name. Value is the absolute
	// repo path (the .sparkwing/'s parent dir). First-write wins
	// in build order, so explicit-before-fallback and
	// non-worktree-before-worktree are encoded in the build loop.
	nameToPath map[string]string
}

var defaultResolver = &resolver{}

// ResolveRepoForPipeline returns the absolute path to the registered
// repo whose .sparkwing/ defines pipeline name. Errors:
//   - ErrNotFound if nothing matches.
//   - Underlying I/O / describe errors if the registry can't be
//     read or every candidate's describe call fails (the function
//     keeps trying after individual describe failures, so a single
//     broken checkout doesn't break the whole lookup).
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

// InvalidateCache clears the in-memory describe cache. Useful in
// tests that mutate the registry mid-process. Production code
// shouldn't need it -- describe results don't change underneath
// a running wing invocation.
func InvalidateCache() {
	defaultResolver.mu.Lock()
	defaultResolver.built = false
	defaultResolver.nameToPath = nil
	defaultResolver.mu.Unlock()
}

// build populates nameToPath by scanning every CandidatePath in
// priority order. Non-worktree candidates are scanned first so
// they win on tie; within each group, declaration order from
// repos.yaml is preserved.
func (r *resolver) build() error {
	cands, err := CandidatePaths()
	if err != nil {
		return err
	}
	r.nameToPath = map[string]string{}

	// Two-pass to encode "non-worktree wins on tie": pass 1 fills
	// from regular checkouts, pass 2 fills any names still empty
	// from worktrees. Within each pass we stay in declaration
	// order so the user can promote a primary repo by listing it
	// first in repos.yaml.
	for _, pass := range []bool{false, true} {
		for _, c := range cands {
			if c.Worktree != pass {
				continue
			}
			names, err := PipelineNamesForRepo(c.Path)
			if err != nil {
				// Don't fail the whole resolve on one broken
				// checkout -- a half-deleted repo or a compile
				// failure in a fallback shouldn't block resolution
				// against the others. Stderr already got the gory
				// detail when describe was invoked.
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

// PipelineNamesForRepo runs `<binary> --describe` against the
// compiled pipeline binary for absPath/.sparkwing/ and returns
// the set of registered pipeline names. The binary is fetched
// from (or built into) the bincache, so repeat calls within a
// stable .sparkwing/ tree are basically free.
func PipelineNamesForRepo(absPath string) ([]string, error) {
	sparkwingDir := filepath.Join(absPath, ".sparkwing")
	if _, err := os.Stat(sparkwingDir); err != nil {
		return nil, fmt.Errorf("no .sparkwing/ at %s: %w", sparkwingDir, err)
	}
	hash, err := bincache.PipelineCacheKey(sparkwingDir)
	if err != nil {
		return nil, fmt.Errorf("hash %s: %w", sparkwingDir, err)
	}
	binPath := bincache.CachedBinaryPath(hash)
	if _, err := os.Stat(binPath); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat binary cache: %w", err)
		}
		if err := bincache.CompilePipeline(sparkwingDir, binPath); err != nil {
			return nil, fmt.Errorf("compile %s: %w", sparkwingDir, err)
		}
	}
	cmd := exec.Command(binPath, "--describe")
	cmd.Dir = absPath
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
