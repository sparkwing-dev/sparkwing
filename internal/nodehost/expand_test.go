package nodehost

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const testGenerator = "targets/generate/0"

func fanOutDoc() nodeproto.PlanDoc {
	return nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
		{ID: "targets", Closures: nodeproto.ClosureSpec{Generate: []string{testGenerator}}, Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "summary", Needs: deps("lint"), NeedsGenerated: []string{testGenerator}, Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "lint", Steps: []nodeproto.StepSpec{{ID: "body"}}},
	}}
}

func evaluateExpansionBytes(t *testing.T, groups map[string]string) (json.RawMessage, *store.ExpansionFailure) {
	t.Helper()
	input, state, dependencies, process := evaluationFixture(t)
	dependencies.ValidatePlan = nodeproto.ValidatePlan
	dependencies.Validation = nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}
	process.answer = func(_ context.Context, request nodeproto.Request) (any, error) {
		if request.Op != nodeproto.OpEval {
			return nil, nil
		}
		raw, ok := groups[request.Closure]
		if !ok {
			raw = "[]"
		}
		return nodeproto.EvalResult{Value: json.RawMessage(raw)}, nil
	}
	evaluator, err := NewOwnerEvaluator(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	body, failure, err := evaluator.Expand(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if process.closed != 1 || process.cleaned != 1 || len(state.changes) != 0 {
		t.Fatal("expansion leaked a process or wrote unaccepted topology")
	}
	return body, failure
}

func TestOwnerExpansionValidatesOriginalBytesBeforeWrites(t *testing.T) {
	valid := `{"id":"child","deps":["build"],"groups":["first"],"steps":[{"id":"body"}]}`
	for name, raw := range map[string]string{
		"unknown":   strings.Replace(valid, `"id":"child"`, `"id":"child","ignored":true`, 1),
		"duplicate": strings.Replace(valid, `"id":"child"`, `"id":"discarded","id":"child"`, 1),
		"null":      strings.Replace(valid, `"deps":["build"]`, `"deps":["build"],"env":null`, 1),
		"cycle":     strings.Replace(valid, `["build"]`, `["build","child"]`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			body, failure := evaluateExpansionBytes(t, map[string]string{"first": "[" + raw + "]"})
			if len(body) != 0 || failure == nil || failure.Code != apierr.PlanInvalid {
				t.Fatalf("body=%s failure=%+v", body, failure)
			}
		})
	}
}

func TestOwnerExpansionValidatesSourceBatchesTogether(t *testing.T) {
	first := `{"id":"one","deps":["build","two"],"groups":["first"],"steps":[{"id":"body"}]}`
	second := `{"id":"two","deps":["build"],"groups":["last"],"steps":[{"id":"body"}]}`
	for _, mode := range []string{"forward_dependency", "cycle", "recovery", "implicit_source"} {
		t.Run(mode, func(t *testing.T) {
			one, two := first, second
			switch mode {
			case "cycle":
				two = strings.Replace(two, `["build"]`, `["build","one"]`, 1)
			case "recovery":
				one = strings.Replace(one, `"deps":["build","two"]`, `"on_failure_of":"two"`, 1)
			case "implicit_source":
				one = strings.Replace(one, `"deps":["build","two"],`, ``, 1)
			}
			body, failure := evaluateExpansionBytes(t, map[string]string{"first": "[" + one + "]", "last": "[" + two + "]"})
			if mode == "cycle" {
				if len(body) != 0 || failure == nil || failure.Code != apierr.PlanInvalid {
					t.Fatalf("cyclic body=%s failure=%+v", body, failure)
				}
			} else {
				var plan nodeproto.PlanDoc
				if failure != nil || json.Unmarshal(body, &plan) != nil || len(plan.Jobs) != 2 {
					t.Fatalf("body=%s failure=%+v", body, failure)
				}
			}
		})
	}
}

func TestOwnerExpansionFanOutBoundary(t *testing.T) {
	for _, count := range []int{256, 257} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			jobs := make([]nodeproto.JobSpec, count)
			for i := range jobs {
				jobs[i] = nodeproto.JobSpec{ID: fmt.Sprintf("child-%d", i), Needs: []string{"build"}, Groups: []string{"first"}, Steps: []nodeproto.StepSpec{{ID: "body"}}}
			}
			raw, err := json.Marshal(jobs)
			if err != nil {
				t.Fatal(err)
			}
			body, failure := evaluateExpansionBytes(t, map[string]string{"first": string(raw)})
			if count == 256 {
				var plan nodeproto.PlanDoc
				if failure != nil || json.Unmarshal(body, &plan) != nil || len(plan.Jobs) != count {
					t.Fatalf("count=%d body=%s failure=%+v", count, body, failure)
				}
			} else if len(body) != 0 || failure == nil {
				t.Fatalf("count=%d body=%s failure=%+v", count, body, failure)
			}
		})
	}
}

type expansionWrites struct {
	storage.StateStoreOld
	nodes []store.Node
}

func (s *expansionWrites) CreateNode(_ context.Context, node store.Node) error {
	s.nodes = append(s.nodes, node)
	return nil
}
func (*expansionWrites) UpdateNodeDeps(context.Context, string, string, []string) error { return nil }
func (*expansionWrites) AppendEvent(context.Context, string, string, string, []byte) error {
	return nil
}

func (s *expansionWrites) ListNodes(context.Context, string) ([]*store.Node, error) {
	nodes := make([]*store.Node, 0, len(s.nodes))
	for i := range s.nodes {
		nodes = append(nodes, &s.nodes[i])
	}
	return nodes, nil
}

func FuzzGeneratorReplayPreservesEveryAcceptedDeclaration(f *testing.F) {
	f.Add("sibling", false, false, false)
	f.Add("changed", false, false, false)
	f.Add("sibling", true, false, false)
	f.Add("sibling", false, true, false)
	f.Add("sibling", false, false, true)
	f.Fuzz(func(t *testing.T, siblingID string, optional, add, partial bool) {
		source := nodeproto.JobSpec{ID: "targets", Steps: []nodeproto.StepSpec{{ID: "body"}}, Closures: nodeproto.ClosureSpec{Generate: []string{testGenerator}}}
		jobs := []nodeproto.JobSpec{
			{ID: "leaf", Groups: []string{testGenerator}, Needs: []string{"targets"}, Steps: []nodeproto.StepSpec{{ID: "body"}}},
			{ID: "sibling", Groups: []string{testGenerator}, Needs: []string{"targets"}, Steps: []nodeproto.StepSpec{{ID: "body"}}},
		}
		state := &executorStore{nodes: map[string]store.NodeRow{}}
		snapshot := store.RunSnapshot{Run: store.RunRow{ID: "run"}}
		for _, job := range append([]nodeproto.JobSpec{source}, jobs...) {
			raw, err := json.Marshal(job)
			if err != nil {
				t.Fatal(err)
			}
			hash, err := executorHash(job)
			if err != nil {
				t.Fatal(err)
			}
			row := store.NodeRow{Ref: store.NodeRef{RunID: "run", NodeID: job.ID}, Spec: raw, SpecHash: string(hash)}
			state.nodes[job.ID] = row
			snapshot.Nodes = append(snapshot.Nodes, row)
		}
		body, err := json.Marshal(nodeproto.PlanDoc{Jobs: jobs})
		if err != nil {
			t.Fatal(err)
		}
		hash, err := executorHash(nodeproto.PlanDoc{Jobs: jobs})
		if err != nil {
			t.Fatal(err)
		}
		snapshot.Expansions = []store.AcceptedExpansionRow{{Source: store.NodeRef{RunID: "run", NodeID: "targets"}, SourceGeneration: 1, Body: body, BodyHash: string(hash)}}
		if partial {
			delete(state.nodes, "sibling")
		}
		stored := len(state.nodes)
		jobs[1].ID, jobs[1].Optional = siblingID, optional
		if add {
			jobs = append(jobs, nodeproto.JobSpec{ID: "extra", Groups: []string{testGenerator}, Needs: []string{"targets"}, Steps: []nodeproto.StepSpec{{ID: "body"}}})
		}
		process := &executorProcess{answer: func(context.Context, nodeproto.Request) (any, error) {
			raw, err := json.Marshal(jobs)
			return nodeproto.EvalResult{Value: raw}, err
		}}
		code := sdkCode{Store: state, CanonicalHash: executorHash, ValidatePlan: nodeproto.ValidatePlan, Validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}}
		replayed, err := code.replaySource(t.Context(), &executorSession{process: process}, NodeInput{Accepted: AcceptedRun{RunID: "run"}}, source, GeneratorReplay{SourceJob: "targets", SourceGeneration: 1, GeneratorID: testGenerator, Output: json.RawMessage("null")}, snapshot, []nodeproto.JobSpec{source})
		unchanged := siblingID == "sibling" && !optional && !add && !partial
		if unchanged != (err == nil) || !unchanged && replayed != nil || len(state.nodes) != stored || len(state.changes) != 0 {
			t.Fatal("generator replay changed accepted membership or a sibling declaration")
		}
	})
}

func TestExpansionFansOutAndHoldsItsWaiter(t *testing.T) {
	fixture := newStoreHostFixture(t, fanOutDoc())
	children := make([]nodeproto.JobSpec, 5)
	for i := range children {
		children[i] = nodeproto.JobSpec{ID: fmt.Sprintf("test-%d", i), Needs: []string{"targets"}, Groups: []string{testGenerator}, Steps: []nodeproto.StepSpec{{ID: "body"}}}
	}
	fixture.Host.Expand = func(context.Context, EvaluationInput, store.NodeRow, StorageBinding) (json.RawMessage, *store.ExpansionFailure, error) {
		raw, err := json.Marshal(nodeproto.PlanDoc{Jobs: children})
		return raw, nil, err
	}
	fixture.Host.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
		if input.NodeID == "summary" {
			for _, child := range children {
				row, err := fixture.Tenant.ReadNode(ctx, store.NodeRef{RunID: fixture.Accepted.RunID, NodeID: child.ID})
				if err != nil || row.Outcome != store.NodeSuccess {
					return fmt.Errorf("waiter ran before %s succeeded: %v", child.ID, err)
				}
			}
		}
		return fixture.report(ctx, input, store.NodeSuccess)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	completion, err := fixture.Host.Run(ctx, fixture.Input)
	if err != nil || completion.Outcome != "success" {
		t.Fatalf("completion=%+v error=%v", completion, err)
	}
}

func TestExpansionFailureCancelsItsWaiter(t *testing.T) {
	for _, mode := range []string{"generator-error", "source-failed", "source-skipped"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newStoreHostFixture(t, fanOutDoc())
			fixture.Host.Expand = func(context.Context, EvaluationInput, store.NodeRow, StorageBinding) (json.RawMessage, *store.ExpansionFailure, error) {
				if mode != "generator-error" {
					t.Error("unsuccessful source evaluated a generator")
				}
				return nil, &store.ExpansionFailure{Code: apierr.SDKError, MaskedError: "generator panicked"}, nil
			}
			fixture.Host.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
				if input.NodeID == "summary" {
					return fmt.Errorf("failed expansion released its waiter")
				}
				outcome := store.NodeSuccess
				if input.NodeID == "targets" {
					if mode == "source-failed" {
						outcome = store.NodeFailed
					}
					if mode == "source-skipped" {
						outcome = store.NodeSkipped
					}
				}
				return fixture.report(ctx, input, outcome)
			})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			completion, err := fixture.Host.Run(ctx, fixture.Input)
			wantRun, wantWaiter := "failed", store.NodeCancelled
			if mode == "source-skipped" {
				wantRun, wantWaiter = "success", store.NodeSkipped
			}
			if err != nil || completion.Outcome != wantRun {
				t.Fatalf("completion=%+v error=%v", completion, err)
			}
			snapshot, err := fixture.Tenant.ReadSnapshot(ctx, fixture.Accepted.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Nodes) != 3 {
				t.Fatalf("refused expansion added nodes: %+v", snapshot.Nodes)
			}
			for _, row := range snapshot.Nodes {
				if row.Ref.NodeID == "summary" && (row.Outcome != wantWaiter || row.StartedAt != nil) {
					t.Fatalf("waiter=%+v", row)
				}
				if row.Ref.NodeID == "lint" && row.Outcome != store.NodeSuccess {
					t.Fatalf("independent job=%+v", row)
				}
			}
		})
	}
}
