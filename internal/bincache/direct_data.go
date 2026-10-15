package bincache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/directdata"
)

// DownloadBinary fetches a verified binary through the controller's data route.
func DownloadBinary(ctx context.Context, d *directdata.Client, hash, dest string) error {
	rc, meta, err := d.Download(ctx, "binary", "bin/"+hash)
	if errors.Is(err, directdata.ErrNotFound) {
		return ErrMiss
	}
	if err != nil {
		return err
	}
	defer rc.Close()
	want, err := hex.DecodeString(meta.SHA256)
	if err != nil || len(want) != sha256.Size || meta.Size < 0 {
		return fmt.Errorf("%w: malformed signed binary digest or size", ErrDigest)
	}
	return installVerifiedBinary(dest, rc, want, meta.Size, hash)
}

// UploadBinary publishes a binary through the controller's signed data route.
func UploadBinary(ctx context.Context, d *directdata.Client, hash, src string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	sum := sha256.New()
	n, err := io.Copy(sum, f)
	if err != nil {
		return err
	}
	digest := hex.EncodeToString(sum.Sum(nil))
	key := "bin/" + hash + "/" + digest
	return d.Upload(ctx, "binary", key, f, n, digest)
}
