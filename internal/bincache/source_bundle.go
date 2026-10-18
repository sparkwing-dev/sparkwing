package bincache

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// SeedRef names the bundle ref that carries commit sha, either the snapshot or a selected code revision.
func SeedRef(sha string) string { return "refs/sparkwing-seed/" + sha }

// SourceData binds a source bundle to the host's live data authority. Reads use
// the digest sealed in the run spec; writes retain the exact uploader receipt.
type SourceData struct {
	Bytes     blob.Store
	Records   blob.RecordStore
	Authority blob.Authority
	Identity  blob.UploadIdentity
	Budget    blob.Budget
}

// UploadSourceBundle stores a snapshot as a source_bundle record after verified
// upload. The caller seals the returned digest, rather than an object-store key.
func UploadSourceBundle(ctx context.Context, data *SourceData, sha, src string) (blob.Record, error) {
	if data == nil || data.Bytes == nil || data.Records == nil {
		return blob.Record{}, errors.New("source bundle storage binding is required")
	}
	if !runspec.IsCommitID(sha) {
		return blob.Record{}, errors.New("source bundle requires a full snapshot commit")
	}
	return uploadFileRecord(ctx, data.Bytes, data.Records, data.Authority, data.Identity, blob.KindSourceBundle, sha, src)
}
