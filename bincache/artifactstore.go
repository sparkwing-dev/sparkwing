package bincache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

// LOCAL-006: alternate path for the binary cache that goes through
// the pluggable storage.ArtifactStore (fs / s3 / future) instead of
// the cluster-mode sparkwing-cache HTTP service. Same hash key
// space (PipelineCacheKey output), so a binary published one way
// can be fetched the other -- the bytes don't care which transport
// dropped them off.
//
// `bin/<key>` is the convention; both helpers prepend it so callers
// pass plain hashes.

// FetchFromArtifactStore reads bin/<key> from store and atomic-
// renames into dest with mode 0o755. Returns storage.ErrNotFound
// when the store doesn't have this key, so callers can fall through
// to a local compile cleanly.
func FetchFromArtifactStore(ctx context.Context, store storage.ArtifactStore, key, dest string) error {
	rc, err := store.Get(ctx, "bin/"+key)
	if err != nil {
		return err
	}
	defer rc.Close()

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, rc); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

// UploadToArtifactStore reads src and PUTs it at bin/<key>. Used by
// `sparkwing pipeline publish`. Failures bubble up loud -- this is
// an explicit operator action, not a silent background optimization.
func UploadToArtifactStore(ctx context.Context, store storage.ArtifactStore, key, src string) error {
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer f.Close()
	if err := store.Put(ctx, "bin/"+key, f); err != nil {
		return fmt.Errorf("put bin/%s: %w", key, err)
	}
	return nil
}

// HasInArtifactStore is a thin wrapper over store.Has for the bin/
// keyspace; saves callers from re-prefixing.
func HasInArtifactStore(ctx context.Context, store storage.ArtifactStore, key string) (bool, error) {
	return store.Has(ctx, "bin/"+key)
}

// IsNotFound reports whether err is a storage.ErrNotFound, including
// wrapped forms. Convenience for the compile path where the miss
// case falls through to a local build.
func IsNotFound(err error) bool { return errors.Is(err, storage.ErrNotFound) }
