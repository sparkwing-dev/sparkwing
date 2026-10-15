package nodehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func evaluationFixture(t *testing.T) (EvaluationInput, *executorStore, EvaluationDependencies, *executorProcess) {
	t.Helper()
	input, st, exec, _ := executorFixture(t)
	job := input.Accepted.Plan.Jobs[0]
	job.Closures.Generate = []string{"first", "empty", "last"}
	input.Accepted.Plan.Jobs[0] = job
	hash, _ := executorHash(input.Accepted.Plan)
	input.Accepted.PlanHash = string(hash)
	var spec map[string]any
	if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	spec["plan_hash"] = hash
	input.Accepted.Spec, _ = json.Marshal(spec)
	st.row.Spec, _ = json.Marshal(job)
	hash, _ = executorHash(job)
	st.row.SpecHash = string(hash)
	st.row.Status, st.row.Outcome, st.row.Expansion = store.StatusDone, store.NodeSuccess, store.ExpansionExpected
	st.row.ClaimedBy, st.row.CapabilityPrefix = "", ""
	p := &executorProcess{plan: input.Accepted.Plan}
	in := EvaluationInput{Engine: input.Engine, Accepted: input.Accepted, NodeID: input.NodeID, Generation: input.Generation, SpecHash: string(hash), Entry: input.Entry, GrantedRunner: input.GrantedRunner, RouteBinding: input.RouteBinding}
	deps := EvaluationDependencies{Store: st, Start: func(context.Context, EvaluationInput) (ExecutorProcess, error) { return p, nil }, EnsureEngine: exec.EnsureEngine, ValidatePlan: exec.ValidatePlan, CanonicalHash: exec.CanonicalHash, ChildContext: exec.ChildContext, Mask: func(s string) string { return strings.ReplaceAll(s, "private", "***") }, ReadOutput: func(_ context.Context, ref store.NodeRef, generation int64) (json.RawMessage, error) {
		if ref != st.row.Ref || generation != st.row.Generation {
			t.Fatal("read output without source fence")
		}
		return json.RawMessage(`{"count":2}`), nil
	}}
	return in, st, deps, p
}

func TestOwnerEvaluationCombinesAllGenerators(t *testing.T) {
	for _, outcome := range []store.NodeState{store.NodeSuccess, store.NodeMemoized} {
		t.Run(string(outcome), func(t *testing.T) {
			in, st, deps, p := evaluationFixture(t)
			st.row.Outcome = outcome
			p.answer = func(_ context.Context, r nodeproto.Request) (any, error) {
				if r.Op != nodeproto.OpEval {
					return nil, nil
				}
				if string(r.Input) != `{"count":2}` || r.Job != "" || r.Attempt != 0 {
					t.Fatalf("eval = %+v", r)
				}
				jobs := []nodeproto.JobSpec{}
				if r.Closure != "empty" {
					jobs = append(jobs, nodeproto.JobSpec{ID: r.Closure, Groups: []string{r.Closure}})
				}
				raw, _ := json.Marshal(jobs)
				return nodeproto.EvalResult{Value: raw}, nil
			}
			h, err := NewOwnerEvaluator(deps)
			if err != nil {
				t.Fatal(err)
			}
			body, failure, err := h.Expand(t.Context(), in)
			if err != nil || failure != nil {
				t.Fatalf("failure = %+v, error = %v", failure, err)
			}
			var plan nodeproto.PlanDoc
			if err := json.Unmarshal(body, &plan); err != nil {
				t.Fatal(err)
			}
			if len(plan.Jobs) != 2 || plan.Jobs[0].ID != "first" || plan.Jobs[1].ID != "last" {
				t.Fatalf("plan = %+v", plan)
			}
			if p.closed != 1 || p.cleaned != 1 || len(st.changes) != 0 {
				t.Fatalf("cleanup = %d/%d, writes = %v", p.closed, p.cleaned, st.changes)
			}
			var ops []nodeproto.Op
			for _, r := range p.requests {
				ops = append(ops, r.Op)
			}
			if !reflect.DeepEqual(ops, []nodeproto.Op{nodeproto.OpHello, nodeproto.OpPlan, nodeproto.OpEval, nodeproto.OpEval, nodeproto.OpEval}) {
				t.Fatalf("operations = %v", ops)
			}
		})
	}
}

func TestOwnerEvaluationEmptyExpansionIsExplicit(t *testing.T) {
	in, _, deps, p := evaluationFixture(t)
	p.answer = func(_ context.Context, r nodeproto.Request) (any, error) {
		if r.Op == nodeproto.OpEval {
			return nodeproto.EvalResult{Value: json.RawMessage(`[]`)}, nil
		}
		return nil, nil
	}
	h, _ := NewOwnerEvaluator(deps)
	body, failure, err := h.Expand(t.Context(), in)
	if err != nil || failure != nil || string(body) != `{"jobs":[]}` {
		t.Fatalf("body = %s, failure = %+v, error = %v", body, failure, err)
	}
}

func TestOwnerEvaluationFailuresNeverReturnPartialTopology(t *testing.T) {
	for _, mode := range []string{"sdk", "shape", "validator", "drift", "output", "close", "cleanup"} {
		t.Run(mode, func(t *testing.T) {
			in, _, deps, p := evaluationFixture(t)
			cause := &apierr.Error{Code: apierr.SDKError, Message: "private evaluation failed"}
			p.answer = func(_ context.Context, r nodeproto.Request) (any, error) {
				if r.Op != nodeproto.OpEval {
					return nil, nil
				}
				if mode == "sdk" && r.Closure == "last" {
					return nodeproto.EvalResult{Error: cause}, nil
				}
				if mode == "shape" {
					return nodeproto.EvalResult{Value: json.RawMessage(`{"jobs":[]}`)}, nil
				}
				return nodeproto.EvalResult{Value: json.RawMessage(`[]`)}, nil
			}
			switch mode {
			case "validator":
				validate := deps.ValidatePlan
				deps.ValidatePlan = func(raw json.RawMessage, ctx nodeproto.ValidationContext) (nodeproto.PlanDoc, error) {
					if ctx.Expansion {
						return nodeproto.PlanDoc{}, cause
					}
					return validate(raw, ctx)
				}
			case "drift":
				p.plan.Jobs = []nodeproto.JobSpec{{ID: "changed"}}
			case "output":
				deps.ReadOutput = func(context.Context, store.NodeRef, int64) (json.RawMessage, error) { return nil, cause }
			case "close":
				p.closeErr = cause
			case "cleanup":
				p.cleanupErr = cause
			}
			h, _ := NewOwnerEvaluator(deps)
			body, failure, err := h.Expand(t.Context(), in)
			if err != nil || len(body) != 0 || failure == nil || strings.Contains(failure.MaskedError, "private") {
				t.Fatalf("body = %s, failure = %+v, error = %v", body, failure, err)
			}
			if mode == "drift" && failure.Code != apierr.PlanDrift {
				t.Fatalf("failure = %+v", failure)
			}
			if mode != "output" && (p.closed != 1 || p.cleaned != 1) {
				t.Fatal("unjoined process")
			}
		})
	}
}

func TestOwnerEvaluationAuthorityLossDoesNotResolveBarrier(t *testing.T) {
	for _, cause := range []error{context.Canceled, lease.ErrLeaseLost, lease.ErrSilent, &lease.CancelError{Reason: "cancel_requested"}} {
		in, _, deps, p := evaluationFixture(t)
		ctx, cancel := context.WithCancelCause(t.Context())
		p.answer = func(_ context.Context, r nodeproto.Request) (any, error) {
			if r.Op == nodeproto.OpEval {
				cancel(cause)
				return nil, cause
			}
			return nil, nil
		}
		h, _ := NewOwnerEvaluator(deps)
		body, failure, err := h.Expand(ctx, in)
		if !errors.Is(err, cause) || failure != nil || len(body) != 0 {
			t.Fatalf("body = %s, failure = %+v, error = %v", body, failure, err)
		}
		if p.closed != 1 || p.cleaned != 1 {
			t.Fatal("unjoined cancelled evaluator")
		}
	}
}

func TestOwnerEvaluationRequiresCurrentSuccessfulSource(t *testing.T) {
	for _, mode := range []string{"generation", "failed", "live", "accepted", "cancelled", "binding", "hash"} {
		t.Run(mode, func(t *testing.T) {
			in, st, deps, p := evaluationFixture(t)
			switch mode {
			case "generation":
				in.Generation++
			case "failed":
				st.row.Outcome = store.NodeFailed
			case "live":
				st.row.Status = store.StatusRunning
			case "accepted":
				st.row.Expansion = store.ExpansionAccepted
			case "cancelled":
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				h, _ := NewOwnerEvaluator(deps)
				_, _, err := h.Expand(ctx, in)
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				return
			case "binding":
				in.RouteBinding.JobID = "other"
			case "hash":
				in.SpecHash = "wrong"
			}
			h, _ := NewOwnerEvaluator(deps)
			body, failure, err := h.Expand(t.Context(), in)
			if err == nil || len(body) != 0 || failure != nil {
				t.Fatalf("body = %s, failure = %+v, error = %v", body, failure, err)
			}
			if len(p.requests) > 0 {
				t.Fatal("invalid source started SDK")
			}
		})
	}
}

func TestOwnerEvaluationCannotAttributeJobsToAnotherGenerator(t *testing.T) {
	in, _, deps, p := evaluationFixture(t)
	p.answer = func(_ context.Context, r nodeproto.Request) (any, error) {
		if r.Op == nodeproto.OpEval {
			return nodeproto.EvalResult{Value: json.RawMessage(`[{"id":"child","groups":["last"]}]`)}, nil
		}
		return nil, nil
	}
	h, _ := NewOwnerEvaluator(deps)
	body, failure, err := h.Expand(t.Context(), in)
	if err != nil || len(body) != 0 || failure == nil || failure.Code != apierr.PlanInvalid {
		t.Fatalf("body = %s, failure = %+v, error = %v", body, failure, err)
	}
}

func TestOwnerEvaluationValidatorSeesRawCombinedBodyAndPersistedTopology(t *testing.T) {
	in, st, deps, p := evaluationFixture(t)
	var validated bool
	validate := deps.ValidatePlan
	deps.ValidatePlan = func(raw json.RawMessage, ctx nodeproto.ValidationContext) (nodeproto.PlanDoc, error) {
		if !ctx.Expansion {
			return validate(raw, ctx)
		}
		validated = true
		if ctx.SourceJob != st.row.Ref.NodeID || len(ctx.ExistingJobs) != 1 || ctx.ExistingJobs[0].ID != in.NodeID {
			t.Fatalf("context = %+v", ctx)
		}
		if !strings.Contains(string(raw), `"unrecognized":true`) {
			t.Fatalf("host discarded an unknown declaration before validation: %s", raw)
		}
		return nodeproto.PlanDoc{}, &apierr.Error{Code: apierr.PlanInvalid, Message: "unknown field"}
	}
	p.answer = func(_ context.Context, r nodeproto.Request) (any, error) {
		if r.Op != nodeproto.OpEval {
			return nil, nil
		}
		raw := json.RawMessage(`[]`)
		if r.Closure == "first" {
			raw = json.RawMessage(`[{"id":"child","groups":["first"],"unrecognized":true}]`)
		}
		return nodeproto.EvalResult{Value: raw}, nil
	}
	h, _ := NewOwnerEvaluator(deps)
	body, failure, err := h.Expand(t.Context(), in)
	if !validated || err != nil || len(body) != 0 || failure == nil || failure.Code != apierr.PlanInvalid {
		t.Fatalf("validated = %v, body = %s, failure = %+v, error = %v", validated, body, failure, err)
	}
}

func TestOwnerEvaluationReconstructsNestedGeneratorSource(t *testing.T) {
	in, st, deps, p := evaluationFixture(t)
	source := in.Accepted.Plan.Jobs[0]
	root := nodeproto.JobSpec{ID: "root", Closures: nodeproto.ClosureSpec{Generate: []string{"ancestor"}}}
	source.Groups = []string{"ancestor"}
	st.row.Spec, _ = json.Marshal(source)
	hash, _ := executorHash(source)
	st.row.SpecHash, in.SpecHash = string(hash), string(hash)
	in.Accepted.Plan = nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{root}}
	p.plan = in.Accepted.Plan
	hash, _ = executorHash(in.Accepted.Plan)
	in.Accepted.PlanHash = string(hash)
	var spec map[string]any
	if err := json.Unmarshal(in.Accepted.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	spec["plan_hash"] = hash
	in.Accepted.Spec, _ = json.Marshal(spec)
	expansion := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{source}}
	hash, _ = executorHash(expansion)
	raw, _ := json.Marshal(expansion)
	st.expansions = []store.AcceptedExpansionRow{{Source: store.NodeRef{RunID: "run", NodeID: "root"}, SourceGeneration: 0, Body: raw, BodyHash: string(hash)}}
	rootBody, _ := json.Marshal(root)
	rootHash, _ := executorHash(root)
	st.nodes = map[string]store.NodeRow{"root": {Ref: store.NodeRef{RunID: "run", NodeID: "root"}, Status: store.StatusDone, Outcome: store.NodeSuccess, Spec: rootBody, SpecHash: string(rootHash)}}
	deps.Store = recordedReplayStore{st}
	rootKey := blob.MaterializedRecordKey("a", "run", "root", 0, "gh:1", blob.RefScope{}, "ci", blob.KindOutput)
	sourceKey := blob.MaterializedRecordKey("a", "run", in.NodeID, 0, "gh:1", blob.RefScope{}, "ci", blob.KindOutput)
	row := st.nodes["root"]
	row.OutputRecord = &rootKey
	st.nodes["root"] = row
	st.row.OutputRecord = &sourceKey
	authority := blob.Authority{CapabilityRef: "owner"}
	backend := &scriptedStorage{}
	backend.onLookupRecord = func(_ context.Context, request blob.RecordLookup) (blob.RecordLookupResult, error) {
		if request.Authority != authority {
			t.Fatal("storage authority changed")
		}
		if request.Key != rootKey && request.Key != sourceKey {
			t.Fatalf("unexpected selector: %+v", request.Key)
		}
		return blob.RecordLookupResult{Found: true, Record: blob.Record{Key: request.Key, Digest: blob.Digest(request.Key.Scope.Job)}}, nil
	}
	backend.onGet = func(_ context.Context, request blob.GetRequest) (blob.VerifiedReader, error) {
		value := `{"count":2}`
		if request.Digest == blob.Digest("root") {
			value = `{"root":true}`
		}
		return &outputReader{Reader: bytes.NewReader([]byte(value))}, nil
	}
	in.Storage = StorageBinding{Bytes: backend, Records: backend, Authority: authority}
	deps.ReadOutput = nil
	replayed := 0
	p.answer = func(_ context.Context, r nodeproto.Request) (any, error) {
		if r.Op != nodeproto.OpEval {
			return nil, nil
		}
		if r.Closure == "ancestor" {
			replayed++
			if r.Job != "" || string(r.Input) != `{"root":true}` {
				t.Fatalf("replay = %+v", r)
			}
			jobs, _ := json.Marshal([]nodeproto.JobSpec{source})
			return nodeproto.EvalResult{Value: jobs}, nil
		}
		if r.Job != "" || string(r.Input) != `{"count":2}` {
			t.Fatalf("expansion = %+v", r)
		}
		return nodeproto.EvalResult{Value: json.RawMessage(`[]`)}, nil
	}
	h, _ := NewOwnerEvaluator(deps)
	body, failure, err := h.Expand(t.Context(), in)
	if err != nil || failure != nil || string(body) != `{"jobs":[]}` || replayed != 1 || len(st.changes) != 0 {
		t.Fatalf("body = %s, failure = %+v, error = %v, replays = %d", body, failure, err, replayed)
	}
}

func TestOwnerEvaluationUnclaimedMemoSourceKeepsGenerationZero(t *testing.T) {
	in, st, deps, p := evaluationFixture(t)
	in.Generation, st.row.Generation, st.row.Outcome = 0, 0, store.NodeMemoized
	p.answer = func(_ context.Context, r nodeproto.Request) (any, error) {
		if r.Op == nodeproto.OpEval {
			return nodeproto.EvalResult{Value: json.RawMessage(`[]`)}, nil
		}
		return nil, nil
	}
	h, _ := NewOwnerEvaluator(deps)
	body, failure, err := h.Expand(t.Context(), in)
	if err != nil || failure != nil || string(body) != `{"jobs":[]}` {
		t.Fatalf("body = %s, failure = %+v, error = %v", body, failure, err)
	}
}
