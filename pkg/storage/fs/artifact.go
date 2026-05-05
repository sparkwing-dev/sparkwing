// Package fs implements storage.ArtifactStore + storage.LogStore on
// top of the local filesystem.
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
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

// ArtifactStore is a content-addressed blob store under Root. The
// on-disk path is a 2-char shard prefix followed by the rest of the
// key, so 100K-blob trees don't blow up any one directory.
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

var _ storage.ArtifactStore = (*ArtifactStore)(nil)

func (s *ArtifactStore) path(key string) string {
	// Keys shorter than 2 chars share an "_" shard.
	if len(key) >= 2 {
		return filepath.Join(s.Root, key[:2], key)
	}
	return filepath.Join(s.Root, "_", key)
}

func (s *ArtifactStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	f, err := os.Open(s.path(key))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, storage.ErrNotFound
		}
		return nil, err
	}
	return f, nil
}

func (s *ArtifactStore) Put(_ context.Context, key string, r io.Reader) error {
	dst := s.path(key)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".put-*")
	if err != nil {
		return err
	}
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

func (s *ArtifactStore) Has(_ context.Context, key string) (bool, error) {
	_, err := os.Stat(s.path(key))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func (s *ArtifactStore) Delete(_ context.Context, key string) error {
	err := os.Remove(s.path(key))
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("fs delete %s: %w", key, err)
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
		// Skip in-flight tempfiles from atomic Put.
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

// keyFromRelPath inverts ArtifactStore.path by stripping the shard
// segment.
func keyFromRelPath(rel string) string {
	rel = filepath.ToSlash(rel)
	_, after, ok := strings.Cut(rel, "/")
	if !ok {
		return ""
	}
	return after
}
