package store

import (
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
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
			request.Policy, request.Source, request.MaterializeOnly = SourceMemo, &MaterializeSource{Kind: SourceMemo, Record: memo.Key}, true
			assertMaterialization := func(want ReconcileKind) {
				t.Helper()
				got, err := tenant.ReconcileNode(t.Context(), request)
				if err != nil || got.Kind != want {
					t.Fatalf("materialization=%+v error=%v, want %s", got, err, want)
				}
			}
			assertMaterialization(Dispatch)
			switch outcome {
			case "success":
				if _, err := records.tenant.TransitionNode(t.Context(), change); err != nil {
					t.Fatal(err)
				}
				rewriteSchemaExec(t, s.db, "DELETE FROM runs WHERE team='a' AND id='run'")
				assertMaterialization(Materialize)
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
				assertMaterialization(Dispatch)
			case "refused":
				change.Actor.NodeFence.Generation++
				if _, err := records.tenant.TransitionNode(t.Context(), change); err == nil {
					t.Fatal("stale producer report accepted")
				}
				assertMaterialization(Dispatch)
			case "failed", "cancelled":
				change.To, change.Extra.Result = NodeState(outcome), &NodeResult{Outcome: NodeState(outcome)}
				if _, err := records.tenant.TransitionNode(t.Context(), change); err != nil {
					t.Fatal(err)
				}
				assertMaterialization(Dispatch)
			case "retired":
				rewriteSchemaExec(t, s.db, "DELETE FROM runs WHERE team='a' AND id='run'")
				assertMaterialization(Dispatch)
			}
		})
	}
}
