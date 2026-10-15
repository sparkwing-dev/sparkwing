// Package fs reads filesystem objects and measures filesystem usage.
package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

// ArtifactStore reads sharded fixture objects and reports filesystem usage.
type ArtifactStore struct {
	Root string
}

// NewArtifactStore returns an ArtifactStore rooted at root, creating
// the directory if needed.
func NewArtifactStore(root string) (*ArtifactStore, error) {
	if root == "" {
		return nil, errors.New("fs.NewArtifactStore: root required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &ArtifactStore{Root: root}, nil
}

// safety: keys arrive from HTTP path segments, so a key is validated and
// its shard join confirmed inside the root before any filesystem call.
func relPath(key string) (string, error) {
	if err := storage.SafeArtifactKey(key); err != nil {
		return "", err
	}
	shard := "_"
	if len(key) >= 2 {
		shard = key[:2]
	}
	rel := filepath.Join(shard, filepath.FromSlash(key))
	if !filepath.IsLocal(rel) {
		return "", fmt.Errorf("storage: key %q resolves outside the store root", key)
	}
	return rel, nil
}

func (s *ArtifactStore) openRoot(key string) (*os.Root, string, error) {
	rel, err := relPath(key)
	if err != nil {
		return nil, "", err
	}
	root, err := os.OpenRoot(s.Root)
	if err != nil {
		return nil, "", err
	}
	return root, rel, nil
}

func notFound(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return storage.ErrNotFound
	}
	return err
}

func (s *ArtifactStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	root, rel, err := s.openRoot(key)
	if err != nil {
		return nil, notFound(err)
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(rel)
	if err != nil {
		return nil, notFound(err)
	}
	return f, nil
}

// List walks Root and returns every blob whose logical key starts
// with prefix. The on-disk shard segment is stripped so callers see
// the same stored keyspace.
func (s *ArtifactStore) List(_ context.Context, prefix string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(s.Root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if d.IsDir() {
			if rel, relErr := filepath.Rel(s.Root, path); relErr == nil && rel == ".cas-locks" {
				return filepath.SkipDir
			}
			return nil
		}
		name := filepath.Base(path)
		if strings.HasPrefix(name, ".put-") {
			return nil
		}
		rel, err := filepath.Rel(s.Root, path)
		if err != nil {
			return err
		}
		key := keyFromRelPath(rel)
		if key == "" {
			return nil
		}
		if _, err := relPath(key); err != nil {
			return nil
		}
		if prefix == "" || strings.HasPrefix(key, prefix) {
			out = append(out, key)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("fs list %s: %w", prefix, err)
	}
	return out, nil
}

var _ storage.UsageReporter = (*ArtifactStore)(nil)

// Usage totals every file under Root, as an object store totals every
// object under its prefix, leaving out in-flight Put temporaries and the
// conditional-write locks. A walk the caller's context stops early
// reports a partial total.
func (s *ArtifactStore) Usage(ctx context.Context) (storage.StoreUsage, error) {
	var usage storage.StoreUsage
	err := filepath.WalkDir(s.Root, func(path string, d os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if d.IsDir() {
			if rel, relErr := filepath.Rel(s.Root, path); relErr == nil && rel == ".cas-locks" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || strings.HasPrefix(d.Name(), ".put-") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		usage.Objects++
		usage.Bytes += info.Size()
		return nil
	})
	if err != nil {
		if ctx.Err() == nil {
			return storage.StoreUsage{}, fmt.Errorf("fs usage %s: %w", s.Root, err)
		}
		usage.Partial = true
	}
	usage.ObservedAt = time.Now().UTC()
	return usage, nil
}

func keyFromRelPath(rel string) string {
	rel = filepath.ToSlash(rel)
	_, after, ok := strings.Cut(rel, "/")
	if !ok {
		return ""
	}
	return after
}
