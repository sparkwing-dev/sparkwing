package nodehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func (h *nodeExecutor) memo(ctx context.Context, session *executorSession, input NodeInput, job nodeproto.JobSpec, spec runspec.Spec) (runspec.Hash, *runstate.NodeResult, error) {
	key, err := h.memoKey(ctx, session, input, job)
	if err != nil || key == "" {
		return "", nil, err
	}
	hash := runspec.Hash(key)
	if spec.Resolved.Settings.Options.NoMemo.Value {
		return hash, nil, nil
	}
	result, err := h.restoreMemo(ctx, input, spec.Request.Request.Pipeline, hash)
	if cause := context.Cause(ctx); cause != nil {
		return "", nil, cause
	}
	if blob.IsCacheMiss(err) {
		return hash, nil, nil
	}
	return hash, result, err
}

func (h *nodeExecutor) memoKey(ctx context.Context, session *executorSession, input NodeInput, job nodeproto.JobSpec) (string, error) {
	if job.Memo == nil && job.Closures.MemoKey == "" {
		return "", nil
	}
	if job.Memo == nil || (job.Memo.Input == nil && job.Closures.MemoKey == "") || (job.Memo.Input != nil && job.Closures.MemoKey != "") {
		return "", executorError(apierr.PlanInvalid, "memo requires exactly one input recipe or value callback")
	}
	bounded, cancel, err := h.deps.ChildContext(ctx, 30*time.Second, 0)
	if err != nil {
		return "", err
	}
	defer cancel()
	if job.Memo.Input != nil {
		var environment procenv.Env
		if memoNeedsEnvironment(*job.Memo.Input) {
			if h.deps.memoEnvironment == nil {
				return "", errors.New("memo requires the effective job environment")
			}
			environment, err = h.deps.memoEnvironment(bounded, input)
			if err != nil {
				return "", err
			}
		}
		value, err := memoInputValue(bounded, *job.Memo.Input, memoWorkspace(input), environment)
		if err != nil {
			return "", errors.Join(executorError(apierr.Internal, "memo input evaluation failed"), err)
		}
		return memoValueKey(value)
	}
	value, err := h.eval(bounded, session, input, job.Closures.MemoKey, "memo_key", nil, nil)
	if err = errors.Join(err, context.Cause(bounded)); err != nil {
		return "", err
	}
	if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return "", nil
	}
	if !json.Valid(value) || bytes.Equal(bytes.TrimSpace(value), []byte(`""`)) {
		return "", executorError(apierr.ProtocolError, "memo callback must return a JSON value or null")
	}
	return memoValueKey(json.RawMessage(value))
}

func (h *nodeExecutor) restoreMemo(ctx context.Context, input NodeInput, pipeline string, hash runspec.Hash) (*runstate.NodeResult, error) {
	storage := input.Storage
	if storage.Bytes == nil || storage.Records == nil || !storage.Authority.Attempt.Set {
		return nil, errors.New("memo requires bound attempt storage")
	}
	key := blob.MemoKey{Repo: storage.Authority.Repo, Ref: storage.Authority.Ref.Scope, Pipeline: pipeline, Job: input.NodeID, Hash: hash}
	lookup := blob.MemoLookup{Authority: storage.Authority, Key: key}
	hit, err := storage.Records.LookupMemo(ctx, lookup)
	if err != nil {
		return nil, err
	}
	get := func(ctx context.Context, digest blob.Digest) (blob.VerifiedReader, error) {
		return storage.Bytes.Get(ctx, blob.GetRequest{Authority: storage.Authority, Digest: digest})
	}
	remote := !hit.Found && storage.RemoteCache != nil
	if remote {
		hit, err = storage.RemoteCache.Memo(ctx, key, blob.Budget{})
		get = storage.RemoteCache.Artifact
	}
	if err != nil || !hit.Found {
		return nil, err
	}
	team := storage.Authority.Attempt.Value.Team
	if remote && len(hit.Members) > 0 {
		team = hit.Members[0].Team
	}
	output, manifest, err := blob.ReadMemo(ctx, get, team, hit)
	if err != nil {
		return nil, err
	}
	if len(output) > 0 && !json.Valid(output) {
		return nil, blob.ErrIntegrity
	}
	if remote {
		if err := h.importMemo(ctx, input, pipeline, hit, output, manifest); err != nil {
			return nil, err
		}
	} else {
		copied, err := storage.Records.CopyMemo(ctx, blob.CopyMemoRequest{Lookup: lookup, Destination: storage.Authority.Attempt.Value})
		if err != nil || !copied.Found {
			return nil, err
		}
		// A mutable key can select a different record between lookup and copy.
		if hit.Output != copied.Output || hit.Manifest != copied.Manifest || !slices.Equal(hit.Members, copied.Members) {
			output, _, err = blob.ReadMemo(ctx, get, storage.Authority.Attempt.Value.Team, copied)
			if err != nil {
				return nil, err
			}
			if len(output) > 0 && !json.Valid(output) {
				return nil, blob.ErrIntegrity
			}
		}
		hit = copied
	}
	result := &runstate.NodeResult{Outcome: runstate.NodeMemoized}
	if len(output) > 0 {
		outputKey := blob.ResultRecordKey(storage.Authority.Attempt.Value, key.Repo, key.Ref, pipeline, blob.KindOutput)
		result.OutputRecord = &outputKey
	}
	if hit.Manifest.Set {
		manifestKey := blob.ResultRecordKey(storage.Authority.Attempt.Value, key.Repo, key.Ref, pipeline, blob.KindManifest)
		result.ManifestRecord = &manifestKey
	}
	return result, context.Cause(ctx)
}

func (h *nodeExecutor) importMemo(ctx context.Context, input NodeInput, pipeline string, hit blob.MemoHit, output, manifest []byte) error {
	storage := input.Storage
	identity := executorMemoIdentity(storage.Authority)
	upload := JobUpload{Identity: identity, Authority: storage.Authority}
	hit.Members = append([]blob.Member(nil), hit.Members...)
	for i := range hit.Members {
		hit.Members[i].Team = identity.Team
	}
	for _, member := range hit.Members {
		reader, err := storage.RemoteCache.Artifact(ctx, member.Digest)
		if err != nil {
			if reader != nil {
				err = errors.Join(err, reader.Close())
			}
			return err
		}
		if reader == nil {
			return blob.ErrIntegrity
		}
		err = upload.put(ctx, storage.Bytes, member.Digest, member.Size, reader)
		verified := reader.Verified()
		err = errors.Join(err, reader.Close())
		if err != nil || !verified {
			return errors.Join(err, blob.ErrIntegrity)
		}
	}
	for _, part := range []struct {
		kind    blob.Kind
		body    []byte
		digest  runspec.Optional[blob.Digest]
		members []blob.Member
	}{{blob.KindOutput, output, hit.Output, nil}, {blob.KindManifest, manifest, hit.Manifest, hit.Members}} {
		if !part.digest.Set {
			continue
		}
		upload.Key = blob.ResultRecordKey(identity.Attempt.Value, storage.Authority.Repo, storage.Authority.Ref.Scope, pipeline, part.kind)
		if err := upload.put(ctx, storage.Bytes, part.digest.Value, int64(len(part.body)), bytes.NewReader(part.body)); err != nil {
			return err
		}
		if _, err := upload.commit(ctx, storage.Records, part.digest.Value, part.members); err != nil {
			return err
		}
	}
	return context.Cause(ctx)
}

func executorMemoIdentity(authority blob.Authority) blob.UploadIdentity {
	return blob.UploadIdentity{Kind: blob.UploadJob, Team: authority.Attempt.Value.Team, RunID: authority.Attempt.Value.RunID, Principal: authority.Principal, CapabilityRef: authority.CapabilityRef, Attempt: authority.Attempt}
}

func (h *nodeExecutor) saveMemo(ctx context.Context, input NodeInput, hash runspec.Hash, result runstate.NodeResult) error {
	if hash == "" || result.Outcome != runstate.NodeSuccess {
		return nil
	}
	storage := input.Storage
	if storage.Bytes == nil || storage.Records == nil || !storage.Authority.Attempt.Set {
		return errors.New("memo publication requires bound attempt storage")
	}
	var spec runspec.Spec
	if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
		return err
	}
	identity := executorMemoIdentity(storage.Authority)
	request := blob.CommitRecordRequest{Authority: storage.Authority, Source: identity}
	key, err := blob.ResolveWriteKey(storage.Authority, blob.WriteScopeIntent{Kind: blob.KindMemo, Pipeline: spec.Request.Request.Pipeline, Job: input.NodeID, Key: string(hash), Name: blob.MemoRecordName})
	if err != nil {
		return err
	}
	request.Key = key
	if result.OutputRecord == nil {
		record, err := (blob.OutputData{Bytes: storage.Bytes, Records: storage.Records, Authority: storage.Authority, Identity: identity, Pipeline: spec.Request.Request.Pipeline}).Put(ctx, nil)
		if err != nil {
			return err
		}
		request.Digest = record.Digest
	} else {
		if *result.OutputRecord != blob.ResultRecordKey(identity.Attempt.Value, storage.Authority.Repo, storage.Authority.Ref.Scope, spec.Request.Request.Pipeline, blob.KindOutput) {
			return blob.ErrDigestNotReadable
		}
		output, err := storage.Records.LookupRecord(ctx, blob.RecordLookup{Authority: storage.Authority, Key: *result.OutputRecord})
		if err != nil || !output.Found || output.Record.Key != *result.OutputRecord {
			return errors.Join(err, blob.ErrNotFound)
		}
		request.Digest = output.Record.Digest
	}
	if result.ManifestRecord != nil {
		if *result.ManifestRecord != blob.ResultRecordKey(identity.Attempt.Value, storage.Authority.Repo, storage.Authority.Ref.Scope, spec.Request.Request.Pipeline, blob.KindManifest) {
			return blob.ErrDigestNotReadable
		}
		manifest, err := storage.Records.LookupRecord(ctx, blob.RecordLookup{Authority: storage.Authority, Key: *result.ManifestRecord})
		if err != nil || !manifest.Found || manifest.Record.Key != *result.ManifestRecord {
			return errors.Join(err, blob.ErrNotFound)
		}
		request.ManifestDigest = runspec.Optional[blob.Digest]{Set: true, Value: manifest.Record.Digest}
		request.Members = manifest.Members
	}
	if ttl := input.Execution.MemoTTL.Value; ttl > 0 {
		request.ExpiresAt = runspec.Optional[time.Time]{Set: true, Value: time.Now().Add(ttl)}
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	record, err := storage.Records.CommitRecord(ctx, request)
	if err != nil {
		return err
	}
	if record.Key != request.Key || record.Digest != request.Digest || record.Source != identity || record.ManifestDigest != request.ManifestDigest {
		return blob.ErrIntegrity
	}
	return context.Cause(ctx)
}
