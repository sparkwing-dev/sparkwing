package bincache

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

// BinaryData binds binary records and verified bytes to a host's live authority.
// The host obtains Trust from team policy, never from a pipeline declaration.
// Local runs use the machine's binary cache without this remote binding.
type BinaryData struct {
	Bytes     blob.Store
	Records   blob.RecordStore
	Authority blob.Authority
	Identity  blob.UploadIdentity
	Trust     blob.BuildTrust
	Budget    blob.Budget
}

// DownloadBinary installs only the exact authorized record's EOF-verified bytes.
// A provenance or integrity refusal leaves an existing destination unchanged.
func DownloadBinary(ctx context.Context, data *BinaryData, hash, dest string) (err error) {
	defer func() {
		if errors.Is(err, context.DeadlineExceeded) {
			err = errors.Join(blob.ErrBudget, err)
		}
	}()
	if data == nil || data.Bytes == nil || data.Records == nil {
		return errors.New("binary storage binding is required")
	}
	if hash == "" || data.Trust.Team == "" || data.Authority.Repo == "" || data.Authority.Ref.Scope.Ref == "" {
		return blob.ErrDigestNotReadable
	}
	if data.Budget.Deadline.IsZero() {
		return blob.ErrBudget
	}
	ctx, cancel := context.WithDeadline(ctx, data.Budget.Deadline)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	key := blob.RecordKey{Team: data.Trust.Team, Kind: blob.KindBinary, Scope: blob.Scope{Repo: data.Authority.Repo, Ref: data.Authority.Ref.Scope}, Name: hash}
	result, err := data.Records.LookupRecord(ctx, blob.RecordLookup{Authority: data.Authority, Key: key, Budget: data.Budget})
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !result.Found {
		return ErrMiss
	}
	if result.Record.Key != key {
		return blob.ErrDigestNotReadable
	}
	if err := blob.CheckBinaryExecution(blob.BinaryExecution{Record: result.Record, Digest: result.Record.Digest, Trust: data.Trust}); err != nil {
		return err
	}
	reader, err := data.Bytes.Get(ctx, blob.GetRequest{Authority: data.Authority, Digest: result.Record.Digest})
	if err != nil {
		return err
	}
	return installBinary(ctx, dest, reader)
}

func installBinary(ctx context.Context, dest string, reader blob.VerifiedReader) (err error) {
	if reader == nil {
		return blob.ErrIntegrity
	}
	closed := false
	defer func() {
		if !closed {
			err = errors.Join(err, reader.Close())
		}
	}()
	if err := mkdirCache(filepath.Dir(dest)); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(dest), ".fetch-*")
	if err != nil {
		return err
	}
	defer func() {
		if removeErr := os.Remove(f.Name()); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}()
	_, readErr := io.Copy(f, reader)
	closeErr := f.Close()
	readerErr := reader.Close()
	closed = true
	if err := errors.Join(readErr, closeErr, readerErr); err != nil {
		return err
	}
	if !reader.Verified() {
		return blob.ErrIntegrity
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Chmod(f.Name(), 0o755); err != nil {
		return err
	}
	return os.Rename(f.Name(), dest)
}

// UploadBinary commits a binary record only after its exact uploader has a
// verified receipt. Provenance is assigned by RecordStore, never by this host.
func UploadBinary(ctx context.Context, data *BinaryData, hash, src string) error {
	if data == nil || data.Bytes == nil || data.Records == nil {
		return errors.New("binary storage binding is required")
	}
	if hash == "" {
		return errors.New("binary build key is required")
	}
	if err := blob.AuthorizeUpload(data.Authority, data.Identity, blob.KindBinary); err != nil {
		return err
	}
	key, err := blob.ResolveWriteKey(data.Authority, blob.WriteScopeIntent{Kind: blob.KindBinary, Name: hash})
	if err != nil {
		return err
	}
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	sum := sha256.New()
	n, err := io.Copy(sum, f)
	if err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	digest := blob.Digest(fmt.Sprintf("%x", sum.Sum(nil)))
	if _, err := data.Bytes.Put(ctx, blob.PutRequest{Identity: data.Identity, Authority: data.Authority, Digest: digest, Size: n, Body: f}); err != nil {
		return err
	}
	record, err := data.Records.CommitRecord(ctx, blob.CommitRecordRequest{Source: data.Identity, Authority: data.Authority, Key: key, Digest: digest})
	if err != nil {
		return err
	}
	if record.Key != key || record.Digest != digest || record.Source != data.Identity {
		return blob.ErrIntegrity
	}
	return nil
}
