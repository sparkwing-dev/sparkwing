package bincache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func uploadFileRecord(ctx context.Context, bytes blob.Store, records blob.RecordStore, authority blob.Authority, identity blob.UploadIdentity, kind blob.Kind, name, src string) (record blob.Record, err error) {
	if err := ctx.Err(); err != nil {
		return blob.Record{}, err
	}
	if err := blob.AuthorizeUpload(authority, identity, kind); err != nil {
		return blob.Record{}, err
	}
	key, err := blob.ResolveWriteKey(authority, blob.WriteScopeIntent{Kind: kind, Name: name})
	if err != nil {
		return blob.Record{}, err
	}
	f, err := os.Open(src)
	if err != nil {
		return blob.Record{}, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	sum := sha256.New()
	n, err := io.Copy(sum, f)
	if err != nil {
		return blob.Record{}, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return blob.Record{}, err
	}
	digest := blob.Digest(hex.EncodeToString(sum.Sum(nil)))
	receipt, err := bytes.Put(ctx, blob.PutRequest{Identity: identity, Authority: authority, Digest: digest, Size: n, Body: f})
	if err != nil {
		return blob.Record{}, err
	}
	if receipt.Digest != digest || receipt.Identity != identity {
		return blob.Record{}, blob.ErrIntegrity
	}
	record, err = records.CommitRecord(ctx, blob.CommitRecordRequest{Source: identity, Authority: authority, Key: key, Digest: digest})
	if err != nil {
		return blob.Record{}, err
	}
	if record.Key != key || record.Digest != digest || record.Source != identity {
		return blob.Record{}, blob.ErrIntegrity
	}
	return record, nil
}

func receiveFile(ctx context.Context, dir string, reader blob.VerifiedReader) (path string, err error) {
	if reader == nil {
		return "", blob.ErrIntegrity
	}
	var staged string
	defer func() {
		err = errors.Join(err, reader.Close())
		if err != nil && staged != "" {
			err = errors.Join(err, os.Remove(staged))
			path = ""
		}
	}()
	f, err := os.CreateTemp(dir, ".fetch-*")
	if err != nil {
		return "", err
	}
	staged = f.Name()
	_, readErr := io.Copy(f, reader)
	if err := errors.Join(readErr, f.Close()); err != nil {
		return "", err
	}
	if !reader.Verified() {
		return "", blob.ErrIntegrity
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return f.Name(), nil
}
