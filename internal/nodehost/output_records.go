package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// JobUpload carries server-derived job authority and its destination record key.
// The backend rechecks live authority at upload and record commit; this value
// cannot grant access merely by naming an attempt or digest.
type JobUpload struct {
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
	Bind    func(context.Context, NodeInput) (JobUpload, error)
}

// PublishOutput returns a record only after both immutable upload and record
// commit succeed. The terminal node transaction still authorizes its attachment.
func (o OutputRecords) PublishOutput(ctx context.Context, input NodeInput, value json.RawMessage) (*blob.RecordKey, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if input.Storage.Bytes != nil || input.Storage.Records != nil {
		if input.Storage.Bytes == nil || input.Storage.Records == nil {
			return nil, errors.New("output storage binding is incomplete")
		}
		o.Blobs, o.Records = input.Storage.Bytes, input.Storage.Records
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
	if input.Storage.Authority.CapabilityRef != "" && input.Storage.Authority != upload.Authority {
		return nil, errors.New("output authority differs from the current storage binding")
	}
	if err := upload.check(input, blob.KindOutput); err != nil {
		return nil, err
	}
	record, err := (blob.OutputData{Bytes: o.Blobs, Records: o.Records, Authority: upload.Authority, Identity: upload.Identity, Pipeline: upload.Key.Scope.Pipeline}).Put(ctx, value)
	if err != nil {
		return nil, err
	}
	return &record.Key, nil
}

func (u JobUpload) check(input NodeInput, kind blob.Kind) error {
	var spec runspec.Spec
	if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil || input.Accepted.SpecHash == "" || input.Accepted.PlanHash == "" || string(spec.Hash) != input.Accepted.SpecHash || string(spec.PlanHash) != input.Accepted.PlanHash {
		return executorError(apierr.SpecDrift, "job upload requires an accepted run spec")
	}
	attempt := runspec.AttemptIdentity{Team: spec.Request.Request.Team, RunID: input.Accepted.RunID, NodeID: input.NodeID, Generation: input.Generation, Attempt: input.Attempt}
	if attempt.Team == "" || attempt.RunID == "" || attempt.NodeID == "" || attempt.Generation < 1 || attempt.Attempt < 1 || input.Authority.Holder == "" || input.Authority.CapabilityPrefix == "" {
		return errors.New("job upload requires an awarded job attempt")
	}
	if u.Identity.Kind != blob.UploadJob || u.Identity.Unit.Set || !u.Identity.Attempt.Set || u.Identity.Attempt.Value != attempt || u.Identity.Team != attempt.Team || u.Identity.RunID != attempt.RunID {
		return errors.New("job upload differs from its awarded attempt")
	}
	if !u.Authority.Attempt.Set || u.Authority.Attempt.Value != attempt || u.Authority.CapabilityRef == "" || u.Authority.CapabilityRef != u.Identity.CapabilityRef || u.Authority.Principal == "" || u.Authority.Principal != u.Identity.Principal {
		return errors.New("job upload differs from its authenticated authority")
	}
	if u.Key.Kind != kind || u.Key.Team != attempt.Team || u.Key.Scope.Job != attempt.NodeID || u.Key.Scope.Pipeline != spec.Request.Request.Pipeline || u.Key.Scope.Repo == "" || u.Key.Scope.Repo != u.Authority.Repo || u.Key.Scope.Ref != u.Authority.Ref.Scope || u.Key.Name == "" {
		return errors.New("job record differs from its authenticated scope")
	}
	if spec.Request.Request.Git.Repository.Key != "" && u.Key.Scope.Repo != spec.Request.Request.Git.Repository.Key {
		return errors.New("job record differs from its sealed repository")
	}
	if (kind == blob.KindOutput || kind == blob.KindManifest) && u.Key != blob.ResultRecordKey(attempt, u.Authority.Repo, u.Authority.Ref.Scope, u.Key.Scope.Pipeline, kind) {
		return errors.New("job result record differs from its exact attempt layout")
	}
	return nil
}

func (u JobUpload) put(ctx context.Context, backend blob.Store, digest blob.Digest, size int64, body io.Reader) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	receipt, err := backend.Put(ctx, blob.PutRequest{Identity: u.Identity, Authority: u.Authority, Digest: digest, Size: size, Body: body})
	if err != nil {
		return err
	}
	if receipt.Identity != u.Identity || receipt.Digest != digest {
		return errors.New("job upload receipt differs from its upload")
	}
	return context.Cause(ctx)
}

func (u JobUpload) commit(ctx context.Context, records blob.RecordStore, digest blob.Digest, members []blob.Member) (*blob.RecordKey, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	record, err := records.CommitRecord(ctx, blob.CommitRecordRequest{Source: u.Identity, Authority: u.Authority, Key: u.Key, Digest: digest, Members: members})
	if err != nil {
		return nil, err
	}
	if record.Key != u.Key || record.Digest != digest || record.Source != u.Identity {
		return nil, errors.New("committed job record differs from its upload")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	return &record.Key, nil
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
	return readOutput(ctx, reader, err)
}

func readOutput(ctx context.Context, reader blob.VerifiedReader, readErr error) (value json.RawMessage, err error) {
	err = readErr
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
