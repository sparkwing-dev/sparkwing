package sparkwing

import (
	"fmt"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/depcache"
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
	spec depcache.Spec
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
	return DirCache{spec: depcache.Spec{
		Name:     "go-modules",
		Resolver: depcache.ResolverGoModules,
		KeyFiles: []string{"go.sum"},
	}}
}

// NpmCache caches npm's content-addressed cache directory, keyed on
// package-lock.json.
//
// It caches npm's store (`npm config get cache`,
// default ~/.npm on Unix and %LOCALAPPDATA%/npm-cache on Windows) rather
// than node_modules: `npm ci` deletes
// node_modules before installing, so a restored install tree is
// discarded bytes, while a restored store makes the reinstall fast.
// The store is also safe under a stale key -- npm tops up only what
// is missing. To knowingly cache a materialized node_modules for an
// `npm install` flow, reach for [Dir]; staleness semantics are then
// yours.
//
// The directory comes from npm_config_cache, then
// `npm config get cache`, then the platform default, resolved where the node
// runs.
func NpmCache() DirCache {
	return DirCache{spec: depcache.Spec{
		Name:     "npm",
		Resolver: depcache.ResolverNpm,
		KeyFiles: []string{"package-lock.json"},
	}}
}

// Dir declares a cache over an arbitrary directory, keyed by an
// explicit [KeySource]. The escape hatch for ecosystems without a
// dedicated helper:
//
//	.CacheDir(sparkwing.Dir("vendor/bundle", sparkwing.KeyFromFile("Gemfile.lock")))
//
// name in keys and logs is derived from the directory's base name.
func Dir(path string, key KeySource) DirCache {
	return DirCache{spec: depcache.Spec{
		Name:     filepath.Base(filepath.Clean(path)),
		Path:     path,
		KeyScope: filepath.ToSlash(filepath.Clean(path)),
		KeyFiles: key.files,
	}}
}

// CacheDir registers dependency-directory caches on the node: the
// engine restores each declared directory from the cache before the
// node's BeforeRun hooks and Run (on an exact key hit) and saves it back
// after a successful Run and its AfterRun hooks when the restore missed.
// The key is derived from the lockfile's content plus GOOS/GOARCH, so a
// bumped dependency or a different platform gets a fresh cache instead
// of a poisoned hit.
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
		if c.spec.Path == "" && c.spec.Resolver == "" {
			panic("sparkwing: CacheDir: Dir path must not be empty")
		}
		if len(c.spec.KeyFiles) == 0 {
			panic(fmt.Sprintf("sparkwing: CacheDir: %s: key source must name at least one file", c.spec.Name))
		}
		n.dirCaches = append(n.dirCaches, c)
	}
	return n
}

// DirCaches returns the node's declared dependency-directory caches,
// in declaration order. Empty when [JobNode.CacheDir] was not called.
func (n *JobNode) DirCaches() []DirCache { return n.dirCaches }

func dirCacheSpecs(n *JobNode) []depcache.Spec {
	if len(n.dirCaches) == 0 {
		return nil
	}
	out := make([]depcache.Spec, len(n.dirCaches))
	for i, c := range n.dirCaches {
		out[i] = c.spec
	}
	return out
}
