package nodehost

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestNodeExecutorGeneratedRecoveryUsesRecordedFailure(t *testing.T) {
	input, state, deps, _, _ := recordedReplayFixture(t)
	var job nodeproto.JobSpec
	if err := json.Unmarshal(state.row.Spec, &job); err != nil {
		t.Fatal(err)
	}
	job.OnFailureOf = "failed"
	var err error
	state.row.Spec, err = json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := executorHash(job)
	if err != nil {
		t.Fatal(err)
	}
	input.SpecHash, state.row.SpecHash = string(hash), string(hash)
	expansion := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{job}}
	state.expansions[1].Body, err = json.Marshal(expansion)
	if err != nil {
		t.Fatal(err)
	}
	hash, err = executorHash(expansion)
	if err != nil {
		t.Fatal(err)
	}
	state.expansions[1].BodyHash = string(hash)
	state.nodes["failed"] = store.NodeRow{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "failed"}, Status: store.StatusDone, Outcome: store.NodeFailed, Generation: 9, Spec: json.RawMessage(`{"id":"failed"}`)}
	failure := &nodeproto.JobFailure{Job: "failed", Stage: "action", Error: &apierr.Error{Code: apierr.SDKError, Message: "recorded source failure"}}
	deps.ReadFailure = func(_ context.Context, ref controllerwire.NodeRef, generation int64) (*nodeproto.JobFailure, error) {
		if ref != state.nodes["failed"].Ref || generation != 9 {
			t.Fatalf("failure source: %+v generation %d", ref, generation)
		}
		return failure, nil
	}
	start, executed := deps.Start, false
	deps.Start = func(ctx context.Context, input NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
		process, err := start(ctx, input, activity)
		fake := process.(*executorProcess)
		answer := fake.answer
		fake.answer = func(ctx context.Context, request nodeproto.Request) (any, error) {
			if request.Op == nodeproto.OpEval && request.Closure == "children" {
				raw, err := json.Marshal([]nodeproto.JobSpec{job})
				return nodeproto.EvalResult{Value: raw}, err
			}
			if request.Op == nodeproto.OpRunStep {
				executed = true
				if !reflect.DeepEqual(request.Failure, failure) {
					t.Fatalf("recovery failure: %+v", request.Failure)
				}
			}
			return answer(ctx, request)
		}
		return process, err
	}
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Execute(t.Context(), input); err != nil || !executed {
		t.Fatalf("generated recovery executed=%v: %v", executed, err)
	}
}

func FuzzNodeExecutorMasksOuterAndPublicationFailures(f *testing.F) {
	f.Add([]byte("secret"), true)
	f.Add([]byte("secret"), false)
	f.Fuzz(func(t *testing.T, value []byte, outer bool) {
		if len(value) == 0 || len(value) > 128 {
			return
		}
		secret := "private-" + hex.EncodeToString(value)
		masker := secrets.NewMasker()
		masker.Register(secret)
		input, state, deps, _ := executorFixture(t)
		deps.Mask = masker.Mask
		failure := errors.New(strings.Repeat("h", nodeErrTextHeadBytes-10) + secret + "\n" + strings.Repeat("y", 2*nodeErrTextMaxBytes))
		if outer {
			deps.PrepareLayer = func(context.Context, LayerSpec, NodeInput, nodeproto.JobSpec) (ExecutorFinalizer, error) {
				return nil, failure
			}
		} else {
			job := input.Accepted.Plan.Jobs[0]
			job.ResultStep = "body"
			executorChangeJob(t, &input, state, job)
			start := deps.Start
			deps.Start = func(ctx context.Context, input NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
				process, err := start(ctx, input, activity)
				process.(*executorProcess).plan = input.Accepted.Plan
				process.(*executorProcess).result.Output = json.RawMessage(`{"value":1}`)
				return process, err
			}
			deps.PublishOutput = func(context.Context, NodeInput, json.RawMessage) (*blob.RecordKey, error) {
				return nil, failure
			}
		}
		host, err := NewNodeExecutor(deps)
		if err != nil {
			t.Fatal(err)
		}
		err = host.Execute(t.Context(), input)
		raw, marshalErr := json.Marshal(state.changes)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(string(raw), secret) || !strings.Contains(string(raw), "***") {
			t.Fatal("execution failure was unreported or retained a registered secret")
		}
	})
}

func TestNodeExecutorGeneratedReplayRunsEachAncestorOnce(t *testing.T) {
	input, _, deps, processes, _ := recordedReplayFixture(t)
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	var evaluated []string
	for _, process := range *processes {
		for _, request := range process.requests {
			if request.Op == nodeproto.OpEval {
				evaluated = append(evaluated, request.Closure)
			}
		}
	}
	if !reflect.DeepEqual(evaluated, []string{"ancestors", "children"}) {
		t.Fatal("generator evaluation order:", evaluated)
	}
}

func TestNodeExecutorGeneratedReplayUsesAttemptTimeout(t *testing.T) {
	input, state, deps, processes, _ := recordedReplayFixture(t)
	input.Execution.Timeout.Set, input.Execution.Timeout.Value = true, time.Minute
	executorRetryBudget(t, &input, state, 0)
	var expire context.CancelCauseFunc
	deps.ChildContext = func(parent context.Context, duration, _ time.Duration) (context.Context, context.CancelFunc, error) {
		if duration != time.Minute {
			t.Fatalf("attempt timeout = %s", duration)
		}
		ctx, cancel := context.WithCancelCause(parent)
		expire = cancel
		return ctx, func() { cancel(context.Canceled) }, nil
	}
	start := deps.Start
	deps.Start = func(ctx context.Context, input NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
		process, err := start(ctx, input, activity)
		answer := process.(*executorProcess).answer
		process.(*executorProcess).answer = func(ctx context.Context, request nodeproto.Request) (any, error) {
			if request.Op == nodeproto.OpEval {
				expire(context.DeadlineExceeded)
				return nil, context.Cause(ctx)
			}
			return answer(ctx, request)
		}
		return process, err
	}
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if len(*processes) != 1 || (*processes)[0].closed != 1 || (*processes)[0].cleaned != 1 {
		t.Fatal("timed out generator process was not joined")
	}
	for _, request := range (*processes)[0].requests {
		if request.Op == nodeproto.OpRunStep {
			t.Fatal("body ran after generator timeout")
		}
	}
	last := state.changes[len(state.changes)-1]
	if last.To != store.NodeFailed || last.Extra.Result == nil || last.Extra.Result.Reason != string(apierr.DeadlineExceeded) {
		t.Fatalf("timeout report: %+v", last)
	}
}

func TestNodeExecutorReconstructsLongRecordedGeneratorChain(t *testing.T) {
	input, state, deps, _ := executorFixture(t)
	const depth = 70
	jobs := make([]nodeproto.JobSpec, depth+1)
	for i := range jobs {
		jobs[i] = nodeproto.JobSpec{ID: fmt.Sprintf("n%d", i), Closures: nodeproto.ClosureSpec{Generate: []string{fmt.Sprintf("g%d", i)}}}
	}
	state.nodes = make(map[string]store.NodeRow)
	for i := range jobs[:depth] {
		body, err := json.Marshal(jobs[i])
		if err != nil {
			t.Fatal(err)
		}
		hash, err := executorHash(jobs[i])
		if err != nil {
			t.Fatal(err)
		}
		state.nodes[jobs[i].ID] = store.NodeRow{Ref: controllerwire.NodeRef{RunID: "run", NodeID: jobs[i].ID}, Status: store.StatusDone, Outcome: store.NodeSuccess, Generation: runspec.ClaimGeneration(int64(i + 1)), Spec: body, SpecHash: string(hash)}
		expansion := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{jobs[i+1]}}
		body, err = json.Marshal(expansion)
		if err != nil {
			t.Fatal(err)
		}
		hash, err = executorHash(expansion)
		if err != nil {
			t.Fatal(err)
		}
		state.expansions = append(state.expansions, store.AcceptedExpansionRow{Source: state.nodes[jobs[i].ID].Ref, SourceGeneration: int64(i + 1), Body: body, BodyHash: string(hash)})
	}
	input.NodeID = jobs[depth].ID
	deps.Store = recordedReplayStore{state}
	reads := 0
	deps.ReadOutput = func(_ context.Context, ref controllerwire.NodeRef, generation int64) (json.RawMessage, error) {
		if ref != state.nodes[jobs[reads].ID].Ref || generation != int64(reads+1) {
			t.Fatalf("ancestor %d: %+v generation %d", reads, ref, generation)
		}
		reads++
		return json.RawMessage(`null`), nil
	}
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := host.(*nodeExecutor).recordedReplay(t.Context(), input, jobs[:1])
	if err != nil || len(replay) != depth || reads != depth {
		t.Fatalf("long replay = %d, reads = %d, error = %v", len(replay), reads, err)
	}
	for i, ancestor := range replay {
		if ancestor.SourceJob != jobs[i].ID || ancestor.GeneratorID != jobs[i].Closures.Generate[0] {
			t.Fatalf("ancestor %d: %+v", i, ancestor)
		}
	}
}
