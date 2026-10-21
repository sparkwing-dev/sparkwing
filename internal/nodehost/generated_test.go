package nodehost

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func claimedFixtureNode(t *testing.T, f *storeHostFixture, id string) NodeInput {
	t.Helper()
	ref := controllerwire.NodeRef{RunID: runspec.RunID(f.Accepted.RunID), NodeID: runspec.NodeID(id)}
	if _, err := f.State.TransitionNode(t.Context(), store.ReadyNode{Ref: ref, Actor: f.Actor}); err != nil {
		t.Fatal(err)
	}
	awarded, err := f.Tenant.ReconcileNode(t.Context(), store.ReconcileRequest{Ref: ref, Actor: f.Actor, Policy: store.SourceNone})
	if err != nil || awarded.Kind != store.Execute {
		t.Fatalf("claim=%+v error=%v", awarded, err)
	}
	var acceptedSpec struct {
		Resolved struct{ Engine engine.Identity }
	}
	if err := json.Unmarshal(f.Accepted.Spec, &acceptedSpec); err != nil {
		t.Fatal(err)
	}
	input, err := NewNodeInput(f.Accepted, awarded.Node, engine.Resolved{Identity: acceptedSpec.Resolved.Engine}, f.Input.GrantedRunner)
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func acceptedFixtureExpansion(t *testing.T, f *storeHostFixture, source NodeInput, jobs ...nodeproto.JobSpec) store.AcceptedExpansion {
	t.Helper()
	if jobs == nil {
		jobs = []nodeproto.JobSpec{}
	}
	body, err := json.Marshal(nodeproto.PlanDoc{Jobs: jobs})
	if err != nil {
		t.Fatal(err)
	}
	row, err := f.Tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: runspec.RunID(f.Accepted.RunID), NodeID: runspec.NodeID(source.NodeID)})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := f.Host.expansionActor(t.Context(), f.Input, row)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := f.State.AcceptExpansion(t.Context(), store.ExpansionSubmission{Source: row.Ref, SourceGeneration: source.Generation, Actor: actor, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	return accepted
}

func TestAResumedRunKeepsAFailedGeneratedNode(t *testing.T) {
	doc := fanOutDoc()
	f := newStoreHostFixture(t, doc)
	source := claimedFixtureNode(t, f, "targets")
	if err := f.report(t.Context(), source, store.NodeSuccess); err != nil {
		t.Fatal(err)
	}
	child := nodeproto.JobSpec{ID: "test-a", Groups: []string{testGenerator}, Needs: []string{"targets"}, Steps: []nodeproto.StepSpec{{ID: "body"}}}
	acceptedFixtureExpansion(t, f, source, child)
	if err := f.report(t.Context(), claimedFixtureNode(t, f, child.ID), store.NodeFailed); err != nil {
		t.Fatal(err)
	}
	f.Host.Expand = func(context.Context, EvaluationInput, store.NodeRow, StorageBinding) (json.RawMessage, *store.ExpansionFailure, error) {
		t.Error("accepted expansion evaluated again")
		return nil, nil, nil
	}
	f.Host.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error { return f.report(ctx, input, store.NodeSuccess) })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	completion, err := f.Host.Run(ctx, f.Input)
	if err != nil || completion.Outcome != "failed" {
		t.Fatalf("completion=%+v error=%v", completion, err)
	}
	row, err := f.Tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: runspec.RunID(f.Accepted.RunID), NodeID: runspec.NodeID(child.ID)})
	if err != nil || row.Outcome != store.NodeFailed {
		t.Fatalf("child=%+v error=%v", row, err)
	}
	summary, err := f.Tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: runspec.RunID(f.Accepted.RunID), NodeID: "summary"})
	if err != nil || summary.Outcome != store.NodeCancelled {
		t.Fatalf("waiter=%+v error=%v", summary, err)
	}
}

func TestAResumedRunGeneratesASourcesUnrecordedNodes(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "child", true: "empty"}[empty], func(t *testing.T) {
			f := newStoreHostFixture(t, fanOutDoc())
			source := claimedFixtureNode(t, f, "targets")
			if err := f.report(t.Context(), source, store.NodeSuccess); err != nil {
				t.Fatal(err)
			}
			calls := 0
			f.Host.Expand = func(_ context.Context, input EvaluationInput, row store.NodeRow, _ StorageBinding) (json.RawMessage, *store.ExpansionFailure, error) {
				calls++
				if input.NodeID != source.NodeID || runspec.ClaimGeneration(input.Generation) != source.Generation || row.Ref.NodeID != runspec.NodeID(source.NodeID) {
					t.Error("expansion lost source fence")
				}
				jobs := []nodeproto.JobSpec{}
				if !empty {
					jobs = append(jobs, nodeproto.JobSpec{ID: "test-a", Groups: []string{testGenerator}, Needs: []string{"targets"}, Steps: []nodeproto.StepSpec{{ID: "body"}}})
				}
				body, err := json.Marshal(nodeproto.PlanDoc{Jobs: jobs})
				return body, nil, err
			}
			f.Host.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error { return f.report(ctx, input, store.NodeSuccess) })
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			completion, err := f.Host.Run(ctx, f.Input)
			if err != nil || completion.Outcome != "success" || calls != 1 {
				t.Fatalf("completion=%+v error=%v evaluations=%d", completion, err, calls)
			}
			snapshot, err := f.Tenant.ReadSnapshot(t.Context(), f.Accepted.RunID)
			if err != nil || len(snapshot.Expansions) != 1 || runspec.ClaimGeneration(snapshot.Expansions[0].SourceGeneration) != source.Generation {
				t.Fatalf("expansions=%+v error=%v", snapshot.Expansions, err)
			}
		})
	}
}

func TestAResumeKeepsACompleteBatchBesideAnEmptyOne(t *testing.T) {
	doc := fanOutDoc()
	doc.Jobs[0].Closures.Generate = append(doc.Jobs[0].Closures.Generate, "targets/generate/1")
	doc.Jobs[1].NeedsGenerated = append(doc.Jobs[1].NeedsGenerated, "targets/generate/1")
	f := newStoreHostFixture(t, doc)
	source := claimedFixtureNode(t, f, "targets")
	if err := f.report(t.Context(), source, store.NodeSuccess); err != nil {
		t.Fatal(err)
	}
	child := nodeproto.JobSpec{ID: "test-a", Groups: []string{testGenerator}, Steps: []nodeproto.StepSpec{{ID: "body"}}}
	accepted := acceptedFixtureExpansion(t, f, source, child)
	replayed := acceptedFixtureExpansion(t, f, source, child)
	if !replayed.Replayed || replayed.BodyHash != accepted.BodyHash {
		t.Fatal("accepted complete/empty groups did not replay immutably")
	}
	f.Host.Expand = func(context.Context, EvaluationInput, store.NodeRow, StorageBinding) (json.RawMessage, *store.ExpansionFailure, error) {
		t.Error("accepted generators ran again")
		return nil, nil, nil
	}
	f.Host.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error { return f.report(ctx, input, store.NodeSuccess) })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	completion, err := f.Host.Run(ctx, f.Input)
	if err != nil || completion.Outcome != "success" {
		t.Fatalf("completion=%+v error=%v", completion, err)
	}
}

func TestExpansionFailureWithoutAWaiterFailsTheRun(t *testing.T) {
	doc := fanOutDoc()
	doc.Jobs = doc.Jobs[:1]
	f := newStoreHostFixture(t, doc)
	f.Host.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error { return f.report(ctx, input, store.NodeSuccess) })
	f.Host.Expand = func(context.Context, EvaluationInput, store.NodeRow, StorageBinding) (json.RawMessage, *store.ExpansionFailure, error) {
		return nil, &store.ExpansionFailure{Code: apierr.SDKError, MaskedError: "generator failed"}, nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	completion, err := f.Host.Run(ctx, f.Input)
	if err != nil || completion.Outcome != "failed" {
		t.Fatalf("completion=%+v error=%v", completion, err)
	}
	snapshot, err := f.Tenant.ReadSnapshot(t.Context(), f.Accepted.RunID)
	if err != nil || len(snapshot.Nodes) != 1 || snapshot.Nodes[0].Expansion != store.ExpansionFailed {
		t.Fatalf("source expansion did not persist failure: %v", err)
	}
}
