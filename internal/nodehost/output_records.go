package nodehost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// OutputUpload carries server-derived job authority and its output record key.
// The backend rechecks live authority at upload and record commit; this value
// cannot grant access merely by naming an attempt or digest.
type OutputUpload struct {
	Identity  blob.UploadIdentity
	Authority blob.Authority
	Key       blob.RecordKey
}

// OutputRecords connects typed SDK output to verified bytes and record commits.
// Bind resolves the current claim through the placement's authenticated adapter.
// PublishOutput supplies ExecutorDependencies.PublishOutput. ReadOutput consumes
// a record key authorized for a persisted result; it never treats absence as null.
type OutputRecords struct {
	Blobs   blob.Store
	Records blob.RecordStore
	Bind    func(context.Context, NodeInput) (OutputUpload, error)
}

// PublishOutput returns a record only after both immutable upload and record
// commit succeed. The terminal node transaction still authorizes its attachment.
func (o OutputRecords) PublishOutput(ctx context.Context, input NodeInput, value json.RawMessage) (*blob.RecordKey, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if o.Blobs == nil || o.Records == nil || o.Bind == nil {
		return nil, errors.New("output publication requires bytes, records and authenticated binding")
	}
	if err := checkOutput(value); err != nil {
		return nil, err
	}
	upload, err := o.Bind(ctx, input)
	if err != nil {
		return nil, err
	}
	if err := upload.check(input); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(value)
	digest := blob.Digest(hex.EncodeToString(sum[:]))
	receipt, err := o.Blobs.Put(ctx, blob.PutRequest{Identity: upload.Identity, Authority: upload.Authority, Digest: digest, Size: int64(len(value)), Body: bytes.NewReader(value)})
	if err != nil {
		return nil, err
	}
	if receipt.Identity != upload.Identity || receipt.Digest != digest {
		return nil, errors.New("output receipt differs from its upload")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	record, err := o.Records.CommitRecord(ctx, blob.CommitRecordRequest{Source: upload.Identity, Authority: upload.Authority, Key: upload.Key, Digest: digest})
	if err != nil {
		return nil, err
	}
	if record.Key != upload.Key || record.Digest != digest || record.Source != upload.Identity {
		return nil, errors.New("committed output differs from its upload")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	return &record.Key, nil
}

func (u OutputUpload) check(input NodeInput) error {
	var spec runspec.Spec
	if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil || input.Accepted.SpecHash == "" || input.Accepted.PlanHash == "" || string(spec.Hash) != input.Accepted.SpecHash || string(spec.PlanHash) != input.Accepted.PlanHash {
		return executorError(apierr.SpecDrift, "output requires an accepted run spec")
	}
	attempt := runspec.AttemptIdentity{Team: spec.Request.Request.Team, RunID: input.Accepted.RunID, NodeID: input.NodeID, Generation: input.Generation, Attempt: input.Attempt}
	if attempt.Team == "" || attempt.RunID == "" || attempt.NodeID == "" || attempt.Generation < 1 || attempt.Attempt < 1 || input.Authority.Holder == "" || input.Authority.CapabilityPrefix == "" {
		return errors.New("output requires an awarded job attempt")
	}
	if u.Identity.Kind != blob.UploadJob || u.Identity.Unit.Set || !u.Identity.Attempt.Set || u.Identity.Attempt.Value != attempt || u.Identity.Team != attempt.Team || u.Identity.RunID != attempt.RunID {
		return errors.New("output upload differs from its awarded attempt")
	}
	if !u.Authority.Attempt.Set || u.Authority.Attempt.Value != attempt || u.Authority.CapabilityRef == "" || u.Authority.CapabilityRef != u.Identity.CapabilityRef || u.Authority.Principal == "" || u.Authority.Principal != u.Identity.Principal {
		return errors.New("output upload differs from its authenticated authority")
	}
	if u.Key.Kind != blob.KindOutput || u.Key.Team != attempt.Team || u.Key.Scope.Job != attempt.NodeID || u.Key.Scope.Pipeline != spec.Request.Request.Pipeline || u.Key.Scope.Repo == "" || u.Key.Scope.Repo != u.Authority.Repo || u.Key.Scope.Ref != u.Authority.Ref.Scope || u.Key.Name == "" {
		return errors.New("output record differs from its authenticated scope")
	}
	return nil
}

// ReadOutput closes every reader and accepts bytes only after verified EOF.
// The caller supplies its existing read authority; looking up a key creates none.
func (o OutputRecords) ReadOutput(ctx context.Context, request blob.RecordLookup) (value json.RawMessage, err error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if o.Blobs == nil || o.Records == nil || request.Key.Kind != blob.KindOutput {
		return nil, errors.New("output read requires bytes, records and an output key")
	}
	lookup, err := o.Records.LookupRecord(ctx, request)
	if err != nil {
		return nil, err
	}
	if !lookup.Found {
		return nil, blob.ErrNotFound
	}
	if lookup.Record.Key != request.Key || lookup.Record.Digest == "" {
		return nil, errors.New("output lookup changed its requested record")
	}
	reader, err := o.Blobs.Get(ctx, blob.GetRequest{Authority: request.Authority, Digest: lookup.Record.Digest})
	if reader != nil {
		defer func() {
			err = errors.Join(err, reader.Close())
			if err != nil {
				value = nil
			}
		}()
	}
	if err != nil {
		return nil, err
	}
	if reader == nil {
		return nil, errors.New("output backend returned no reader")
	}
	value, err = io.ReadAll(io.LimitReader(reader, limits.NodeOutputBytes+1))
	if err != nil {
		return nil, err
	}
	if err := checkOutput(value); err != nil {
		return nil, err
	}
	if !reader.Verified() {
		return nil, blob.ErrIntegrity
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	return value, nil
}

func checkOutput(value json.RawMessage) error {
	if int64(len(value)) > limits.NodeOutputBytes {
		return executorError(apierr.PayloadTooLarge, "job output exceeds its limit")
	}
	if !json.Valid(value) {
		return executorError(apierr.ProtocolError, "job output is not JSON")
	}
	return nil
}
