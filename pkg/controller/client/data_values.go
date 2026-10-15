package client

import (
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	wire "github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func dataAuthority(input blob.Authority) wire.DataAuthoritySelector {
	result := wire.DataAuthoritySelector{CapabilityRef: input.CapabilityRef, Principal: input.Principal, Repo: input.Repo}
	if input.Attempt.Set {
		result.Attempt = &input.Attempt.Value
	}
	if input.Upload.Set {
		identity := dataIdentity(input.Upload.Value)
		result.Upload = &identity
	}
	return result
}

func dataIdentity(input blob.UploadIdentity) wire.DataUploadIdentity {
	result := wire.DataUploadIdentity{Kind: input.Kind, Team: input.Team, RunID: input.RunID, Principal: input.Principal, CapabilityRef: input.CapabilityRef, Attempt: input.Attempt}
	if input.Unit.Set {
		result.Unit = runspec.Optional[wire.DataUploadUnit]{Set: true, Value: wire.DataUploadUnit(input.Unit.Value)}
	}
	return result
}

func blobIdentity(input wire.DataUploadIdentity) blob.UploadIdentity {
	result := blob.UploadIdentity{Kind: input.Kind, Team: input.Team, RunID: input.RunID, Principal: input.Principal, CapabilityRef: input.CapabilityRef, Attempt: input.Attempt}
	if input.Unit.Set {
		result.Unit = runspec.Optional[blob.UploadUnit]{Set: true, Value: blob.UploadUnit(input.Unit.Value)}
	}
	return result
}

func dataRecordKey(input blob.RecordKey) wire.DataRecordKey {
	return wire.DataRecordKey{Team: input.Team, Kind: input.Kind, Name: input.Name, Scope: wire.DataScope{Repo: input.Scope.Repo, Ref: wire.DataRefScope(input.Scope.Ref), Pipeline: input.Scope.Pipeline, Job: input.Scope.Job, Key: input.Scope.Key}}
}

func blobRecordKey(input wire.DataRecordKey) blob.RecordKey {
	return blob.RecordKey{Team: input.Team, Kind: input.Kind, Name: input.Name, Scope: blob.Scope{Repo: input.Scope.Repo, Ref: blob.RefScope(input.Scope.Ref), Pipeline: input.Scope.Pipeline, Job: input.Scope.Job, Key: input.Scope.Key}}
}

func dataMemoKey(input blob.MemoKey) wire.DataMemoKey {
	return wire.DataMemoKey{Repo: input.Repo, Ref: wire.DataRefScope(input.Ref), Pipeline: input.Pipeline, Job: input.Job, Hash: input.Hash}
}

func dataMembers(input []blob.Member) []wire.DataMember {
	if input == nil {
		return nil
	}
	result := make([]wire.DataMember, len(input))
	for i, member := range input {
		result[i] = wire.DataMember(member)
	}
	return result
}

func blobMembers(input []wire.DataMember) []blob.Member {
	if input == nil {
		return nil
	}
	result := make([]blob.Member, len(input))
	for i, member := range input {
		result[i] = blob.Member(member)
	}
	return result
}

func blobRecord(input wire.DataRecord) blob.Record {
	facts := input.RefFacts
	return blob.Record{
		Key: blobRecordKey(input.Key), Digest: input.Digest, ManifestDigest: input.ManifestDigest, Uploader: input.Uploader,
		Source: blobIdentity(input.Source), Provenance: input.Provenance, CreatedAt: input.CreatedAt, ExpiresAt: input.ExpiresAt, Pinned: input.Pinned,
		RefFacts: blob.RefFacts{Scope: blob.RefScope(facts.Scope), DefaultBranch: facts.DefaultBranch, DefaultBranchVerified: facts.DefaultBranchVerified, AppDeliveryKey: facts.AppDeliveryKey, SHA: facts.SHA, PullRequestBase: facts.PullRequestBase},
	}
}

func blobReadSet(input wire.DataReadSet) blob.ReadSet {
	result := blob.ReadSet{Reader: input.Reader}
	if input.Edges != nil {
		result.Edges = make([]blob.ReadEdge, len(input.Edges))
		for i, edge := range input.Edges {
			result.Edges[i] = blob.ReadEdge{Kind: edge.Kind, Reader: edge.Reader, Source: edge.Source, Record: blobRecordKey(edge.Record), Digest: edge.Digest}
		}
	}
	return result
}

func blobMemoHit(input wire.DataMemoHit) blob.MemoHit {
	return blob.MemoHit{Found: input.Found, Output: input.Output, Manifest: input.Manifest, Members: blobMembers(input.Members), ReadSet: blobReadSet(input.ReadSet)}
}
