package store

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// A memo names a producer's output, which is reusable only once the producer's
// success commits. Hits then settle through the reading job's own start permit.
func TestMemoIsNotReusableWhileItsProducerRuns(t *testing.T) {
	_, records, change := blobResultFixture(t)
	authority := blob.Authority{CapabilityRef: change.Actor.CapabilityPrefix}
	output, err := records.LookupRecord(t.Context(), blob.RecordLookup{Authority: authority, Key: *change.Extra.Result.OutputRecord})
	if err != nil || !output.Found {
		t.Fatalf("producer output: %+v %v", output, err)
	}
	memo := blob.CommitRecordRequest{Authority: authority, Source: output.Record.Source, Key: output.Record.Key, Digest: output.Record.Digest}
	memo.Key.Kind, memo.Key.Name, memo.Key.Scope.Key = blob.KindMemo, "output", fixtureMemoHash
	if _, err := records.CommitRecord(t.Context(), memo); err != nil {
		t.Fatal(err)
	}
	lookup := blob.MemoLookup{Authority: authority, Key: blob.MemoKey{Repo: memo.Key.Scope.Repo, Ref: memo.Key.Scope.Ref, Pipeline: memo.Key.Scope.Pipeline, Job: memo.Key.Scope.Job, Hash: fixtureMemoHash}}
	if hit, err := records.LookupMemo(t.Context(), lookup); err != nil || hit.Found {
		t.Fatalf("running producer reusable: %+v %v", hit, err)
	}
	if hit, err := records.CopyMemo(t.Context(), blob.CopyMemoRequest{Lookup: lookup, Destination: output.Record.Source.Attempt.Value}); err != nil || hit.Found {
		t.Fatalf("running producer copied: %+v %v", hit, err)
	}
}

// Decision 42: a memo hit settles only through the executing holder's start
// permit, so the coordinator's reconciliation refuses a memo source.
func TestCoordinatorReconciliationRefusesMemoSources(t *testing.T) {
	s, records, change := blobResultFixture(t)
	_, tenant, request := materializationTarget(t, s, records.tenant, change.Ref)
	memo := blob.RecordKey{Team: "a", Kind: blob.KindMemo, Name: "output"}
	for _, source := range []*runstate.MaterializeSource{nil, {Kind: runstate.SourcePolicy("memo"), Record: memo}} {
		request.Policy, request.Source, request.MaterializeOnly = runstate.SourcePolicy("memo"), source, true
		if got, err := tenant.ReconcileNode(t.Context(), request); err == nil {
			t.Fatalf("coordinator reconciled a memo source: %+v", got)
		}
	}
}
