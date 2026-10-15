package blob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// OutputData binds job outputs to their exact uploader and authorized records.
// Budget covers the complete record lookup and read, including EOF verification.
type OutputData struct {
	Bytes     Store
	Records   RecordStore
	Authority Authority
	Identity  UploadIdentity
	Pipeline  string
	Budget    Budget
}

// Put commits an output record after obtaining the exact uploader's receipt.
// Empty output is stored explicitly, so missing bytes cannot stand for success.
func (d OutputData) Put(ctx context.Context, body []byte) (Record, error) {
	if err := context.Cause(ctx); err != nil {
		return Record{}, err
	}
	if d.Bytes == nil || d.Records == nil {
		return Record{}, fmt.Errorf("output requires records and bytes")
	}
	if int64(len(body)) > limits.NodeOutputBytes {
		return Record{}, fmt.Errorf("output exceeds %d bytes", limits.NodeOutputBytes)
	}
	if err := AuthorizeUpload(d.Authority, d.Identity, KindOutput); err != nil {
		return Record{}, err
	}
	key, err := ResolveWriteKey(d.Authority, WriteScopeIntent{
		Kind: KindOutput, Pipeline: d.Pipeline, Job: d.Identity.Attempt.Value.NodeID, Name: d.Identity.RunID,
	})
	if err != nil {
		return Record{}, err
	}
	sum := sha256.Sum256(body)
	digest := Digest(hex.EncodeToString(sum[:]))
	receipt, err := d.Bytes.Put(ctx, PutRequest{Authority: d.Authority, Identity: d.Identity, Digest: digest, Size: int64(len(body)), Body: bytes.NewReader(body)})
	if err = errors.Join(err, context.Cause(ctx)); err != nil {
		return Record{}, err
	}
	if receipt.Digest != digest || receipt.Identity != d.Identity {
		return Record{}, ErrIntegrity
	}
	if err := context.Cause(ctx); err != nil {
		return Record{}, err
	}
	record, err := d.Records.CommitRecord(ctx, CommitRecordRequest{Authority: d.Authority, Source: d.Identity, Key: key, Digest: digest})
	if err = errors.Join(err, context.Cause(ctx)); err != nil {
		return Record{}, err
	}
	if record.Key != key || record.Digest != digest || record.Source != d.Identity {
		return Record{}, ErrIntegrity
	}
	return record, nil
}

// Read resolves an output record under the reader's authority and consumes its
// verified bytes. Missing records, missing objects and incomplete reads fail.
func (d OutputData) Read(ctx context.Context, key RecordKey) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if d.Bytes == nil || d.Records == nil {
		return nil, fmt.Errorf("output requires records and bytes")
	}
	if key.Kind != KindOutput {
		return nil, ErrDigestNotReadable
	}
	if d.Budget.Deadline.IsZero() {
		return nil, ErrBudget
	}
	ctx, cancel := context.WithDeadline(ctx, d.Budget.Deadline)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, budgetError(err)
	}
	result, err := d.Records.LookupRecord(ctx, RecordLookup{Authority: d.Authority, Key: key, Budget: d.Budget})
	if err != nil {
		return nil, budgetError(err)
	}
	if !result.Found {
		return nil, ErrNotFound
	}
	if result.Record.Key != key {
		return nil, ErrDigestNotReadable
	}
	body, err := readOutputBytes(ctx, d.Bytes, d.Authority, result.Record.Digest)
	return body, budgetError(err)
}

func readOutputBytes(ctx context.Context, store Store, authority Authority, digest Digest) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r, err := store.Get(ctx, GetRequest{Authority: authority, Digest: digest})
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, reader: r}, limits.NodeOutputBytes+1))
	err = errors.Join(err, r.Close(), ctx.Err())
	if err != nil {
		return nil, err
	}
	if !r.Verified() || int64(len(body)) > limits.NodeOutputBytes {
		return nil, ErrIntegrity
	}
	return body, nil
}
