package store_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestMemoPublicationCommitsWithProducerOutcome(t *testing.T) {
	for _, dialect := range []struct {
		name   string
		target func(*testing.T) *storetest.Target
	}{{"sqlite", storetest.NewSQLite}, {"postgres", storetest.NewPostgres}} {
		t.Run(dialect.name, func(t *testing.T) {
			for _, outcome := range []runstate.NodeState{runstate.NodeSuccess, runstate.NodeCancelled} {
				t.Run(string(outcome), func(t *testing.T) {
					f := newStoreRecoveryFixtureAt(t, true, dialect.target(t), func(doc *nodeproto.PlanDoc) {
						doc.Jobs[0].Memo = &nodeproto.MemoSpec{}
						doc.Jobs[0].Closures.MemoKey = "memo"
					})
					row, err := f.tenant.ReadNode(t.Context(), f.ref)
					if err != nil {
						t.Fatal(err)
					}
					attempt := runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{Team: string(f.tenant.Team()), RunID: string(f.ref.RunID), NodeID: string(f.ref.NodeID), Attempt: int64(row.Attempt), Generation: int64(row.Generation)}}
					authority := blob.Authority{CapabilityRef: f.holder.CapabilityPrefix, Attempt: attempt}
					identity := blob.UploadIdentity{Kind: blob.UploadJob, Team: string(f.tenant.Team()), RunID: string(f.ref.RunID), Attempt: attempt, Principal: blob.Principal(f.holder.Holder), CapabilityRef: f.holder.CapabilityPrefix}
					ticket, err := f.tenant.ReserveBlobUpload(t.Context(), store.UploadReservationRequest{ID: "memo-producer", Intent: blob.UploadIntent{Authority: authority, Identity: identity, Digest: blob.Digest(strings.Repeat("a", 64)), Size: 3}, ExpiresAt: time.Now().Add(time.Minute)})
					if err != nil {
						t.Fatal(err)
					}
					receipt, err := f.tenant.CommitBlobUpload(t.Context(), store.VerifiedUploadCommit{Authority: authority, TicketID: ticket.ID, Verified: ticket.Blob, ReplicaID: "memo"})
					if err != nil {
						t.Fatal(err)
					}
					request := f.plan.Seal.Request
					ref := blob.ScopeFor(request.Provenance)
					key := blob.RecordKey{Team: string(f.tenant.Team()), Kind: blob.KindMemo, Scope: blob.Scope{Repo: request.Request.Git.Repository.Key, Ref: ref, Pipeline: request.Request.Pipeline, Job: string(f.ref.NodeID), Key: strings.Repeat("e", 64)}, Name: blob.MemoRecordName}
					if _, err := f.tenant.CommitRecord(t.Context(), blob.CommitRecordRequest{Authority: authority, Source: identity, Key: key, Digest: receipt.Digest}); err != nil {
						t.Fatal(err)
					}
					binding, reader := admissionPolicyBlocker(t, f)
					if granted, err := f.tenant.MayStart(t.Context(), binding, reader); err != nil || granted.Kind != runstate.AcquireGranted {
						t.Fatalf("reader admission=%+v %v", granted, err)
					}
					readerRef := runspec.NodeRef{RunID: runspec.RunID(binding.RunID), NodeID: runspec.NodeID(binding.NodeID)}
					if _, err := f.tenant.TransitionNode(t.Context(), store.StartNode{Ref: readerRef, Actor: reader, SourceAttempt: acceptanceAttempt(0)}); err != nil {
						t.Fatal(err)
					}
					lookup := blob.MemoLookup{Authority: blob.Authority{CapabilityRef: reader.CapabilityPrefix}, Key: blob.MemoKey{Repo: key.Scope.Repo, Ref: ref, Pipeline: key.Scope.Pipeline, Job: key.Scope.Job, Hash: runspec.Hash(key.Scope.Key)}}
					if hit, err := f.tenant.LookupMemo(t.Context(), lookup); err != nil || hit.Found {
						t.Fatalf("unsettled producer reusable: %+v %v", hit, err)
					}
					if _, err := f.tenant.TransitionNode(t.Context(), store.ReportNode{Ref: f.ref, Actor: f.holder, SourceAttempt: &row.Attempt, Outcome: outcome, Result: &runstate.NodeResult{Outcome: outcome}}); err != nil {
						t.Fatal(err)
					}
					hit, err := f.tenant.LookupMemo(t.Context(), lookup)
					if err != nil || hit.Found != (outcome == runstate.NodeSuccess) {
						t.Fatalf("producer %s reuse=%+v %v", outcome, hit, err)
					}
				})
			}
		})
	}
}
