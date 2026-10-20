package nodehost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/tarsafe"
)

func (d DepcacheRecords) save(ctx context.Context, input NodeInput, cache directoryCache) (err error) {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if input.Storage.Bytes == nil || input.Storage.Records == nil {
		return errors.New("cache save requires the current storage binding")
	}
	empty, err := cache.empty()
	if err != nil || empty {
		return err
	}
	key := cache.key
	if key.Repo != input.Storage.Authority.Repo || key.Ref != input.Storage.Authority.Ref.Scope {
		return errors.New("cache save changed its repository or ref")
	}
	upload, err := d.Bind(ctx, input, key)
	if err != nil {
		return err
	}
	if upload.Authority != input.Storage.Authority || upload.Key.Scope.Key != key.Key || upload.Key.Name != key.Name {
		return errors.New("cache upload differs from its bound key")
	}
	if err := upload.check(input, blob.KindDepcache); err != nil {
		return err
	}
	file, err := os.CreateTemp("", "sparkwing-cache-*.tar.gz")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close(), os.Remove(file.Name())) }()
	hash := sha256.New()
	if err := writeCacheArchive(ctx, io.MultiWriter(file, hash), cache); err != nil {
		return err
	}
	size, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	digest := blob.Digest(hex.EncodeToString(hash.Sum(nil)))
	if err := upload.put(ctx, input.Storage.Bytes, digest, size, file); err != nil {
		return err
	}
	upload.Key.Scope.Pipeline, upload.Key.Scope.Job = "", ""
	_, err = upload.commit(ctx, input.Storage.Records, digest, nil)
	return err
}

func writeCacheArchive(ctx context.Context, dst io.Writer, cache directoryCache) error {
	parent, name, err := cache.open()
	if err != nil {
		return err
	}
	defer parent.Close()
	dir, err := parent.OpenRoot(name)
	if err != nil {
		return err
	}
	defer dir.Close()
	return tarsafe.WriteGzipRoot(ctx, dst, dir)
}
