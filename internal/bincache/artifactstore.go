package bincache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

// DigestBackfillEnv names the variable that lets a fetch accept a blob
// whose digest sidecar is missing and write the sidecar from the bytes
// it just downloaded. Unset, a missing sidecar fails the fetch.
const DigestBackfillEnv = "SPARKWING_ARTIFACT_DIGEST_BACKFILL"

func digestKey(key string) string { return "bin/" + key + ".sha256" }

func storedDigest(ctx context.Context, store storage.ArtifactStore, key string) ([]byte, error) {
	rc, err := store.Get(ctx, digestKey(key))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, fmt.Errorf("%w: bin/%s has no stored digest: %w", ErrDigest, key, storage.ErrNotFound)
		}
		return nil, err
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, 128))
	if err != nil {
		return nil, err
	}
	want, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(want) != sha256.Size {
		return nil, fmt.Errorf("%w: bin/%s digest is malformed", ErrDigest, key)
	}
	return want, nil
}

func FetchFromArtifactStore(ctx context.Context, store storage.ArtifactStore, key, dest string) error {
	want, err := storedDigest(ctx, store, key)
	backfill := false
	if err != nil {
		if !errors.Is(err, storage.ErrNotFound) {
			return err
		}
		// safety: with no companion object nothing outside the fetched bytes attests them, so healing is opt-in.
		if os.Getenv(DigestBackfillEnv) == "" {
			slog.Default().Warn("artifact-store blob has no stored digest; refusing it",
				"hash", key, "opt_in", DigestBackfillEnv)
			return err
		}
		backfill = true
	}
	rc, err := store.Get(ctx, "bin/"+key)
	if err != nil {
		return err
	}
	defer rc.Close()

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + ".tmp"
	// safety: the blob earns its execute bit after the digest settles, never while it is still unverified.
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	sum := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, sum), rc); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	got := sum.Sum(nil)
	if backfill {
		if err := store.Put(ctx, digestKey(key), strings.NewReader(hex.EncodeToString(got))); err != nil {
			slog.Default().Warn("artifact-store digest backfill failed", "err", err, "hash", key)
		}
	} else {
		// safety: the key folds source inputs, not content, so only this digest ties the bytes to the cache entry.
		if !bytes.Equal(got, want) {
			_ = os.Remove(tmp)
			return fmt.Errorf("%w: bin/%s", ErrDigest, key)
		}
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
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
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return fmt.Errorf("hash %s: %w", src, err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := store.Put(ctx, "bin/"+key, f); err != nil {
		return fmt.Errorf("put bin/%s: %w", key, err)
	}
	digest := hex.EncodeToString(sum.Sum(nil))
	if err := store.Put(ctx, digestKey(key), strings.NewReader(digest)); err != nil {
		return fmt.Errorf("put %s: %w", digestKey(key), err)
	}
	return nil
}

func HasInArtifactStore(ctx context.Context, store storage.ArtifactStore, key string) (bool, error) {
	return store.Has(ctx, "bin/"+key)
}

func IsNotFound(err error) bool { return errors.Is(err, storage.ErrNotFound) }
