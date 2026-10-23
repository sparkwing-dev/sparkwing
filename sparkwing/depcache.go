package sparkwing

import (
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/pkg/depcachekey"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

// DirCache declares files restored and saved by the host around a job attempt.
type DirCache struct{ spec nodeproto.CacheSpec }

// KeySource names files whose content determines a dependency cache key.
type KeySource struct{ files []string }

// KeyFromFile declares a lockfile relative to the job's workspace.
func KeyFromFile(path string) KeySource { return KeySource{files: []string{path}} }

// GoModules caches downloaded Go modules using go.sum as its input.
func GoModules() DirCache {
	return DirCache{spec: nodeproto.CacheSpec{Name: "go-modules", Resolver: "go-modules", KeyFiles: []string{"go.sum"}}}
}

// Dir declares a dependency cache rooted at path with explicit key inputs.
func Dir(path string, key KeySource) DirCache {
	return DirCache{spec: nodeproto.CacheSpec{Name: filepath.Base(filepath.Clean(path)), Path: path, KeyScope: filepath.ToSlash(filepath.Clean(path)), KeyFiles: append([]string(nil), key.files...)}}
}

// Key hashes the declaration, platform and first present key file contained in workdir.
func (c DirCache) Key(workdir string) (string, error) {
	return depcachekey.Files(workdir, c.spec.Name, c.spec.KeyScope, c.spec.KeyFiles)
}
