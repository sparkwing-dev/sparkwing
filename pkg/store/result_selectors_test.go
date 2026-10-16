package store

import (
	"context"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func TestTerminalRecordSelectorsProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(8341, 72))
	for _, policy := range []SourcePolicy{SourceNone, SourceMemo, SourceManualRetry, SourceCoalesced} {
		t.Run(string(policy), func(t *testing.T) {
			for i := range 8 {
				t.Run(fmt.Sprint(i), func(t *testing.T) {
					tenant, request := materializationFixture(t, policy)
					output := blob.RecordKey{Team: "a", Kind: blob.KindOutput, Name: fmt.Sprintf("%x:/雪", random.Uint64())}
					manifest := output
					manifest.Kind, manifest.Name = blob.KindManifest, fmt.Sprintf("%x:%s", random.Uint64(), output.Name)
					want := materializedRecords{Output: &output, Manifest: &manifest}
					if i%3 == 0 {
						want.Output = nil
					}
					if i%3 == 1 {
						want.Manifest = nil
					}
					if policy == SourceNone {
						tenant, _, _ = reconcileFixture(t)
						tenant.s.resultRecords = nodeResultRecordsFunc(func(context.Context, *storeTx, Team, Actor, NodeRow, NodeResult) error { return nil })
						holder := Actor{Kind: ActorHolder, Authority: request.Actor.Authority, CapabilityPrefix: "claim", Holder: "runner", Generation: 2}
						holder.Authority.ID = "claim"
						change := NodeTransition{Ref: request.Ref, Actor: holder, To: NodeSuccess, Cause: CauseReport, SourceAttempt: sourceAttempt(3), Extra: TransitionExtra{Result: &NodeResult{Outcome: NodeSuccess, OutputRecord: want.Output, ManifestRecord: want.Manifest}}}
						if _, err := tenant.TransitionNode(t.Context(), change); err != nil {
							t.Fatal(err)
						}
					} else {
						tenant.s.materializations = materializationRecordsFunc(func(context.Context, *storeTx, Team, Actor, NodeRef, MaterializeSource) (materializedRecords, error) {
							return want, nil
						})
						got, err := tenant.ReconcileNode(t.Context(), request)
						if err != nil || got.Kind != Materialize || got.Node.Attempt != 0 {
							t.Fatalf("materialization: %+v, %v", got, err)
						}
						assertResultSelectors(t, got.Node, want)
						got, err = tenant.ReconcileNode(t.Context(), request)
						if err != nil || got.Kind != KeepTerminal {
							t.Fatalf("terminal replay: %+v, %v", got, err)
						}
						assertResultSelectors(t, got.Node, want)
					}
					rewriteSchemaExec(t, tenant.s.db, `DELETE FROM events`)
					node, err := tenant.ReadNode(t.Context(), request.Ref)
					if err != nil {
						t.Fatal(err)
					}
					assertResultSelectors(t, node, want)
					snapshot, err := tenant.ReadSnapshot(t.Context(), request.Ref.RunID)
					if err != nil || len(snapshot.Nodes) != 1 {
						t.Fatalf("snapshot: %+v, %v", snapshot, err)
					}
					assertResultSelectors(t, snapshot.Nodes[0], want)
					foreign := &Tenant{s: tenant.s, team: "b"}
					if _, err := foreign.ReadNode(t.Context(), request.Ref); err == nil {
						t.Fatal("foreign tenant read terminal selectors")
					}
				})
			}
		})
	}
}

func assertResultSelectors(t *testing.T, node NodeRow, want materializedRecords) {
	t.Helper()
	got := materializedRecords{Output: node.OutputRecord, Manifest: node.ManifestRecord}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("terminal selectors: got %+v, want %+v", got, want)
	}
}
