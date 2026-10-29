package depcacheblob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/depcache"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// RestoreArchiveIn restores name inside parent, opening every path through parent.
func RestoreArchiveIn(ctx context.Context, r blob.VerifiedReader, parent *os.Root, name string) (n int64, err error) {
	if r == nil {
		return 0, fmt.Errorf("dependency cache returned no reader")
	}
	if err := ctx.Err(); err != nil {
		return 0, errors.Join(err, r.Close())
	}
	f, err := os.CreateTemp("", "sparkwing-depcache-*")
	if err != nil {
		return 0, errors.Join(err, r.Close())
	}
	defer func() { err = errors.Join(err, f.Close(), os.Remove(f.Name())) }()
	n, err = io.Copy(f, io.LimitReader(r, limits.CacheArchiveBytes+1))
	err = errors.Join(err, r.Close(), ctx.Err())
	if err != nil {
		return n, err
	}
	if n > limits.CacheArchiveBytes {
		return n, fmt.Errorf("dependency archive exceeds %d bytes", limits.CacheArchiveBytes)
	}
	// safety: tar EOF can precede transport EOF; verify before publishing extracted files.
	if !r.Verified() {
		return n, blob.ErrIntegrity
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return n, err
	}
	return n, depcache.ExtractArchiveStagedRoot(ctx, f, parent, name)
}
