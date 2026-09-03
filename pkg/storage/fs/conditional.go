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

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

var _ storage.ConditionalWriter = (*ArtifactStore)(nil)

// safety: SafeArtifactKey rejects a segment starting with a dot, so no
// stored object can land in here and List can skip the whole directory.
const casLockDir = ".cas-locks"

const casProbeKey = "conditional-write-probe"

var errLockUnsupported = errors.New("storage: filesystem does not support locking")

// safety: the lock lives in its own file that no write ever replaces.
// Locking the object itself would not serialize anything, because the
// atomic rename that publishes a write swaps the inode out from under
// every holder of the old one.
func (s *ArtifactStore) lockKey(key string) (*os.File, error) {
	if err := storage.SafeArtifactKey(key); err != nil {
		return nil, err
	}
	// safety: two concurrent O_CREATE opens of one name through *os.Root
	// can leave one of them with ENOENT. The name is a hash under a
	// constant directory, so no caller input reaches this path.
	dir := filepath.Join(s.Root, casLockDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(key))
	f, err := os.OpenFile(filepath.Join(dir, hex.EncodeToString(sum[:])), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockExclusive(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func releaseKey(f *os.File) {
	_ = unlockFile(f)
	_ = f.Close()
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
	lock, err := s.lockKey(key)
	if err != nil {
		return "", err
	}
	defer releaseKey(lock)

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
	lock, err := s.lockKey(key)
	if err != nil {
		return "", err
	}
	defer releaseKey(lock)

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

// ConditionalWritesSupported reports whether this root can carry the
// lock the CAS pair holds. It is true on a filesystem that locks, which
// makes the preconditions hold between processes as well as within one,
// and false on a mount whose kernel refuses the lock, so a caller falls
// back to last-write-wins instead of trusting a reservation nothing
// enforces.
func (s *ArtifactStore) ConditionalWritesSupported(context.Context) (bool, error) {
	lock, err := s.lockKey(casProbeKey)
	if errors.Is(err, errLockUnsupported) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	releaseKey(lock)
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
