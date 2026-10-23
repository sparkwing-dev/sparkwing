package bincache

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

// binaryData binds binary records and verified bytes to a host's live authority.
// The host obtains Trust from team policy, never from a pipeline declaration.
// Local runs use the machine's binary cache without this remote binding.
type binaryData struct {
	Bytes     blob.Store
	Records   blob.RecordStore
	Authority blob.Authority
	Identity  blob.UploadIdentity
	Trust     blob.BuildTrust
	Budget    blob.Budget
}

// downloadBinary installs only the exact authorized record's EOF-verified bytes.
// A provenance or integrity refusal leaves an existing destination unchanged.
func downloadBinary(ctx context.Context, data *binaryData, hash, dest string) (err error) {
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
		return errMiss
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
	if err := mkdirCache(filepath.Dir(dest)); err != nil {
		if reader != nil {
			err = errors.Join(err, reader.Close())
		}
		return err
	}
	path, err := receiveFile(ctx, filepath.Dir(dest), reader)
	if err != nil {
		return err
	}
	defer func() {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}()
	if err := os.Chmod(path, 0o755); err != nil {
		return err
	}
	return os.Rename(path, dest)
}

// uploadBinary commits a binary record only after its exact uploader has a
// verified receipt. Provenance is assigned by RecordStore, never by this host.
func uploadBinary(ctx context.Context, data *binaryData, hash, src string) error {
	if data == nil || data.Bytes == nil || data.Records == nil {
		return errors.New("binary storage binding is required")
	}
	if hash == "" {
		return errors.New("binary build key is required")
	}
	_, err := uploadFileRecord(ctx, data.Bytes, data.Records, data.Authority, data.Identity, blob.KindBinary, hash, src)
	return err
}
