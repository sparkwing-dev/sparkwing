package fs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sync"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

var _ storage.ConditionalWriter = (*ArtifactStore)(nil)

func (s *ArtifactStore) keyMu(key string) *sync.Mutex {
	m, _ := s.casLocks.LoadOrStore(key, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// GetWithETag returns the object and the sha256 of its bytes as the
// ETag. The ETag feeds back into PutIfMatch to gate the next write.
func (s *ArtifactStore) GetWithETag(_ context.Context, key string) (io.ReadCloser, storage.ETag, error) {
	root, rel, err := s.openRoot(key)
	if err != nil {
		return nil, "", notFound(err)
	}
	defer func() { _ = root.Close() }()
	body, err := root.ReadFile(rel)
	if err != nil {
		return nil, "", notFound(err)
	}
	return io.NopCloser(bytes.NewReader(body)), contentETag(body), nil
}

// PutIfAbsent writes only when key has no object. A pre-existing
// object yields ErrPreconditionFailed.
func (s *ArtifactStore) PutIfAbsent(ctx context.Context, key string, r io.Reader) (storage.ETag, error) {
	mu := s.keyMu(key)
	mu.Lock()
	defer mu.Unlock()

	has, err := s.Has(ctx, key)
	if err != nil {
		return "", err
	}
	if has {
		return "", storage.ErrPreconditionFailed
	}
	return s.writeAtomic(ctx, key, r)
}

// PutIfMatch writes only when the current object's ETag equals expect.
// A differing or absent object yields ErrPreconditionFailed.
func (s *ArtifactStore) PutIfMatch(ctx context.Context, key string, r io.Reader, expect storage.ETag) (storage.ETag, error) {
	mu := s.keyMu(key)
	mu.Lock()
	defer mu.Unlock()

	rc, cur, err := s.GetWithETag(ctx, key)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return "", storage.ErrPreconditionFailed
		}
		return "", err
	}
	_ = rc.Close()
	if cur != expect {
		return "", storage.ErrPreconditionFailed
	}
	return s.writeAtomic(ctx, key, r)
}

// ConditionalWritesSupported is always true for the local filesystem:
// the keyed mutex plus atomic rename enforce preconditions
// deterministically within the process.
func (s *ArtifactStore) ConditionalWritesSupported(context.Context) (bool, error) {
	return true, nil
}

func (s *ArtifactStore) writeAtomic(ctx context.Context, key string, r io.Reader) (storage.ETag, error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	if err := s.Put(ctx, key, bytes.NewReader(body)); err != nil {
		return "", err
	}
	return contentETag(body), nil
}

func contentETag(body []byte) storage.ETag {
	sum := sha256.Sum256(body)
	return storage.ETag(hex.EncodeToString(sum[:]))
}
