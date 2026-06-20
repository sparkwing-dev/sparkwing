package fs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

var _ storage.ConditionalWriter = (*ArtifactStore)(nil)

// keyMu returns a mutex unique to key, creating it on first use. It
// serializes the read-check-write critical section of a conditional
// write so PutIfAbsent / PutIfMatch are atomic within this process.
// The fs backend serves local-only mode (a single runner); true
// cross-runner CAS is the object-store backends' job.
func (s *ArtifactStore) keyMu(key string) *sync.Mutex {
	m, _ := s.casLocks.LoadOrStore(key, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// GetWithETag returns the object and the sha256 of its bytes as the
// ETag. The ETag feeds back into PutIfMatch to gate the next write.
func (s *ArtifactStore) GetWithETag(_ context.Context, key string) (io.ReadCloser, storage.ETag, error) {
	body, err := os.ReadFile(s.path(key))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", storage.ErrNotFound
		}
		return nil, "", err
	}
	return io.NopCloser(bytes.NewReader(body)), contentETag(body), nil
}

// PutIfAbsent writes only when key has no object. A pre-existing
// object yields ErrPreconditionFailed.
func (s *ArtifactStore) PutIfAbsent(_ context.Context, key string, r io.Reader) (storage.ETag, error) {
	mu := s.keyMu(key)
	mu.Lock()
	defer mu.Unlock()

	if _, err := os.Stat(s.path(key)); err == nil {
		return "", storage.ErrPreconditionFailed
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return s.writeAtomic(key, r)
}

// PutIfMatch writes only when the current object's ETag equals expect.
// A differing or absent object yields ErrPreconditionFailed.
func (s *ArtifactStore) PutIfMatch(_ context.Context, key string, r io.Reader, expect storage.ETag) (storage.ETag, error) {
	mu := s.keyMu(key)
	mu.Lock()
	defer mu.Unlock()

	cur, err := os.ReadFile(s.path(key))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", storage.ErrPreconditionFailed
		}
		return "", err
	}
	if contentETag(cur) != expect {
		return "", storage.ErrPreconditionFailed
	}
	return s.writeAtomic(key, r)
}

// ConditionalWritesSupported is always true for the local filesystem:
// the keyed mutex plus atomic rename enforce preconditions
// deterministically within the process.
func (s *ArtifactStore) ConditionalWritesSupported(context.Context) (bool, error) {
	return true, nil
}

// writeAtomic writes r to key via a temp file + rename and returns the
// new content ETag. Callers hold the key mutex.
func (s *ArtifactStore) writeAtomic(key string, r io.Reader) (storage.ETag, error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	dst := s.path(key)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".put-*")
	if err != nil {
		return "", err
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	return contentETag(body), nil
}

func contentETag(body []byte) storage.ETag {
	sum := sha256.Sum256(body)
	return storage.ETag(hex.EncodeToString(sum[:]))
}
