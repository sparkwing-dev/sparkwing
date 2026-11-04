package blob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// OutputData binds job outputs to their exact uploader and authorized records.
type OutputData struct {
	Bytes     Store
	Records   RecordStore
	Authority Authority
	Identity  UploadIdentity
	Pipeline  string
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
