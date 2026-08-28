package sparkwing

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
)

// DirCache describes one directory a node restores before running and
// saves after succeeding: a dependency directory keyed by the content
// of the lockfile that produced it. Construct one with [GoModules],
// [NpmCache], or the generic [Dir], and attach it with
// [JobNode.CacheDir].
//
// Ecosystem helpers cache the package manager's content-addressed
// store (GOMODCACHE, the npm cache), not the materialized install
// output: a store restored under a stale key is safe by construction
// -- the tool downloads only what is missing -- where a stale install
// tree is not. [Dir] callers point at arbitrary directories and own
// those semantics themselves.
//
// A DirCache is best-effort by contract. A missing lockfile, an
// unreachable cache service, or a failed archive logs a warning and
// the node runs as if no cache were declared; no cache condition ever
// fails a node.
type DirCache struct {
	name string

	path string

	keyScope string

	resolvePath func() (string, error)

	keyFiles []string
}

// KeySource names the file whose content keys a [Dir] cache.
type KeySource struct {
	files []string
}

// KeyFromFile keys a [Dir] cache on the content of one file,
// typically a lockfile. The path resolves against [WorkDir]. Editing
// the file changes the key; restoring its previous bytes restores the
// previous key.
func KeyFromFile(path string) KeySource {
	return KeySource{files: []string{path}}
}

// GoModules caches the Go module download cache, keyed on go.sum.
//
//	sparkwing.Job(plan, "test", runTests).
//	    CacheDir(sparkwing.GoModules())
//
// The directory comes from GOMODCACHE (the environment variable, then
// `go env GOMODCACHE`, then $HOME/go/pkg/mod), resolved where the
// node runs, so the same declaration lands on the right directory on
// a laptop and in a runner pod.
func GoModules() DirCache {
	return DirCache{
		name:        "go-modules",
		resolvePath: resolveGoModCache,
		keyFiles:    []string{"go.sum"},
	}
}

// NpmCache caches npm's content-addressed cache directory, keyed on
// package-lock.json.
//
// It deliberately caches npm's store (`npm config get cache`,
// default ~/.npm) rather than node_modules: `npm ci` deletes
// node_modules before installing, so a restored install tree is
// discarded bytes, while a restored store makes the reinstall fast.
// The store is also safe under a stale key -- npm tops up only what
// is missing. To knowingly cache a materialized node_modules for an
// `npm install` flow, reach for [Dir]; staleness semantics are then
// yours.
//
// The directory comes from npm_config_cache, then
// `npm config get cache`, then $HOME/.npm, resolved where the node
// runs.
func NpmCache() DirCache {
	return DirCache{
		name:        "npm",
		resolvePath: resolveNpmCacheDir,
		keyFiles:    []string{"package-lock.json"},
	}
}

// Dir declares a cache over an arbitrary directory, keyed by an
// explicit [KeySource]. The escape hatch for ecosystems without a
// dedicated helper:
//
//	.CacheDir(sparkwing.Dir("vendor/bundle", sparkwing.KeyFromFile("Gemfile.lock")))
//
// name in keys and logs is derived from the directory's base name.
func Dir(path string, key KeySource) DirCache {
	return DirCache{
		name:     filepath.Base(filepath.Clean(path)),
		path:     path,
		keyScope: filepath.ToSlash(filepath.Clean(path)),
		keyFiles: key.files,
	}
}

// CacheDir registers dependency-directory caches on the node: each
// declared directory is restored from the cache before the node's Run
// (on an exact key hit) and saved back after a successful Run whose
// restore missed. The key is derived from the lockfile's content plus
// GOOS/GOARCH, so a bumped dependency or a different platform gets a
// fresh cache instead of a poisoned hit.
//
//	sparkwing.Job(plan, "test", runTests).
//	    CacheDir(sparkwing.GoModules())
//
// Storage follows the environment: a laptop run uses
// $SPARKWING_HOME/depcache, a cluster run uses the sparkwing-cache
// service when SPARKWING_CACHE_URL or SPARKWING_GITCACHE_URL is set.
// The archive format is identical in both, which is what makes a
// pipeline behave the same everywhere.
//
// Every cache operation is best-effort: a missing lockfile, an
// unreachable backend, or an oversized archive logs a warning and
// never fails the node. Restores are skipped when the target
// directory already has content (a warm runner's cache is left
// alone). Structural misuse -- a [Dir] with an empty path or no key
// file -- panics at plan construction like other SDK structural
// errors.
func (n *JobNode) CacheDir(caches ...DirCache) *JobNode {
	for _, c := range caches {
		if c.path == "" && c.resolvePath == nil {
			panic("sparkwing: CacheDir: Dir path must not be empty")
		}
		if len(c.keyFiles) == 0 {
			panic(fmt.Sprintf("sparkwing: CacheDir: %s: key source must name at least one file", c.name))
		}
		n.dirCaches = append(n.dirCaches, c)
		st := &dirCacheRun{spec: c, node: n.id}
		n.BeforeRun(st.restore)
		n.AfterRun(st.save)
	}
	return n
}

// DirCaches returns the node's declared dependency-directory caches,
// in declaration order. Empty when [JobNode.CacheDir] was not called.
func (n *JobNode) DirCaches() []DirCache { return n.dirCaches }

var depCacheKeyRE = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,128}$`)

var depCacheDirLocks sync.Map

func lockDepCacheDir(dir string) func() {
	m, _ := depCacheDirLocks.LoadOrStore(filepath.Clean(dir), &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

type dirCacheRun struct {
	spec dirCacheSpecAlias
	node string

	disabled   bool
	key        string
	dir        string
	missed     bool
	emptyStart bool
}

type dirCacheSpecAlias = DirCache

func (st *dirCacheRun) restore(ctx context.Context) error {
	workdir := depCacheWorkdir()

	lockPath, ok := firstExisting(workdir, st.spec.keyFiles)
	if !ok {
		slog.Warn("depcache: no key file found; dependency cache disabled for this run",
			"node", st.node, "cache", st.spec.name, "looked_for", strings.Join(st.spec.keyFiles, ", "), "workdir", workdir)
		st.disabled = true
		return nil
	}

	key, err := deriveDepCacheKey(st.spec.name, st.spec.keyScope, lockPath)
	if err != nil {
		slog.Warn("depcache: key derivation failed; dependency cache disabled for this run",
			"node", st.node, "cache", st.spec.name, "err", err)
		st.disabled = true
		return nil
	}
	st.key = key

	dir, err := st.spec.targetDir(workdir)
	if err != nil {
		slog.Warn("depcache: cannot resolve cache directory; dependency cache disabled for this run",
			"node", st.node, "cache", st.spec.name, "err", err)
		st.disabled = true
		return nil
	}
	st.dir = dir

	notEmpty, _ := dirHasEntries(dir)
	st.emptyStart = !notEmpty

	unlock := lockDepCacheDir(dir)
	defer unlock()

	backend := selectDepCacheBackend()
	hit, err := backend.exists(ctx, key)
	if err != nil {
		slog.Warn("depcache: backend probe failed; running uncached",
			"node", st.node, "cache", st.spec.name, "backend", backend.label(), "err", err)
		st.disabled = true
		return nil
	}
	if !hit {
		st.missed = true
		slog.Info("depcache: miss; will save after success",
			"node", st.node, "cache", st.spec.name, "key", key, "backend", backend.label())
		return nil
	}

	if notEmpty {
		slog.Info("depcache: directory already has content; skipping restore",
			"node", st.node, "cache", st.spec.name, "dir", dir)
		return nil
	}

	start := time.Now()
	size, err := backend.fetch(ctx, key, dir)
	if err != nil {
		slog.Warn("depcache: restore failed; running uncached",
			"node", st.node, "cache", st.spec.name, "key", key, "err", err)
		return nil
	}
	slog.Info(fmt.Sprintf("depcache: restored %s (%s) in %s",
		st.spec.name, humanBytes(size), time.Since(start).Round(100*time.Millisecond)),
		"node", st.node, "key", key, "backend", backend.label())
	return nil
}

func (st *dirCacheRun) save(ctx context.Context, runErr error) {
	if st.disabled || !st.missed || runErr != nil || st.key == "" {
		return
	}

	if !st.emptyStart {
		return
	}

	unlock := lockDepCacheDir(st.dir)
	defer unlock()

	if hasEntries, _ := dirHasEntries(st.dir); !hasEntries {
		slog.Warn("depcache: nothing to save (directory missing or empty)",
			"node", st.node, "cache", st.spec.name, "dir", st.dir)
		return
	}

	backend := selectDepCacheBackend()
	start := time.Now()
	size, err := backend.store(ctx, st.key, st.dir)
	if err != nil {
		slog.Warn("depcache: save failed; next run will re-download",
			"node", st.node, "cache", st.spec.name, "key", st.key, "err", err)
		return
	}
	slog.Info(fmt.Sprintf("depcache: saved %s (%s) in %s",
		st.spec.name, humanBytes(size), time.Since(start).Round(100*time.Millisecond)),
		"node", st.node, "key", st.key, "backend", backend.label())
}

func (c DirCache) targetDir(workdir string) (string, error) {
	if c.resolvePath != nil {
		return c.resolvePath()
	}
	if filepath.IsAbs(c.path) {
		return c.path, nil
	}
	return filepath.Join(workdir, c.path), nil
}

func deriveDepCacheKey(name, scope, lockPath string) (string, error) {
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
		cacheSegment(name, "dir"), goruntime.GOOS, goruntime.GOARCH, hex.EncodeToString(sum[:8]))
	if !depCacheKeyRE.MatchString(key) {
		return "", fmt.Errorf("derived key %q is not cache-safe", key)
	}
	return key, nil
}

func depCacheWorkdir() string {
	if wd := WorkDir(); wd != "" {
		return wd
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return "."
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

func dirHasEntries(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return len(entries) > 0, nil
}

func humanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTP"[exp])
}
