package depcacheblob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/depcache"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// Data supplies the host's run-bound storage clients in memory. Budget is the
// host's cache lookup deadline; no credential is read from process environment.
type Data struct {
	Bytes     blob.Store
	Records   blob.RecordStore
	Authority blob.Authority
	Identity  blob.UploadIdentity
	Budget    blob.Budget
}

// New constructs the signed-data backend for a host's job binding.
func New(data Data) depcache.Backend { return &dataBackend{data: data} }

type dataBackend struct{ data Data }

func (b *dataBackend) Label() string { return "data" }

func (b *dataBackend) lookup(ctx context.Context, key string) (blob.DepcacheHit, error) {
	if b.data.Records == nil || b.data.Bytes == nil {
		return blob.DepcacheHit{}, fmt.Errorf("dependency cache requires records and bytes")
	}
	if b.data.Budget.Deadline.IsZero() {
		return blob.DepcacheHit{}, blob.ErrBudget
	}
	ctx, cancel := context.WithDeadline(ctx, b.data.Budget.Deadline)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return blob.DepcacheHit{}, err
	}
	return b.data.Records.LookupDepcache(ctx, blob.DepcacheLookup{Authority: b.data.Authority, Key: blob.DepcacheKey{Repo: b.data.Authority.Repo, Ref: b.data.Authority.Ref.Scope, Name: key, Key: key}, Budget: b.data.Budget})
}

func (b *dataBackend) Exists(ctx context.Context, key string) (bool, error) {
	hit, err := b.lookup(ctx, key)
	return hit.Found, err
}

func (b *dataBackend) Fetch(ctx context.Context, key, dir string) (int64, error) {
	hit, err := b.lookup(ctx, key)
	if err != nil {
		return 0, err
	}
	if !hit.Found {
		return 0, blob.ErrNotFound
	}
	ctx, cancel := context.WithDeadline(ctx, b.data.Budget.Deadline)
	defer cancel()
	r, err := b.data.Bytes.Get(ctx, blob.GetRequest{Authority: b.data.Authority, Digest: hit.Record.Digest})
	if err != nil {
		return 0, err
	}
	f, err := os.CreateTemp("", "sparkwing-depcache-*")
	if err != nil {
		return 0, errors.Join(err, r.Close())
	}
	defer os.Remove(f.Name())
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(r, limits.CacheArchiveBytes+1))
	err = errors.Join(err, r.Close())
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
	return n, depcache.ExtractArchiveStaged(f, dir)
}

func (b *dataBackend) Store(ctx context.Context, key, dir string) (int64, error) {
	if b.data.Records == nil || b.data.Bytes == nil {
		return 0, fmt.Errorf("dependency cache requires records and bytes")
	}
	recordKey, err := blob.ResolveWriteKey(b.data.Authority, blob.WriteScopeIntent{Kind: blob.KindDepcache, Name: key, Key: key})
	if err != nil {
		return 0, err
	}
	f, err := os.CreateTemp("", "sparkwing-depcache-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	sum := sha256.New()
	if err = depcache.WriteArchive(io.MultiWriter(f, sum), dir); err != nil {
		return 0, err
	}
	size, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	if size > limits.CacheArchiveBytes {
		return 0, fmt.Errorf("dependency archive exceeds %d bytes", limits.CacheArchiveBytes)
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	digest := blob.Digest(hex.EncodeToString(sum.Sum(nil)))
	_, err = b.data.Bytes.Put(ctx, blob.PutRequest{Identity: b.data.Identity, Authority: b.data.Authority, Digest: digest, Size: size, Body: f})
	if err != nil {
		return 0, err
	}
	_, err = b.data.Records.CommitRecord(ctx, blob.CommitRecordRequest{Source: b.data.Identity, Authority: b.data.Authority, Key: recordKey, Digest: digest})
	return size, err
}
