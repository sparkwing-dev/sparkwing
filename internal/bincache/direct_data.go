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

// TryBinaryPreferred uses a controller-announced direct route for cloud
// binaries. A committed local binary cannot reach this reader through that
// route. An older controller keeps the cache service path.
func TryBinaryPreferred(ctx context.Context, controllerURL, controllerToken, grant, runID, gcURL, hash, dest string) error {
	d := directdata.New(controllerURL, grant, runID, nil)
	available, err := d.Available(ctx)
	if err != nil {
		return err
	}
	if !available {
		return TryBinaryPreferSigned(ctx, controllerURL, controllerToken, grant, gcURL, hash, dest)
	}
	rc, meta, err := d.Download(ctx, "binary", "bin/"+hash)
	if errors.Is(err, directdata.ErrNotFound) {
		return ErrMiss
	}
	if err != nil {
		return err
	}
	defer rc.Close()
	want, err := hex.DecodeString(meta.SHA256)
	if err != nil || len(want) != sha256.Size {
		return fmt.Errorf("%w: malformed signed binary digest", ErrDigest)
	}
	return installVerifiedBinary(dest, io.LimitReader(rc, meta.Size+1), want, hash)
}

// UploadBinaryPreferred publishes a content-addressed object under the
// controller's live claim when it announces direct data. An older controller
// keeps the cache service's upload path.
func UploadBinaryPreferred(ctx context.Context, controllerURL, grant, runID, gcURL, hash, src string) error {
	d := directdata.New(controllerURL, grant, runID, nil)
	available, err := d.Available(ctx)
	if err != nil {
		return err
	}
	if !available && gcURL == "" {
		return nil
	}
	if !available {
		return UploadBinary(ctx, gcURL, grant, hash, src)
	}
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
	err = d.Upload(ctx, "binary", key, f, n, digest)
	return err
}
