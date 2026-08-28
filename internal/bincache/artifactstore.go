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
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

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

func HasInArtifactStore(ctx context.Context, store storage.ArtifactStore, key string) (bool, error) {
	return store.Has(ctx, "bin/"+key)
}

func IsNotFound(err error) bool { return errors.Is(err, storage.ErrNotFound) }
