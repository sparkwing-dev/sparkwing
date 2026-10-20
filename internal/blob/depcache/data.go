package depcacheblob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

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
	hit, err := b.data.Records.LookupDepcache(ctx, blob.DepcacheLookup{Authority: b.data.Authority, Key: blob.DepcacheKey{Repo: b.data.Authority.Repo, Ref: b.data.Authority.Ref.Scope, Name: key, Key: key}, Budget: b.data.Budget})
	if err != nil {
		return blob.DepcacheHit{}, err
	}
	if err := ctx.Err(); err != nil {
		return blob.DepcacheHit{}, err
	}
	return hit, nil
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
	return restoreArchive(ctx, r, dir)
}

func restoreArchive(ctx context.Context, r blob.VerifiedReader, dir string) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return 0, errors.Join(err, closeReader(r))
	}
	parent, err := os.OpenRoot(filepath.Dir(dir))
	if err != nil {
		return 0, errors.Join(err, closeReader(r))
	}
	defer parent.Close()
	return RestoreArchiveIn(ctx, r, parent, filepath.Base(dir))
}

func closeReader(r blob.VerifiedReader) error {
	if r == nil {
		return nil
	}
	return r.Close()
}

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

func (b *dataBackend) Store(ctx context.Context, key, dir string) (size int64, err error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if b.data.Records == nil || b.data.Bytes == nil {
		return 0, fmt.Errorf("dependency cache requires records and bytes")
	}
	if err := blob.AuthorizeUpload(b.data.Authority, b.data.Identity, blob.KindDepcache); err != nil {
		return 0, err
	}
	recordKey, err := blob.ResolveWriteKey(b.data.Authority, blob.WriteScopeIntent{Kind: blob.KindDepcache, Name: key, Key: key})
	if err != nil {
		return 0, err
	}
	f, err := os.CreateTemp("", "sparkwing-depcache-*")
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, f.Close(), os.Remove(f.Name())) }()
	sum := sha256.New()
	if err = depcache.WriteArchive(io.MultiWriter(f, sum), dir); err != nil {
		return 0, err
	}
	size, err = f.Seek(0, io.SeekCurrent)
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
	receipt, err := b.data.Bytes.Put(ctx, blob.PutRequest{Identity: b.data.Identity, Authority: b.data.Authority, Digest: digest, Size: size, Body: f})
	if err != nil {
		return 0, err
	}
	if receipt.Digest != digest || receipt.Identity != b.data.Identity {
		return 0, blob.ErrIntegrity
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	record, err := b.data.Records.CommitRecord(ctx, blob.CommitRecordRequest{Source: b.data.Identity, Authority: b.data.Authority, Key: recordKey, Digest: digest})
	if err != nil {
		return 0, err
	}
	if record.Key != recordKey || record.Digest != digest || record.Source != b.data.Identity {
		return 0, blob.ErrIntegrity
	}
	return size, nil
}
