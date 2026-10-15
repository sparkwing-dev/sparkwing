package sparkwing

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"os"
	"path/filepath"
	"runtime"
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

// Key hashes the cache declaration, platform and first present key file inside workdir.
// Moving a checkout preserves the key. Missing files, escaping paths and read failures return errors.
// The key names content only. Hosts still authorize the repository and ref used to store it.
func (c DirCache) Key(workdir string) (string, error) {
	root, err := os.OpenRoot(workdir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	for _, file := range c.spec.KeyFiles {
		if !filepath.IsLocal(file) {
			return "", fmt.Errorf("cache key file must be workspace-relative")
		}
	}
	for _, file := range c.spec.KeyFiles {
		data, err := root.ReadFile(file)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("read cache key input: %w", err)
		}
		raw, err := json.Marshal([]string{c.spec.Name, c.spec.KeyScope, runtime.GOOS, runtime.GOARCH})
		if err != nil {
			return "", err
		}
		digest := sha256.New()
		digest.Write(raw)
		digest.Write([]byte{0})
		digest.Write(data)
		return fmt.Sprintf("dep-%x", digest.Sum(nil)), nil
	}
	return "", fmt.Errorf("cache key input: %w", os.ErrNotExist)
}
