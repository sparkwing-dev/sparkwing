package fs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

// safety: SafeArtifactKey rejects a segment starting with a dot, so no
// stored object can land in here and List can skip the whole directory.
const casLockDir = ".cas-locks"

var errLockUnsupported = errors.New("storage: filesystem does not support locking")

const (
	casLockRetryMin = 250 * time.Microsecond
	casLockRetryMax = 5 * time.Millisecond
)

// safety: the lock lives in its own file that no write ever replaces.
// Locking the object itself would not serialize anything, because the
// atomic rename that publishes a write swaps the inode out from under
// every holder of the old one.
func (s *ArtifactStore) openLockFile(key string) (*os.File, error) {
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
	return os.OpenFile(filepath.Join(dir, hex.EncodeToString(sum[:])), os.O_CREATE|os.O_RDWR, 0o600)
}

// safety: the lock is taken without blocking and retried, because a
// blocking lock cannot be cancelled: one wedged holder would keep every
// other caller waiting long past the deadline its own context carries.
func (s *ArtifactStore) lockKey(ctx context.Context, key string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := s.openLockFile(key)
	if err != nil {
		return nil, err
	}
	for wait := casLockRetryMin; ; {
		locked, err := tryLockExclusive(f)
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		if locked {
			return f, nil
		}
		// #nosec G404 -- lock retry jitter, not a security decision
		timer := time.NewTimer(wait/2 + time.Duration(rand.Int64N(int64(wait/2)+1)))
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = f.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
		if wait < casLockRetryMax {
			wait = min(wait*2, casLockRetryMax)
		}
	}
}

func releaseKey(f *os.File) {
	_ = unlockFile(f)
	_ = f.Close()
}

// PutIfAbsent stores a new object and refuses an existing key.
func (s *ArtifactStore) PutIfAbsent(ctx context.Context, key string, r io.Reader) error {
	lock, err := s.lockKey(ctx, key)
	if err != nil {
		return err
	}
	defer releaseKey(lock)
	has, err := s.Has(ctx, key)
	if err != nil {
		return err
	}
	if has {
		return storage.ErrPreconditionFailed
	}
	return s.Put(ctx, key, r)
}
