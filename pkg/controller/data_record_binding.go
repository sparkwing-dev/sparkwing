package controller

import (
	"context"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func (s *boundData) recordScope(authority blob.Authority, key blob.RecordKey, write bool) error {
	scope := s.authorized.Principal.Capability.Data
	if key.Team != s.authorized.Target.Team || key.Scope.Repo != authority.Repo {
		return dataForbidden()
	}
	if !slices.Contains(blob.Kinds, key.Kind) {
		return invalidEngineBody("unknown data record kind")
	}
	if scope.Use == capability.DataRemoteCache && (write || key.Kind == blob.KindBinary || key.Kind == blob.KindSourceBundle) {
		return dataForbidden()
	}
	if write {
		if !authority.Upload.Set || key.Scope.Ref != authority.Ref.Scope {
			return dataForbidden()
		}
		if key.Scope.Job != "" && (!authority.Attempt.Set || key.Scope.Job != authority.Attempt.Value.NodeID) {
			return dataForbidden()
		}
		identity := authority.Upload.Value
		for _, rule := range blob.UploadRules {
			if identity.Kind == rule.IdentityKind && identity.Attempt.Set == rule.RequiresAttempt && identity.Unit.Set == rule.RequiresUnit && slices.Contains(rule.RecordKinds, key.Kind) {
				return nil
			}
		}
		return dataForbidden()
	}
	if scope.Use != capability.DataRemoteCache && !slices.Contains(scope.ReadRefs, key.Scope.Ref.CapabilityScope()) {
		return dataForbidden()
	}
	return nil
}

func recordDataTarget(key blob.RecordKey, digest blob.Digest) capability.DataTarget {
	return capability.DataTarget{RecordKind: string(key.Kind), RecordScope: key.Scope.Ref.CapabilityScope(), RecordName: key.Name, Digest: string(digest)}
}

func (s *boundData) LookupRecord(ctx context.Context, request blob.RecordLookup) (blob.RecordLookupResult, error) {
	authority, err := s.live(ctx, request.Authority)
	if err != nil {
		return blob.RecordLookupResult{}, err
	}
	if err := s.recordScope(authority, request.Key, false); err != nil {
		return blob.RecordLookupResult{}, err
	}
	if err := s.authorize(ctx, capability.DataLookup, authority, recordDataTarget(request.Key, "")); err != nil {
		return blob.RecordLookupResult{}, err
	}
	request.Authority = authority
	request.VerifiedDefaultOnly = request.VerifiedDefaultOnly || s.authorized.Principal.Capability.Data.Use == capability.DataRemoteCache
	tenant, err := s.controller.dataTransactions(ctx, s.authorized.Target.Team)
	if err != nil {
		return blob.RecordLookupResult{}, err
	}
	result, err := tenant.LookupRecord(ctx, request)
	if err != nil {
		return blob.RecordLookupResult{}, err
	}
	if result.Found {
		if err := s.recordScope(authority, result.Record.Key, false); err != nil {
			return blob.RecordLookupResult{}, dataProjectionError()
		}
		if request.VerifiedDefaultOnly && !result.Record.VerifiedDefault() {
			return blob.RecordLookupResult{}, dataProjectionError()
		}
		result.VerifiedDefault = request.VerifiedDefaultOnly
	}
	return result, nil
}

func (s *boundData) CommitRecord(ctx context.Context, request blob.CommitRecordRequest) (blob.Record, error) {
	authority, err := s.live(ctx, request.Authority)
	if err != nil {
		return blob.Record{}, err
	}
	if err := s.recordScope(authority, request.Key, true); err != nil {
		return blob.Record{}, err
	}
	if request.Source != authority.Upload.Value {
		return blob.Record{}, dataForbidden()
	}
	digests := []blob.Digest{request.Digest}
	if request.ManifestDigest.Set {
		digests = append(digests, request.ManifestDigest.Value)
	}
	manifest := request.ManifestDigest.Value
	if request.Key.Kind == blob.KindManifest {
		manifest = request.Digest
	}
	for _, member := range request.Members {
		if member.Team != request.Key.Team || member.ManifestDigest != manifest || manifest == "" || member.Size < 0 {
			return blob.Record{}, dataForbidden()
		}
		digests = append(digests, member.Digest)
	}
	for _, digest := range digests {
		if !validDataDigest(digest) {
			return blob.Record{}, invalidEngineBody("record edges require SHA-256 digests")
		}
		if err := s.authorize(ctx, capability.DataRecord, authority, recordDataTarget(request.Key, digest)); err != nil {
			return blob.Record{}, err
		}
	}
	request.Authority, request.Source = authority, authority.Upload.Value
	tenant, err := s.controller.dataTransactions(ctx, s.authorized.Target.Team)
	if err != nil {
		return blob.Record{}, err
	}
	return tenant.CommitRecord(ctx, request)
}

func (s *boundData) LookupMemo(ctx context.Context, request blob.MemoLookup) (blob.MemoHit, error) {
	authority, tenant, err := s.cacheLookup(ctx, request.Authority, blob.KindMemo, request.Key.Repo, request.Key.Ref, request.Key.Job, string(request.Key.Hash))
	if err != nil {
		return blob.MemoHit{}, err
	}
	request.Authority = authority
	return tenant.LookupMemo(ctx, request)
}

func (s *boundData) LookupDepcache(ctx context.Context, request blob.DepcacheLookup) (blob.DepcacheHit, error) {
	authority, tenant, err := s.cacheLookup(ctx, request.Authority, blob.KindDepcache, request.Key.Repo, request.Key.Ref, "", request.Key.Name)
	if err != nil {
		return blob.DepcacheHit{}, err
	}
	request.Authority = authority
	return tenant.LookupDepcache(ctx, request)
}

func (s *boundData) cacheLookup(ctx context.Context, supplied blob.Authority, kind blob.Kind, repo runspec.RepoKey, ref blob.RefScope, job, name string) (blob.Authority, DataTransactions, error) {
	authority, err := s.live(ctx, supplied)
	if err != nil {
		return blob.Authority{}, nil, err
	}
	key := blob.RecordKey{Team: s.authorized.Target.Team, Kind: kind, Scope: blob.Scope{Repo: repo, Ref: ref, Job: job}, Name: name}
	if err := s.recordScope(authority, key, false); err != nil {
		return blob.Authority{}, nil, err
	}
	if err := s.authorize(ctx, capability.DataLookup, authority, recordDataTarget(key, "")); err != nil {
		return blob.Authority{}, nil, err
	}
	tenant, err := s.controller.dataTransactions(ctx, s.authorized.Target.Team)
	return authority, tenant, err
}

func (s *boundData) CopyMemo(ctx context.Context, request blob.CopyMemoRequest) (blob.MemoHit, error) {
	authority, err := s.live(ctx, request.Lookup.Authority)
	if err != nil {
		return blob.MemoHit{}, err
	}
	if !authority.Attempt.Set || !authority.Upload.Set || request.Destination != authority.Attempt.Value {
		return blob.MemoHit{}, dataForbidden()
	}
	key := blob.RecordKey{Team: s.authorized.Target.Team, Kind: blob.KindMemo, Scope: blob.Scope{Repo: request.Lookup.Key.Repo, Ref: request.Lookup.Key.Ref, Pipeline: request.Lookup.Key.Pipeline, Job: request.Lookup.Key.Job}, Name: string(request.Lookup.Key.Hash)}
	if err := s.recordScope(authority, key, false); err != nil {
		return blob.MemoHit{}, err
	}
	if err := s.authorize(ctx, capability.DataLookup, authority, recordDataTarget(key, "")); err != nil {
		return blob.MemoHit{}, err
	}
	key.Scope.Ref = authority.Ref.Scope
	if err := s.recordScope(authority, key, true); err != nil {
		return blob.MemoHit{}, err
	}
	if err := s.authorize(ctx, capability.DataRecord, authority, recordDataTarget(key, "")); err != nil {
		return blob.MemoHit{}, err
	}
	request.Lookup.Authority = authority
	tenant, err := s.controller.dataTransactions(ctx, s.authorized.Target.Team)
	if err != nil {
		return blob.MemoHit{}, err
	}
	return tenant.CopyMemo(ctx, request)
}
