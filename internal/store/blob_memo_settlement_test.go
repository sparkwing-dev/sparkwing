package store

import (
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestMemoReuseRequiresCommittedProducerSuccess(t *testing.T) {
	for _, outcome := range []string{"success", "rollback", "refused", "failed", "cancelled", "retired"} {
		t.Run(outcome, func(t *testing.T) {
			s, records, change := blobResultFixture(t)
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
			recordsTemplate := &blobRecords{manifests: records.manifests}
			s.resultRecords, s.materializations = recordsTemplate, recordsTemplate
			_, tenant, request := materializationTarget(t, s, records.tenant, change.Ref)
			request.Policy, request.Source, request.MaterializeOnly = runstate.SourceMemo, &runstate.MaterializeSource{Kind: runstate.SourceMemo, Record: memo.Key}, true
			assertMaterialization := func(want runstate.ReconcileKind) {
				t.Helper()
				got, err := tenant.ReconcileNode(t.Context(), request)
				if err != nil || got.Kind != want {
					t.Fatalf("materialization=%+v error=%v, want %s", got, err, want)
				}
			}
			assertMaterialization(runstate.Dispatch)
			switch outcome {
			case "success":
				if _, err := records.tenant.TransitionNode(t.Context(), change); err != nil {
					t.Fatal(err)
				}
				rewriteSchemaExec(t, s.db, "DELETE FROM runs WHERE team='a' AND id='run'")
				assertMaterialization(runstate.Materialize)
			case "rollback":
				rollback := errors.New("caller rollback")
				err := s.inBlobTx(t.Context(), func(tx *storeTx) error {
					if _, err := transitionNodeTx(t.Context(), tx, "a", change); err != nil {
						return err
					}
					return rollback
				})
				if !errors.Is(err, rollback) {
					t.Fatal(err)
				}
				assertBlobResultUnchanged(t, records, change)
				assertMaterialization(runstate.Dispatch)
			case "refused":
				change.Actor.NodeFence.Generation++
				if _, err := records.tenant.TransitionNode(t.Context(), change); err == nil {
					t.Fatal("stale producer report accepted")
				}
				assertMaterialization(runstate.Dispatch)
			case "failed", "cancelled":
				change.To, change.Extra.Result = runstate.NodeState(outcome), &runstate.NodeResult{Outcome: runstate.NodeState(outcome)}
				if _, err := records.tenant.TransitionNode(t.Context(), change); err != nil {
					t.Fatal(err)
				}
				assertMaterialization(runstate.Dispatch)
			case "retired":
				rewriteSchemaExec(t, s.db, "DELETE FROM runs WHERE team='a' AND id='run'")
				assertMaterialization(runstate.Dispatch)
			}
		})
	}
}
