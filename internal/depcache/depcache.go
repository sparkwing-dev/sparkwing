// Package depcache restores and saves the dependency directories a node
// declares with sparkwing's JobNode.CacheDir. The SDK records each
// declaration as a [Spec]; the node host restores before the node runs and
// saves after it succeeds, so the key rules, archive format and backends
// live in one place for every SDK.
package depcache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/tarsafe"
)

// Resolver names a cache directory the host finds with the ecosystem's own
// tool, where the node runs.
const (
	ResolverGoModules = "gomod"
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
		return resolveNpmCacheDir()
	case "":
	default:
		return "", fmt.Errorf("unknown cache resolver %q", s.Resolver)
	}
	if filepath.IsAbs(s.Path) {
		return s.Path, nil
	}
	return filepath.Join(workdir, s.Path), nil
}

// Segment maps s to a cache-key-safe token, or fallback when nothing safe
// remains.
func Segment(s, fallback string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, s)
	if safe = strings.Trim(safe, "-"); safe == "" {
		return fallback
	}
	return safe
}

var keyRE = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,128}$`)

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

func (st *Run) restore(ctx context.Context, workdir string) {
	lockPath, ok := firstExisting(workdir, st.spec.KeyFiles)
	if !ok {
		slog.Warn("depcache: no key file found; dependency cache disabled for this run",
			"node", st.node, "cache", st.spec.Name, "looked_for", strings.Join(st.spec.KeyFiles, ", "), "workdir", workdir)
		st.disabled = true
		return
	}

	key, err := deriveKey(st.spec.Name, st.spec.KeyScope, lockPath)
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

	b := selectBackend()
	hit, err := b.exists(ctx, key)
	if err != nil {
		slog.Warn("depcache: backend probe failed; running uncached",
			"node", st.node, "cache", st.spec.Name, "backend", b.label(), "err", err)
		st.disabled = true
		return
	}
	if !hit {
		st.missed = true
		slog.Info("depcache: miss; will save after success",
			"node", st.node, "cache", st.spec.Name, "key", key, "backend", b.label())
		return
	}

	if notEmpty {
		slog.Info("depcache: directory already has content; skipping restore",
			"node", st.node, "cache", st.spec.Name, "dir", dir)
		return
	}

	start := time.Now()
	size, err := b.fetch(ctx, key, dir)
	if err != nil {
		slog.Warn("depcache: restore failed; running uncached",
			"node", st.node, "cache", st.spec.Name, "key", key, "err", err)
		return
	}
	slog.Info(fmt.Sprintf("depcache: restored %s (%s) in %s",
		st.spec.Name, tarsafe.HumanBytes(size), time.Since(start).Round(100*time.Millisecond)),
		"node", st.node, "key", key, "backend", b.label())
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

	b := selectBackend()
	start := time.Now()
	size, err := b.store(ctx, st.key, st.dir)
	if err != nil {
		slog.Warn("depcache: save failed; next run will re-download",
			"node", st.node, "cache", st.spec.Name, "key", st.key, "err", err)
		return
	}
	slog.Info(fmt.Sprintf("depcache: saved %s (%s) in %s",
		st.spec.Name, tarsafe.HumanBytes(size), time.Since(start).Round(100*time.Millisecond)),
		"node", st.node, "key", st.key, "backend", b.label())
}

func deriveKey(name, scope, lockPath string) (string, error) {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return "", fmt.Errorf("read key file %s: %w", lockPath, err)
	}
	h := sha256.New()
	h.Write([]byte(scope))
	h.Write([]byte{0})
	h.Write(data)
	sum := h.Sum(nil)
	key := fmt.Sprintf("dep-%s-%s-%s-%s",
		Segment(name, "dir"), goruntime.GOOS, goruntime.GOARCH, hex.EncodeToString(sum[:8]))
	if !keyRE.MatchString(key) {
		return "", fmt.Errorf("derived key %q is not cache-safe", key)
	}
	return key, nil
}

func resolveNpmCacheDir() (string, error) {
	if v := os.Getenv("npm_config_cache"); v != "" {
		return v, nil
	}
	if out, err := exec.Command("npm", "config", "get", "cache").Output(); err == nil {
		if v := strings.TrimSpace(string(out)); v != "" && v != "undefined" {
			return v, nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve npm cache dir: %w", err)
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

func firstExisting(workdir string, candidates []string) (string, bool) {
	for _, c := range candidates {
		p := c
		if !filepath.IsAbs(p) {
			p = filepath.Join(workdir, c)
		}
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return p, true
		}
	}
	return "", false
}

// safety: an unreadable directory counts as empty, so a restore may write
// into it and fail loudly there rather than being skipped as warm.
func dirHasEntries(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}
