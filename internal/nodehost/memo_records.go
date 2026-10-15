package nodehost

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

// MemoRecords verifies reuse candidates without granting execution or recording a verdict.
type MemoRecords struct {
	Budget blob.Budget
	Warn   func(string)
}

// Lookup verifies local candidates and explicit remote-cache hits before returning their references.
func (m MemoRecords) Lookup(ctx context.Context, storage StorageBinding, key blob.MemoKey, job nodeproto.JobSpec) (blob.MemoHit, error) {
	if err := checkMemoLookup(storage, blob.MemoLookup{Authority: storage.Authority, Key: key}, job); err != nil {
		return blob.MemoHit{}, err
	}
	parent := ctx
	ctx, cancel := memoContext(ctx, m.Budget)
	defer cancel()
	if err := context.Cause(ctx); err != nil {
		return m.miss(parent, err)
	}
	hit, err := storage.Records.LookupMemo(ctx, blob.MemoLookup{Authority: storage.Authority, Key: key, Budget: m.Budget})
	if err != nil {
		return m.miss(parent, err)
	}
	get := func(ctx context.Context, digest blob.Digest) (blob.VerifiedReader, error) {
		return storage.Bytes.Get(ctx, blob.GetRequest{Authority: storage.Authority, Digest: digest})
	}
	remote := !hit.Found && storage.RemoteCache != nil
	if remote {
		hit, err = storage.RemoteCache.Memo(ctx, key, m.Budget)
		get = storage.RemoteCache.Artifact
	}
	if err != nil || !hit.Found {
		return m.miss(parent, err)
	}
	if err := verifyMemo(ctx, storage, hit, job, m.Budget, remote, get); err != nil {
		return m.miss(parent, err)
	}
	return hit, nil
}

// Copy verifies the atomic record copy's result before a caller can propose materialized success.
func (m MemoRecords) Copy(ctx context.Context, storage StorageBinding, request blob.CopyMemoRequest, job nodeproto.JobSpec) (blob.MemoHit, error) {
	if err := checkMemoLookup(storage, request.Lookup, job); err != nil {
		return blob.MemoHit{}, err
	}
	if !storage.Authority.Attempt.Set || request.Destination != storage.Authority.Attempt.Value || request.Destination.Team == "" || request.Destination.RunID == "" || request.Destination.NodeID != job.ID || request.Destination.Attempt < 1 || request.Destination.Generation < 1 {
		return blob.MemoHit{}, errors.New("memo copy differs from its current destination attempt")
	}
	parent := ctx
	ctx, cancel := memoContext(ctx, request.Lookup.Budget)
	defer cancel()
	if err := context.Cause(ctx); err != nil {
		return m.miss(parent, err)
	}
	hit, err := storage.Records.CopyMemo(ctx, request)
	if err != nil || !hit.Found {
		return m.miss(parent, err)
	}
	get := func(ctx context.Context, digest blob.Digest) (blob.VerifiedReader, error) {
		return storage.Bytes.Get(ctx, blob.GetRequest{Authority: storage.Authority, Digest: digest})
	}
	if err := verifyMemo(ctx, storage, hit, job, request.Lookup.Budget, false, get); err != nil {
		return m.miss(parent, err)
	}
	return hit, nil
}

func checkMemoLookup(storage StorageBinding, request blob.MemoLookup, job nodeproto.JobSpec) error {
	key := request.Key
	if storage.Bytes == nil || storage.Records == nil || storage.Authority.CapabilityRef == "" || storage.Authority.Principal == "" {
		return errors.New("memo reads require bound bytes and record adapters")
	}
	if request.Authority != storage.Authority || key.Repo == "" || key.Repo != storage.Authority.Repo || key.Ref.Ref == "" || key.Ref != storage.Authority.Ref.Scope || key.Pipeline == "" || key.Job == "" || key.Job != job.ID || key.Hash == "" || job.Memo == nil {
		return errors.New("memo lookup differs from its declared job or authenticated scope")
	}
	return nil
}

func verifyMemo(ctx context.Context, storage StorageBinding, hit blob.MemoHit, job nodeproto.JobSpec, budget blob.Budget, remote bool, get func(context.Context, blob.Digest) (blob.VerifiedReader, error)) error {
	digests, err := memoDigests(storage, hit, job, remote)
	if err != nil {
		return err
	}
	if !remote && len(digests) > 0 {
		presence, err := storage.Bytes.HasMany(ctx, blob.HasManyRequest{Authority: storage.Authority, Digests: digests, Budget: budget})
		if err != nil {
			return err
		}
		present := make(map[blob.Digest]bool, len(presence.Present))
		for _, digest := range presence.Present {
			present[digest] = true
		}
		for _, digest := range digests {
			if !present[digest] {
				return blob.ErrNotFound
			}
		}
	}
	if hit.Output.Set {
		reader, err := get(ctx, hit.Output.Value)
		if _, err := readOutput(ctx, reader, err); err != nil {
			return err
		}
	}
	if hit.Manifest.Set {
		if _, err := copyVerified(ctx, get, hit.Manifest.Value, io.Discard, -1); err != nil {
			return err
		}
	}
	for _, member := range hit.Members {
		size, err := copyVerified(ctx, get, member.Digest, io.Discard, member.Size)
		if err != nil || size != member.Size {
			return errors.Join(err, blob.ErrIntegrity)
		}
	}
	return context.Cause(ctx)
}

func memoDigests(storage StorageBinding, hit blob.MemoHit, job nodeproto.JobSpec, remote bool) ([]blob.Digest, error) {
	if !remote && storage.Authority.Attempt.Set && hit.ReadSet.Reader != storage.Authority.Attempt.Value {
		return nil, errors.New("memo read set differs from its current reader")
	}
	if job.ResultStep != "" && !hit.Output.Set || len(job.Artifacts) > 0 && !hit.Manifest.Set || !hit.Manifest.Set && len(hit.Members) > 0 {
		return nil, errors.New("memo hit omits declared result references")
	}
	if hit.Output.Set && !artifactDigestValid(hit.Output.Value) || hit.Manifest.Set && !artifactDigestValid(hit.Manifest.Value) {
		return nil, blob.ErrIntegrity
	}
	team := ""
	if !remote && storage.Authority.Attempt.Set {
		team = storage.Authority.Attempt.Value.Team
	} else if !remote && storage.Authority.Upload.Set {
		team = storage.Authority.Upload.Value.Team
	} else if len(hit.Members) > 0 {
		team = hit.Members[0].Team
	}
	if len(hit.Members) > 0 && team == "" {
		return nil, errors.New("memo manifest has no bound team")
	}
	if err := checkArtifactMembers(blob.RecordLookupResult{Record: blob.Record{Key: blob.RecordKey{Team: team}, Digest: hit.Manifest.Value}, Members: hit.Members}); err != nil {
		return nil, err
	}
	var digests []blob.Digest
	seen := make(map[blob.Digest]bool)
	add := func(digest blob.Digest) {
		if !seen[digest] {
			digests = append(digests, digest)
			seen[digest] = true
		}
	}
	if hit.Output.Set {
		add(hit.Output.Value)
	}
	if hit.Manifest.Set {
		add(hit.Manifest.Value)
	}
	for _, member := range hit.Members {
		add(member.Digest)
	}
	return digests, nil
}

func memoContext(ctx context.Context, budget blob.Budget) (context.Context, context.CancelFunc) {
	if !budget.Deadline.IsZero() {
		return context.WithDeadline(ctx, budget.Deadline)
	}
	return context.WithCancel(ctx)
}

func (m MemoRecords) miss(ctx context.Context, err error) (blob.MemoHit, error) {
	if cause := context.Cause(ctx); cause != nil {
		return blob.MemoHit{}, cause
	}
	if err != nil {
		if m.Warn != nil {
			m.Warn("memo materialization failed; executing the job")
		} else {
			slog.Warn("memo materialization failed; executing the job")
		}
	}
	return blob.MemoHit{}, nil
}
