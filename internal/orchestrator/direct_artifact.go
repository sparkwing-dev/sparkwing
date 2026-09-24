package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/directdata"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

type directArtifactStore struct {
	client *directdata.Client
	legacy storage.ArtifactStore
}

func (s directArtifactStore) Put(ctx context.Context, key string, body io.Reader) (err error) {
	if !strings.HasPrefix(key, "artifacts/blobs/") && !strings.HasPrefix(key, "artifacts/manifests/") {
		if s.legacy != nil {
			return s.legacy.Put(ctx, key, body)
		}
		return fmt.Errorf("direct artifact: invalid key %q", key)
	}
	f, err := os.CreateTemp("", "sparkwing-artifact-*")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close(), os.Remove(f.Name())) }()
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, sum), io.LimitReader(body, 500<<20+1))
	if err != nil {
		return err
	}
	if n > 500<<20 {
		return errors.New("direct artifact exceeds 500 MiB")
	}
	digest := hex.EncodeToString(sum.Sum(nil))
	if !strings.HasSuffix(key, "/"+digest) {
		return errors.New("artifact key does not match its sha256")
	}
	err = s.client.Upload(ctx, "artifact", key, f, n, digest)
	if errors.Is(err, directdata.ErrExists) {
		return nil
	}
	return err
}

func (s directArtifactStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	rc, answer, err := s.client.Download(ctx, "artifact", key)
	if errors.Is(err, directdata.ErrNotFound) && s.legacy != nil {
		return s.legacy.Get(ctx, key)
	}
	if err != nil {
		return nil, err
	}
	return &verifiedArtifact{ReadCloser: rc, sum: sha256.New(), expected: answer.SHA256, size: answer.Size}, nil
}

type verifiedArtifact struct {
	io.ReadCloser
	sum      hash.Hash
	expected string
	size     int64
	read     int64
}

func (v *verifiedArtifact) Read(p []byte) (int, error) {
	n, err := v.ReadCloser.Read(p)
	v.read += int64(n)
	if n > 0 {
		_, _ = v.sum.Write(p[:n])
	}
	if err == io.EOF && (v.read != v.size || hex.EncodeToString(v.sum.Sum(nil)) != v.expected) {
		return n, errors.New("direct artifact digest or size mismatch")
	}
	return n, err
}

func (s directArtifactStore) Has(ctx context.Context, key string) (bool, error) {
	rc, _, err := s.client.Download(ctx, "artifact", key)
	if errors.Is(err, directdata.ErrNotFound) && s.legacy != nil {
		return s.legacy.Has(ctx, key)
	}
	if errors.Is(err, directdata.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, rc.Close()
}

func (s directArtifactStore) Delete(ctx context.Context, key string) error {
	if s.legacy != nil {
		return s.legacy.Delete(ctx, key)
	}
	return errors.New("direct artifacts are immutable")
}

func (s directArtifactStore) List(ctx context.Context, prefix string) ([]string, error) {
	if s.legacy != nil {
		return s.legacy.List(ctx, prefix)
	}
	return nil, storage.ErrListNotSupported
}
