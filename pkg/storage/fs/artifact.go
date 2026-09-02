// Package fs implements storage.ArtifactStore + storage.LogStore on
// top of the local filesystem. Both surfaces run the shared
// pkg/storage/conformance suites.
//
// Layout under Root:
//
//	artifacts/<aa>/<rest>            content-keyed blobs (sha-prefix shard)
//	logs/<runID>/<nodeID>.ndjson     per-node JSONL log
//
// Both stores write atomically (tmp file + rename).
package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

// ArtifactStore is a content-addressed blob store under Root. The
// on-disk path is a 2-char shard prefix followed by the rest of the
// key, so 100K-blob trees don't blow up any one directory.
type ArtifactStore struct {
	Root string

	casLocks sync.Map
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

var _ storage.ArtifactStore = (*ArtifactStore)(nil)

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

func (s *ArtifactStore) Put(_ context.Context, key string, r io.Reader) error {
	if err := os.MkdirAll(s.Root, 0o755); err != nil {
		return err
	}
	root, rel, err := s.openRoot(key)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return writeThroughRoot(root, rel, r)
}

func writeThroughRoot(root *os.Root, rel string, r io.Reader) error {
	dir := filepath.Dir(rel)
	if err := root.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, tmpRel, err := createTemp(root, dir)
	if err != nil {
		return err
	}
	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		_ = root.Remove(tmpRel)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = root.Remove(tmpRel)
		return err
	}
	if err := root.Rename(tmpRel, rel); err != nil {
		_ = root.Remove(tmpRel)
		return err
	}
	return nil
}

func createTemp(root *os.Root, dir string) (*os.File, string, error) {
	for range 1000 {
		name := filepath.Join(dir, ".put-"+strconv.FormatUint(rand.Uint64(), 36))
		f, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return f, name, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, "", err
		}
	}
	return nil, "", errors.New("fs: no free temp file name")
}

func (s *ArtifactStore) Has(_ context.Context, key string) (bool, error) {
	root, rel, err := s.openRoot(key)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer func() { _ = root.Close() }()
	if _, err := root.Stat(rel); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *ArtifactStore) Delete(_ context.Context, key string) error {
	root, rel, err := s.openRoot(key)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("fs delete %s: %w", key, err)
	}
	defer func() { _ = root.Close() }()
	if err := root.Remove(rel); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("fs delete %s: %w", key, err)
	}
	return nil
}

// List walks Root and returns every blob whose logical key starts
// with prefix. The on-disk shard segment is stripped so callers see
// the same keyspace they Put under.
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

func keyFromRelPath(rel string) string {
	rel = filepath.ToSlash(rel)
	_, after, ok := strings.Cut(rel, "/")
	if !ok {
		return ""
	}
	return after
}
