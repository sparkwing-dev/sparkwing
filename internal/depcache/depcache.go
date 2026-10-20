// Package depcache restores and saves dependency directories around a job.
package depcache

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/depcachekey"
	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/tarsafe"
)

// Resolver names a cache directory the host finds with the ecosystem's own
// tool, where the node runs.
const (
	ResolverGoModules = "go-modules"
	ResolverNpm       = "npm"
)

// Spec is one declared dependency-directory cache, as the plan snapshot
// carries it.
type Spec struct {
	// Name labels the cache in keys and logs.
	Name string `json:"name"`
	// Path is the directory, relative to the work dir or absolute. Empty
	// when Resolver names it.
	Path string `json:"path,omitempty"`
	// Resolver is [ResolverGoModules] or [ResolverNpm]; empty uses Path.
	Resolver string `json:"resolver,omitempty"`
	// KeyScope is hashed ahead of the key file so two directories keyed by
	// the same lockfile get different keys.
	KeyScope string `json:"key_scope,omitempty"`
	// KeyFiles are the candidate key files; the first that exists keys the
	// cache.
	KeyFiles []string `json:"key_files"`
}

// TargetDir resolves the directory the cache restores into.
func (s Spec) TargetDir(workdir string) (string, error) {
	switch s.Resolver {
	case ResolverGoModules:
		return resolveGoModCache()
	case ResolverNpm:
		return resolveNpmCacheDir(workdir)
	case "":
	default:
		return "", fmt.Errorf("unknown cache resolver %q", s.Resolver)
	}
	if !filepath.IsLocal(s.Path) {
		return "", fmt.Errorf("cache path %q must be relative and stay inside the workspace", s.Path)
	}
	return filepath.Join(workdir, s.Path), nil
}

var dirLocks sync.Map

func lockDir(dir string) func() {
	m, _ := dirLocks.LoadOrStore(filepath.Clean(dir), &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// Run is one node's use of one cache: [Restore] fills it, [Run.Save]
// reads it. Every step is best-effort and logs instead of failing the node.
type Run struct {
	spec Spec
	node string

	disabled   bool
	key        string
	dir        string
	missed     bool
	emptyStart bool
}

// Restore resolves the cache's key and directory, and on an exact key hit
// fills an empty directory from the backend. It never returns an error: a
// missing key file, unreachable backend or bad archive logs a warning and
// leaves the node to run uncached.
func Restore(ctx context.Context, spec Spec, node, workdir string) *Run {
	st := &Run{spec: spec, node: node}
	st.restore(ctx, workdir)
	return st
}

// RestoreAll restores each of a node's caches into workdir, or into the
// process's working directory when workdir is empty. Pass the result to
// [SaveAll] once the node has finished.
func RestoreAll(ctx context.Context, specs []Spec, node, workdir string) []*Run {
	if len(specs) == 0 {
		return nil
	}
	if workdir == "" {
		workdir = "."
		if cwd, err := os.Getwd(); err == nil {
			workdir = cwd
		}
	}
	runs := make([]*Run, len(specs))
	for i, spec := range specs {
		runs[i] = Restore(ctx, spec, node, workdir)
	}
	return runs
}

// SaveAll saves each restored cache; runErr non-nil skips them all.
func SaveAll(ctx context.Context, runs []*Run, runErr error) {
	for _, r := range runs {
		r.Save(ctx, runErr)
	}
}

func (st *Run) restore(ctx context.Context, workdir string) {
	key, err := depcachekey.Files(workdir, st.spec.Name, st.spec.KeyScope, st.spec.KeyFiles)
	if err != nil {
		slog.Warn("depcache: key derivation failed; dependency cache disabled for this run",
			"node", st.node, "cache", st.spec.Name, "err", err)
		st.disabled = true
		return
	}
	st.key = key

	dir, err := st.spec.TargetDir(workdir)
	if err != nil {
		slog.Warn("depcache: cannot resolve cache directory; dependency cache disabled for this run",
			"node", st.node, "cache", st.spec.Name, "err", err)
		st.disabled = true
		return
	}
	st.dir = dir

	notEmpty := dirHasEntries(dir)
	st.emptyStart = !notEmpty

	unlock := lockDir(dir)
	defer unlock()

	b := selectBackend(ctx)
	hit, err := b.Exists(ctx, key)
	if err != nil {
		slog.Warn("depcache: backend probe failed; running uncached",
			"node", st.node, "cache", st.spec.Name, "backend", b.Label(), "err", err)
		st.disabled = true
		return
	}
	if !hit {
		st.missed = true
		slog.Info("depcache: miss; will save after success",
			"node", st.node, "cache", st.spec.Name, "key", key, "backend", b.Label())
		return
	}

	if notEmpty {
		slog.Info("depcache: directory already has content; skipping restore",
			"node", st.node, "cache", st.spec.Name, "dir", dir)
		return
	}

	start := time.Now()
	size, err := b.Fetch(ctx, key, dir)
	if err != nil {
		st.missed = true
		slog.Warn("depcache: restore failed; running uncached",
			"node", st.node, "cache", st.spec.Name, "key", key, "err", err)
		return
	}
	slog.Info(fmt.Sprintf("depcache: restored %s (%s) in %s",
		st.spec.Name, tarsafe.HumanBytes(size), time.Since(start).Round(100*time.Millisecond)),
		"node", st.node, "key", key, "backend", b.Label())
}

// Save archives the directory after a successful run whose restore missed
// and found the directory empty. runErr non-nil skips the save.
func (st *Run) Save(ctx context.Context, runErr error) {
	if st.disabled || !st.missed || runErr != nil || st.key == "" {
		return
	}

	if !st.emptyStart {
		return
	}

	unlock := lockDir(st.dir)
	defer unlock()

	if !dirHasEntries(st.dir) {
		slog.Warn("depcache: nothing to save (directory missing or empty)",
			"node", st.node, "cache", st.spec.Name, "dir", st.dir)
		return
	}

	b := selectBackend(ctx)
	start := time.Now()
	size, err := b.Store(ctx, st.key, st.dir)
	if err != nil {
		slog.Warn("depcache: save failed; next run will re-download",
			"node", st.node, "cache", st.spec.Name, "key", st.key, "err", err)
		return
	}
	slog.Info(fmt.Sprintf("depcache: saved %s (%s) in %s",
		st.spec.Name, tarsafe.HumanBytes(size), time.Since(start).Round(100*time.Millisecond)),
		"node", st.node, "key", st.key, "backend", b.Label())
}

func resolveNpmCacheDir(workdir string) (string, error) {
	if v := os.Getenv("npm_config_cache"); v != "" {
		return v, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if command, err := pipelineentry.NPMCommand(ctx, goruntime.GOOS, exec.LookPath, "config", "get", "cache"); err == nil {
		command.Dir = workdir
		var output bytes.Buffer
		command.Stdout = &output
		if err := gotoolchain.Run(ctx, command); err == nil {
			if v := strings.TrimSpace(output.String()); v != "" && v != "undefined" {
				return v, nil
			}
		}
	}
	return defaultNpmCacheDir(goruntime.GOOS)
}

func defaultNpmCacheDir(goos string) (string, error) {
	if goos == "windows" {
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			return filepath.Join(local, "npm-cache"), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve npm cache dir: %w", err)
	}
	if goos == "windows" {
		return filepath.Join(home, "AppData", "Local", "npm-cache"), nil
	}
	return filepath.Join(home, ".npm"), nil
}

func resolveGoModCache() (string, error) {
	if v := os.Getenv("GOMODCACHE"); v != "" {
		return v, nil
	}
	if out, err := exec.Command("go", "env", "GOMODCACHE").Output(); err == nil {
		if v := strings.TrimSpace(string(out)); v != "" {
			return v, nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve GOMODCACHE: %w", err)
	}
	return filepath.Join(home, "go", "pkg", "mod"), nil
}

// safety: an unreadable directory counts as empty, so a restore may write
// into it and fail loudly there rather than being skipped as warm.
func dirHasEntries(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}
