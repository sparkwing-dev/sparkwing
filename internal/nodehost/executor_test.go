package nodehost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type executorStore struct {
	row        store.NodeRow
	changes    []capturedNodeCommand
	fail       store.TransitionCause
	expansions []store.AcceptedExpansionRow
	nodes      map[string]store.NodeRow
}

func (*executorStore) RecordStep(context.Context, store.StepReport) error { return nil }

func (s *executorStore) ReadSnapshot(_ context.Context, runID string) (store.RunSnapshot, error) {
	return store.RunSnapshot{Run: store.RunRow{ID: runID}, Nodes: []store.NodeRow{s.row}, Expansions: s.expansions}, nil
}

func (s *executorStore) ReadNode(_ context.Context, ref controllerwire.NodeRef) (store.NodeRow, error) {
	if ref == s.row.Ref {
		return s.row, nil
	}
	if row, ok := s.nodes[ref.NodeID]; ok {
		return row, nil
	}
	return store.NodeRow{}, errors.New("node absent")
}

func (s *executorStore) TransitionNode(_ context.Context, command store.NodeCommand) (store.Applied, error) {
	c := captureNodeCommand(command)
	s.changes = append(s.changes, c)
	if c.Actor.Kind == store.ActorHolder && (c.SourceAttempt == nil || *c.SourceAttempt != s.row.Attempt) {
		return store.Applied{}, executorError(apierr.LockHeld, "source attempt differs from current attempt")
	}
	if c.Cause == s.fail {
		return store.Applied{}, errors.New("transition refused")
	}
	if c.Cause == store.CauseRetry || c.Cause == store.CauseStart {
		s.row.Attempt++
		if c.Cause == store.CauseRetry {
			s.row.ApplicationRetries++
		}
	}
	return store.Applied{Generation: s.row.Generation, Attempt: s.row.Attempt}, nil
}

type executorProcess struct {
	mu                   sync.Mutex
	plan                 nodeproto.PlanDoc
	result               nodeproto.RunResult
	onRun                func(context.Context)
	requests             []nodeproto.Request
	closed, cleaned      int
	closeErr, cleanupErr error
	answer               func(context.Context, nodeproto.Request) (any, error)
}

func (p *executorProcess) Request(ctx context.Context, r nodeproto.Request) (nodeproto.Reply, error) {
	p.mu.Lock()
	p.requests = append(p.requests, r)
	p.mu.Unlock()
	var value any
	var err error
	if p.answer != nil {
		value, err = p.answer(ctx, r)
		if err != nil {
			return nodeproto.Reply{}, err
		}
	}
	if value == nil {
		switch r.Op {
		case nodeproto.OpHello:
			value = nodeproto.HelloResult{Protocol: nodeproto.Version, SDK: nodeproto.SDKIdentity{Language: "go", Version: "v1.0.0"}, Features: []string{}}
		case nodeproto.OpPlan:
			value = p.plan
		case nodeproto.OpRunStep:
			if p.onRun != nil {
				p.onRun(ctx)
			}
			value = p.result
			if p.result.Outcome == "failed" && p.result.Error == nil {
				failure := p.result
				failure.Error = &apierr.Error{Code: apierr.SDKError, Message: "body failed"}
				value = failure
			}
		case nodeproto.OpEval:
			value = nodeproto.EvalResult{Value: json.RawMessage("null")}
		default:
			return nodeproto.Reply{}, errors.New("unexpected operation")
		}
	}
	raw, err := json.Marshal(value)
	return nodeproto.Reply{ID: r.ID, OK: true, Result: raw}, err
}
func (p *executorProcess) Close() error                  { p.closed++; return p.closeErr }
func (p *executorProcess) Cleanup(context.Context) error { p.cleaned++; return p.cleanupErr }

func executorHash(value any) (runspec.Hash, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	var decoded any
	if err = json.Unmarshal(raw, &decoded); err != nil {
		return "", err
	}
	raw, err = json.Marshal(decoded)
	sum := sha256.Sum256(raw)
	return runspec.Hash(hex.EncodeToString(sum[:])), err
}

func executorFixture(t *testing.T) (NodeInput, *executorStore, ExecutorDependencies, *[]*executorProcess) {
	t.Helper()
	job := nodeproto.JobSpec{ID: "build", Steps: []nodeproto.StepSpec{{ID: "body"}}}
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{job}}
	planHash, _ := executorHash(plan)
	jobHash, _ := executorHash(job)
	identity := engine.Identity{Version: "v1.0.0", ManifestDigest: "manifest", Pin: engine.Pin{SDK: engine.GoSDK, Version: "v1.0.0"}}
	resolved := engine.Resolved{Identity: identity, Executable: "/verified/engine"}
	execution := runspec.JobExecutionSettings{}
	spec := runspec.Spec{Hash: "seal", PlanHash: planHash, Request: runspec.SealedRequest{RunID: "run", Request: runspec.Request{Pipeline: "ci"}}, Resolved: runspec.Resolved{Engine: identity, Settings: runspec.ExecutionSettings{Jobs: map[string]runspec.JobExecutionSettings{"build": execution}}}}
	raw, _ := json.Marshal(spec)
	input := NodeInput{Accepted: AcceptedRun{RunID: "run", Spec: raw, SpecHash: "seal", Plan: plan, PlanHash: string(planHash)}, Engine: resolved, Entry: &source.Entry{Engine: resolved}, NodeID: "build", Generation: 3, Attempt: 4, SpecHash: string(jobHash), Execution: execution, Authority: ClaimAuthority{Holder: "runner", CapabilityPrefix: "claim"}, RouteBinding: nodeproto.ProcessBinding{RunID: "run", JobID: "build"}}
	input.GrantedRunner = nodeproto.RunnerIdentity{Name: "granted", Type: "agent", Labels: []string{}}
	jobJSON, _ := json.Marshal(job)
	s := &executorStore{row: store.NodeRow{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "build"}, Status: store.StatusPending, ClaimedBy: "runner", CapabilityPrefix: "claim", Generation: 3, Attempt: 4, Spec: jobJSON, SpecHash: string(jobHash), Execution: execution}}
	processes := []*executorProcess{}
	deps := ExecutorDependencies{Store: s, Start: func(_ context.Context, _ NodeInput, _ ExecutorActivity) (ExecutorProcess, error) {
		p := &executorProcess{plan: plan, result: nodeproto.RunResult{Outcome: "success"}}
		processes = append(processes, p)
		return p, nil
	}, EnsureEngine: func(context.Context, engine.Identity, releaseasset.Target) (engine.Resolved, error) {
		return resolved, nil
	}, CanonicalHash: executorHash, ValidatePlan: func(raw json.RawMessage, _ nodeproto.ValidationContext) (nodeproto.PlanDoc, error) {
		var p nodeproto.PlanDoc
		err := json.Unmarshal(raw, &p)
		return p, err
	}, ChildContext: func(ctx context.Context, want, _ time.Duration) (context.Context, context.CancelFunc, error) {
		if want > 0 {
			c, cancel := context.WithTimeout(ctx, want)
			return c, cancel, nil
		}
		c, cancel := context.WithCancel(ctx)
		return c, cancel, nil
	}, Mask: func(text string) string { return strings.ReplaceAll(text, "private", "***") }}
	return input, s, deps, &processes
}

func TestNodeExecutorRetriesRetainGenerationAndAdvanceOrdinal(t *testing.T) {
	input, s, deps, processes := executorFixture(t)
	s.row.Execution.Retry.Set = true
	s.row.Execution.Retry.Value = 2
	input.Execution = s.row.Execution
	var spec runspec.Spec
	_ = json.Unmarshal(input.Accepted.Spec, &spec)
	spec.Resolved.Settings.Jobs[input.NodeID] = input.Execution
	input.Accepted.Spec, _ = json.Marshal(spec)
	start := deps.Start
	deps.Start = func(ctx context.Context, in NodeInput, progress ExecutorActivity) (ExecutorProcess, error) {
		p, err := start(ctx, in, progress)
		if len(*processes) < 3 {
			p.(*executorProcess).result.Outcome = "failed"
		}
		return p, err
	}
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	if err = host.Execute(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	want := []store.TransitionCause{store.CauseStart, store.CauseRetry, store.CauseRetry, store.CauseReport}
	var got []store.TransitionCause
	for _, c := range s.changes {
		got = append(got, c.Cause)
		if c.Actor.Generation != 3 {
			t.Fatalf("generation changed: %+v", c)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("causes = %v, want %v", got, want)
	}
	for i, p := range *processes {
		if p.closed != 1 || p.cleaned != 1 {
			t.Fatalf("attempt %d leaked: %+v", i, p)
		}
		for _, r := range p.requests {
			if r.Op == nodeproto.OpRunStep && r.Attempt != 5+i {
				t.Fatalf("attempt = %d, want %d", r.Attempt, 5+i)
			}
		}
	}
}

func FuzzNodeExecutorCapturesSourceAttempt(f *testing.F) {
	f.Add(uint16(0), uint8(0), false)
	f.Add(uint16(43), uint8(2), false)
	f.Add(uint16(15), uint8(1), true)
	f.Fuzz(func(t *testing.T, initial uint16, retries uint8, replaced bool) {
		input, state, deps, processes := executorFixture(t)
		input.Attempt, state.row.Attempt = runspec.AttemptOrdinal(int64(initial)), runspec.AttemptOrdinal(int64(initial))
		executorRetryBudget(t, &input, state, int(retries%4))
		start := deps.Start
		deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
			process, err := start(ctx, in, activity)
			process.(*executorProcess).result.Outcome = "failed"
			return process, err
		}
		host, err := NewNodeExecutor(deps)
		if err != nil {
			t.Fatal(err)
		}
		if err := host.Execute(t.Context(), input); err != nil {
			t.Fatal(err)
		}
		ordinal := int64(initial)
		for _, change := range state.changes {
			if change.SourceAttempt == nil || *change.SourceAttempt != runspec.AttemptOrdinal(ordinal) {
				t.Fatalf("%s lost source ordinal %d: %+v", change.Cause, ordinal, change)
			}
			if change.Cause == store.CauseStart || change.Cause == store.CauseRetry {
				ordinal++
			}
		}
		if len(*processes) != int(retries%4)+1 || state.row.Attempt != runspec.AttemptOrdinal(ordinal) {
			t.Fatal("execution did not consume authoritative attempt ordinals")
		}
		captured := state.changes[len(state.changes)-1]
		if replaced {
			state.row.Attempt++
			before := state.row
			if _, err := state.TransitionNode(t.Context(), captured.Command); err == nil || !reflect.DeepEqual(before, state.row) {
				t.Fatal("stale captured report affected a replacement attempt")
			}
		}
	})
}

func TestNodeExecutorRetryReceivesFreshProcessBinding(t *testing.T) {
	input, st, deps, processes := executorFixture(t)
	executorRetryBudget(t, &input, st, 1)
	input.RouteBinding.Routes.Bearer = "parent"
	issued := 0
	deps.Bind = func(_ context.Context, in NodeInput) (ProcessGrant, error) {
		issued++
		binding := in.RouteBinding
		binding.Routes.Bearer = fmt.Sprintf("child-%d", issued)
		return ProcessGrant{Binding: binding, Revoke: func() error { return nil }}, nil
	}
	start := deps.Start
	deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
		process, err := start(ctx, in, activity)
		p := process.(*executorProcess)
		want := fmt.Sprintf("child-%d", len(*processes))
		if in.RouteBinding.Routes.Bearer != want {
			t.Error("process adapter did not receive its delegated binding")
		}
		p.answer = func(_ context.Context, r nodeproto.Request) (any, error) {
			if r.Op == nodeproto.OpHello && r.Binding.Routes.Bearer != want {
				t.Error("retry reused a revoked bearer")
			}
			return nil, nil
		}
		if len(*processes) == 1 {
			p.result.Outcome = "failed"
		}
		return p, err
	}
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if issued != 2 || len(*processes) != 2 {
		t.Fatalf("bindings = %d, attempts = %d", issued, len(*processes))
	}
}

func TestNodeExecutorDriftPreventsBody(t *testing.T) {
	input, s, deps, processes := executorFixture(t)
	start := deps.Start
	deps.Start = func(ctx context.Context, in NodeInput, progress ExecutorActivity) (ExecutorProcess, error) {
		p, err := start(ctx, in, progress)
		p.(*executorProcess).plan.Jobs = []nodeproto.JobSpec{{ID: "build", Steps: []nodeproto.StepSpec{{ID: "changed"}}}}
		return p, err
	}
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	if err = host.Execute(context.Background(), input); err == nil {
		t.Fatal("drift accepted")
	}
	for _, p := range *processes {
		for _, r := range p.requests {
			if r.Op == nodeproto.OpRunStep {
				t.Fatal("body ran after drift")
			}
		}
	}
	if len(s.changes) != 2 || s.changes[0].Cause != store.CauseStart || s.changes[1].Cause != store.CauseReport || s.changes[1].To != store.NodeFailed {
		t.Fatalf("changes = %+v", s.changes)
	}
}

func TestNodeExecutorLostLeaseNeverReports(t *testing.T) {
	input, s, deps, processes := executorFixture(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	start := deps.Start
	deps.Start = func(ctx context.Context, in NodeInput, progress ExecutorActivity) (ExecutorProcess, error) {
		p, err := start(ctx, in, progress)
		p.(*executorProcess).onRun = func(context.Context) { cancel(lease.ErrLeaseLost) }
		return p, err
	}
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	if err = host.Execute(ctx, input); !errors.Is(err, lease.ErrLeaseLost) {
		t.Fatalf("error = %v", err)
	}
	if len(s.changes) != 1 || s.changes[0].Cause != store.CauseStart {
		t.Fatalf("changes = %+v", s.changes)
	}
	if len(*processes) != 1 || (*processes)[0].cleaned != 1 {
		t.Fatal("process cleanup was not joined")
	}
}

func executorRetryBudget(t *testing.T, input *NodeInput, s *executorStore, budget int) {
	t.Helper()
	input.Execution.Retry.Set = true
	input.Execution.Retry.Value = budget
	s.row.Execution = input.Execution
	var spec runspec.Spec
	if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	spec.Resolved.Settings.Jobs[input.NodeID] = input.Execution
	input.Accepted.Spec, _ = json.Marshal(spec)
}

func executorChangeJob(t *testing.T, input *NodeInput, s *executorStore, job nodeproto.JobSpec) {
	t.Helper()
	input.Accepted.Plan.Jobs = []nodeproto.JobSpec{job}
	hash, err := executorHash(input.Accepted.Plan)
	if err != nil {
		t.Fatal(err)
	}
	input.Accepted.PlanHash = string(hash)
	var spec runspec.Spec
	if err = json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	spec.PlanHash = hash
	input.Accepted.Spec, _ = json.Marshal(spec)
	hash, err = executorHash(job)
	if err != nil {
		t.Fatal(err)
	}
	input.SpecHash = string(hash)
	s.row.SpecHash = string(hash)
	s.row.Spec, _ = json.Marshal(job)
}

func TestNodeExecutorTransitionRefusalStopsExecution(t *testing.T) {
	for _, cause := range []store.TransitionCause{store.CauseStart, store.CauseRetry, store.CauseReport} {
		t.Run(string(cause), func(t *testing.T) {
			input, s, deps, processes := executorFixture(t)
			s.fail = cause
			if cause == store.CauseRetry {
				executorRetryBudget(t, &input, s, 2)
			}
			start := deps.Start
			deps.Start = func(ctx context.Context, in NodeInput, p ExecutorActivity) (ExecutorProcess, error) {
				process, err := start(ctx, in, p)
				if cause == store.CauseRetry {
					process.(*executorProcess).result.Outcome = "failed"
				}
				return process, err
			}
			host, err := NewNodeExecutor(deps)
			if err != nil {
				t.Fatal(err)
			}
			if err = host.Execute(context.Background(), input); err == nil {
				t.Fatal("transition refusal was ignored")
			}
			wantProcesses := 1
			if cause == store.CauseStart {
				wantProcesses = 0
			}
			if len(*processes) != wantProcesses {
				t.Fatalf("process count = %d", len(*processes))
			}
			want := 2
			if cause == store.CauseStart {
				want = 1
			}
			if len(s.changes) != want || s.changes[len(s.changes)-1].Cause != cause {
				t.Fatalf("transitions = %+v", s.changes)
			}
		})
	}
}

func TestNodeExecutorRetriesRespectPreviouslyConsumedBudget(t *testing.T) {
	input, s, deps, processes := executorFixture(t)
	executorRetryBudget(t, &input, s, 2)
	s.row.ApplicationRetries = 2
	start := deps.Start
	deps.Start = func(ctx context.Context, in NodeInput, p ExecutorActivity) (ExecutorProcess, error) {
		process, err := start(ctx, in, p)
		process.(*executorProcess).result.Outcome = "failed"
		return process, err
	}
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	if err = host.Execute(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if len(*processes) != 1 || len(s.changes) != 2 || s.changes[1].To != store.NodeFailed {
		t.Fatalf("retry budget reset: %+v", s.changes)
	}
}

func TestNodeExecutorCancellationWinsSuccessfulBody(t *testing.T) {
	input, s, deps, processes := executorFixture(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	start := deps.Start
	deps.Start = func(ctx context.Context, in NodeInput, p ExecutorActivity) (ExecutorProcess, error) {
		process, err := start(ctx, in, p)
		process.(*executorProcess).onRun = func(context.Context) { cancel(&lease.CancelError{Reason: "cancel_requested"}) }
		return process, err
	}
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	if err = host.Execute(ctx, input); err == nil {
		t.Fatal("cancellation cause was lost")
	}
	if len(*processes) != 1 || len(s.changes) != 2 || s.changes[1].To != store.NodeCancelled {
		t.Fatalf("changes = %+v", s.changes)
	}
}

func TestNodeExecutorStorageLayersWrapRetriesOnce(t *testing.T) {
	input, s, deps, _ := executorFixture(t)
	executorRetryBudget(t, &input, s, 1)
	var events []string
	deps.PrepareLayer = func(_ context.Context, layer LayerSpec, _ NodeInput, _ nodeproto.JobSpec) (ExecutorFinalizer, error) {
		events = append(events, layer.Name+" in")
		return func(context.Context, NodeInput, *store.NodeResult) error {
			events = append(events, layer.Name+" out")
			return nil
		}, nil
	}
	start := deps.Start
	attempts := 0
	deps.Start = func(ctx context.Context, in NodeInput, p ExecutorActivity) (ExecutorProcess, error) {
		attempts++
		events = append(events, "attempt")
		process, err := start(ctx, in, p)
		if attempts == 1 {
			process.(*executorProcess).result.Outcome = "failed"
		}
		return process, err
	}
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	if err = host.Execute(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	want := []string{"artifacts in", "caches in", "attempt", "attempt", "caches out", "artifacts out"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("layer order = %v, want %v", events, want)
	}
}

func TestNodeExecutorCleanupFailureCannotReportSuccess(t *testing.T) {
	input, s, deps, processes := executorFixture(t)
	cleanupErr := errors.New("cleanup failed")
	start := deps.Start
	deps.Start = func(ctx context.Context, in NodeInput, p ExecutorActivity) (ExecutorProcess, error) {
		process, err := start(ctx, in, p)
		process.(*executorProcess).cleanupErr = cleanupErr
		return process, err
	}
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	if err = host.Execute(context.Background(), input); !errors.Is(err, cleanupErr) {
		t.Fatalf("error = %v", err)
	}
	if len(*processes) != 1 || (*processes)[0].closed != 1 || (*processes)[0].cleaned != 1 || s.changes[len(s.changes)-1].To != store.NodeFailed {
		t.Fatal("cleanup failure reported success or leaked the process")
	}
}

func TestNodeExecutorTimeoutRetriesWithFreshBudget(t *testing.T) {
	input, s, deps, processes := executorFixture(t)
	executorRetryBudget(t, &input, s, 1)
	var deadlineCancel context.CancelCauseFunc
	budgets := 0
	deps.ChildContext = func(parent context.Context, want, margin time.Duration) (context.Context, context.CancelFunc, error) {
		budgets++
		ctx, cancel := context.WithCancelCause(parent)
		deadlineCancel = cancel
		return ctx, func() { cancel(nil) }, nil
	}
	start := deps.Start
	deps.Start = func(ctx context.Context, in NodeInput, p ExecutorActivity) (ExecutorProcess, error) {
		process, err := start(ctx, in, p)
		if len(*processes) == 1 {
			process.(*executorProcess).onRun = func(context.Context) { deadlineCancel(context.DeadlineExceeded) }
		}
		return process, err
	}
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	if err = host.Execute(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if budgets != 2 || len(*processes) != 2 || s.changes[len(s.changes)-1].To != store.NodeSuccess {
		t.Fatalf("budgets=%d, processes=%d, changes=%+v", budgets, len(*processes), s.changes)
	}
}

func TestNodeExecutorRejectsSDKMismatchBeforeBody(t *testing.T) {
	input, s, deps, processes := executorFixture(t)
	start := deps.Start
	deps.Start = func(ctx context.Context, in NodeInput, p ExecutorActivity) (ExecutorProcess, error) {
		process, err := start(ctx, in, p)
		process.(*executorProcess).answer = func(_ context.Context, r nodeproto.Request) (any, error) {
			if r.Op == nodeproto.OpHello {
				return nodeproto.HelloResult{Protocol: 1}, nil
			}
			return nil, nil
		}
		return process, err
	}
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	if err = host.Execute(context.Background(), input); err == nil {
		t.Fatal("SDK mismatch accepted")
	}
	if len(*processes) != 1 || len((*processes)[0].requests) != 1 || len(s.changes) != 2 || s.changes[1].To != store.NodeFailed {
		t.Fatalf("body ran: %+v", s.changes)
	}
}

func TestNodeExecutorMissingOutputPublisherFailsLoudly(t *testing.T) {
	input, s, deps, _ := executorFixture(t)
	job := input.Accepted.Plan.Jobs[0]
	job.ResultStep = "body"
	executorChangeJob(t, &input, s, job)
	deps.Start = func(context.Context, NodeInput, ExecutorActivity) (ExecutorProcess, error) {
		return &executorProcess{plan: input.Accepted.Plan, result: nodeproto.RunResult{Outcome: "success", Output: json.RawMessage(`{"value":3}`)}}, nil
	}
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	if err = host.Execute(context.Background(), input); err == nil {
		t.Fatal("missing publisher silently dropped output")
	}
	if s.changes[len(s.changes)-1].To != store.NodeFailed {
		t.Fatal("unpublished output was reported successful")
	}
}

func TestNodeExecutorRiskGateUsesSealedAllowList(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(fmt.Sprint(allowed), func(t *testing.T) {
			input, s, deps, processes := executorFixture(t)
			job := input.Accepted.Plan.Jobs[0]
			job.Steps[0].Risks = []string{"prod"}
			executorChangeJob(t, &input, s, job)
			if allowed {
				var spec runspec.Spec
				_ = json.Unmarshal(input.Accepted.Spec, &spec)
				spec.Resolved.Settings.Options.AllowRisk.Value = []string{"prod"}
				spec.Resolved.Settings.Options.AllowRisk.Set = true
				input.Accepted.Spec, _ = json.Marshal(spec)
			}
			start := deps.Start
			deps.Start = func(ctx context.Context, in NodeInput, p ExecutorActivity) (ExecutorProcess, error) {
				process, err := start(ctx, in, p)
				process.(*executorProcess).plan = input.Accepted.Plan
				return process, err
			}
			host, err := NewNodeExecutor(deps)
			if err != nil {
				t.Fatal(err)
			}
			err = host.Execute(context.Background(), input)
			if (err == nil) != allowed {
				t.Fatalf("allowed=%v, error=%v", allowed, err)
			}
			bodies := 0
			for _, p := range *processes {
				for _, r := range p.requests {
					if r.Op == nodeproto.OpRunStep {
						bodies++
					}
				}
			}
			if (bodies == 1) != allowed {
				t.Fatalf("allowed=%v, bodies=%d", allowed, bodies)
			}
		})
	}
}

func TestNodeExecutorGeneratorReplayChecksCompleteAcceptedMembership(t *testing.T) {
	for _, omit := range []bool{false, true} {
		t.Run(fmt.Sprint(omit), func(t *testing.T) {
			input, s, deps, processes := executorFixture(t)
			generated := input.Accepted.Plan.Jobs[0]
			generated.Groups = []string{"generator"}
			generatedHash, _ := executorHash(generated)
			input.SpecHash = string(generatedHash)
			s.row.SpecHash = input.SpecHash
			s.row.Spec, _ = json.Marshal(generated)
			source := nodeproto.JobSpec{ID: "source", Steps: []nodeproto.StepSpec{{ID: "body"}}, Closures: nodeproto.ClosureSpec{Generate: []string{"generator"}}}
			input.Accepted.Plan = nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{source}}
			planHash, _ := executorHash(input.Accepted.Plan)
			input.Accepted.PlanHash = string(planHash)
			var spec runspec.Spec
			_ = json.Unmarshal(input.Accepted.Spec, &spec)
			spec.PlanHash = planHash
			input.Accepted.Spec, _ = json.Marshal(spec)
			other := nodeproto.JobSpec{ID: "other", Groups: []string{"generator"}, Steps: []nodeproto.StepSpec{{ID: "body"}}}
			otherHash, _ := executorHash(other)
			otherJSON, _ := json.Marshal(other)
			s.nodes = map[string]store.NodeRow{"other": {Ref: controllerwire.NodeRef{RunID: "run", NodeID: "other"}, Spec: otherJSON, SpecHash: string(otherHash)}}
			accepted := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{generated, other}}
			body, _ := json.Marshal(accepted)
			bodyHash, _ := executorHash(accepted)
			s.expansions = []store.AcceptedExpansionRow{{Source: controllerwire.NodeRef{RunID: "run", NodeID: "source"}, SourceGeneration: 2, Body: body, BodyHash: string(bodyHash)}}
			input.Replay = []GeneratorReplay{{SourceJob: "source", SourceGeneration: 2, GeneratorID: "generator", Output: json.RawMessage(`{"seed":1}`)}}
			start := deps.Start
			deps.Start = func(ctx context.Context, in NodeInput, p ExecutorActivity) (ExecutorProcess, error) {
				process, err := start(ctx, in, p)
				fake := process.(*executorProcess)
				fake.plan = input.Accepted.Plan
				fake.answer = func(_ context.Context, r nodeproto.Request) (any, error) {
					if r.Op == nodeproto.OpEval {
						jobs := accepted.Jobs
						if omit {
							jobs = jobs[:1]
						}
						raw, _ := json.Marshal(jobs)
						return nodeproto.EvalResult{Value: raw}, nil
					}
					return nil, nil
				}
				return fake, err
			}
			host, err := NewNodeExecutor(deps)
			if err != nil {
				t.Fatal(err)
			}
			err = host.Execute(context.Background(), input)
			if (err != nil) != omit {
				t.Fatalf("omit=%v, error=%v", omit, err)
			}
			if omit {
				for _, p := range *processes {
					for _, r := range p.requests {
						if r.Op == nodeproto.OpRunStep {
							t.Fatal("body ran after member deletion")
						}
					}
				}
			}
		})
	}
}

type recordedReplayStore struct{ *executorStore }

func (s recordedReplayStore) ReadSnapshot(ctx context.Context, runID string) (store.RunSnapshot, error) {
	snapshot, err := s.executorStore.ReadSnapshot(ctx, runID)
	for _, row := range s.nodes {
		snapshot.Nodes = append(snapshot.Nodes, row)
	}
	return snapshot, err
}

func recordedReplayFixture(t *testing.T) (NodeInput, *executorStore, ExecutorDependencies, *[]*executorProcess, *[]string) {
	t.Helper()
	input, state, deps, processes := executorFixture(t)
	job := input.Accepted.Plan.Jobs[0]
	job.Groups = []string{"children"}
	executorChangeJob(t, &input, state, job)
	root := nodeproto.JobSpec{ID: "root", Closures: nodeproto.ClosureSpec{Generate: []string{"ancestors"}}}
	parent := nodeproto.JobSpec{ID: "parent", Groups: []string{"ancestors"}, Closures: nodeproto.ClosureSpec{Generate: []string{"children"}}}
	unrelated := nodeproto.JobSpec{ID: "unrelated", Closures: nodeproto.ClosureSpec{Generate: []string{"other"}}}
	input.Accepted.Plan = nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{root, unrelated}}
	hash, _ := executorHash(input.Accepted.Plan)
	input.Accepted.PlanHash = string(hash)
	var spec runspec.Spec
	_ = json.Unmarshal(input.Accepted.Spec, &spec)
	spec.PlanHash = hash
	input.Accepted.Spec, _ = json.Marshal(spec)
	state.nodes = map[string]store.NodeRow{}
	for i, declaration := range []nodeproto.JobSpec{root, parent, unrelated} {
		body, _ := json.Marshal(declaration)
		hash, _ := executorHash(declaration)
		state.nodes[declaration.ID] = store.NodeRow{Ref: controllerwire.NodeRef{RunID: "run", NodeID: declaration.ID}, Status: store.StatusDone, Outcome: store.NodeSuccess, Generation: runspec.ClaimGeneration(int64(i + 2)), Spec: body, SpecHash: string(hash)}
	}
	for i, declaration := range []nodeproto.JobSpec{parent, job, {ID: "unused", Groups: []string{"other"}}} {
		body := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{declaration}}
		raw, _ := json.Marshal(body)
		hash, _ := executorHash(body)
		state.expansions = append(state.expansions, store.AcceptedExpansionRow{Source: controllerwire.NodeRef{RunID: "run", NodeID: []string{"root", "parent", "unrelated"}[i]}, SourceGeneration: int64(i + 2), Body: raw, BodyHash: string(hash)})
	}
	reads := []string{}
	deps.Store = recordedReplayStore{state}
	deps.ReadOutput = func(_ context.Context, ref controllerwire.NodeRef, generation int64) (json.RawMessage, error) {
		if ref.RunID != "run" || ref.NodeID != "root" && ref.NodeID != "parent" || runspec.ClaimGeneration(generation) != state.nodes[ref.NodeID].Generation {
			t.Fatal("read outside the persisted ancestor generations")
		}
		reads = append(reads, ref.NodeID)
		return json.Marshal(ref.NodeID)
	}
	start := deps.Start
	deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
		process, err := start(ctx, in, activity)
		fake := process.(*executorProcess)
		fake.plan = input.Accepted.Plan
		fake.answer = func(_ context.Context, request nodeproto.Request) (any, error) {
			if request.Op != nodeproto.OpEval {
				return nil, nil
			}
			var generated nodeproto.JobSpec
			switch request.Closure {
			case "ancestors":
				generated = parent
				if string(request.Input) != `"root"` {
					t.Fatal("ancestor replay lost the recorded root output")
				}
			case "children":
				generated = job
				if string(request.Input) != `"parent"` {
					t.Fatal("child replay lost the recorded parent output")
				}
			default:
				t.Fatal("evaluated an unrelated generator")
			}
			raw, _ := json.Marshal([]nodeproto.JobSpec{generated})
			return nodeproto.EvalResult{Value: raw}, nil
		}
		return process, err
	}
	return input, state, deps, processes, &reads
}

func TestNodeExecutorReconstructsOnlyPersistedGeneratorAncestors(t *testing.T) {
	input, _, deps, processes, reads := recordedReplayFixture(t)
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*reads, []string{"root", "parent"}) {
		t.Fatal("ancestor output reads:", *reads)
	}
	if len(*processes) != 1 || (*processes)[0].closed != 1 || (*processes)[0].cleaned != 1 {
		t.Fatal("generated execution did not join its process")
	}
}

func FuzzRecordedReplayValidatesLineageBeforeOutputReads(f *testing.F) {
	for field := uint8(0); field < 10; field++ {
		f.Add(field, "changed")
	}
	f.Fuzz(func(t *testing.T, field uint8, value string) {
		input, state, deps, _, reads := recordedReplayFixture(t)
		row := state.nodes["root"]
		switch field % 10 {
		case 0:
			row.Generation++
		case 1:
			row.SpecHash = "changed:" + value
		case 2:
			state.expansions[0].BodyHash = "changed:" + value
		case 3:
			state.expansions[0].Source.RunID = "changed:" + value
		case 4:
			row.Status = store.StatusPending
		case 5:
			state.expansions = state.expansions[1:]
		case 6:
			state.expansions = append(state.expansions, state.expansions[0])
		case 7:
			row.Spec = json.RawMessage(`{"id":"changed"}`)
		case 8:
			row.Ref.NodeID = "changed:" + value
		case 9:
			state.expansions[0].SourceGeneration++
		}
		state.nodes["root"] = row
		host, err := NewNodeExecutor(deps)
		if err != nil {
			t.Fatal(err)
		}
		_, err = host.(*nodeExecutor).recordedReplay(t.Context(), input, input.Accepted.Plan.Jobs)
		if err == nil || len(*reads) != 0 {
			t.Fatalf("invalid lineage read ancestor output: reads=%v error=%v", *reads, err)
		}
	})
}

func TestRecordedReplayRejectsCyclicAncestryBeforeOutputReads(t *testing.T) {
	input, state, deps, _, reads := recordedReplayFixture(t)
	root := input.Accepted.Plan.Jobs[0]
	root.Groups = []string{"children"}
	row := state.nodes[root.ID]
	row.Spec, _ = json.Marshal(root)
	hash, _ := executorHash(root)
	row.SpecHash = string(hash)
	state.nodes[root.ID] = row
	var body nodeproto.PlanDoc
	_ = json.Unmarshal(state.expansions[1].Body, &body)
	body.Jobs = append(body.Jobs, root)
	state.expansions[1].Body, _ = json.Marshal(body)
	hash, _ = executorHash(body)
	state.expansions[1].BodyHash = string(hash)
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	_, err = host.(*nodeExecutor).recordedReplay(t.Context(), input, input.Accepted.Plan.Jobs[1:])
	if err == nil || len(*reads) != 0 {
		t.Fatalf("cyclic ancestry read source output: reads=%v error=%v", *reads, err)
	}
}

func TestNodeExecutorRecoveryReceivesPersistedFailure(t *testing.T) {
	input, s, deps, processes := executorFixture(t)
	job := input.Accepted.Plan.Jobs[0]
	job.OnFailureOf = "source"
	executorChangeJob(t, &input, s, job)
	s.nodes = map[string]store.NodeRow{"source": {Ref: controllerwire.NodeRef{RunID: "run", NodeID: "source"}, Status: store.StatusDone, Outcome: store.NodeFailed}}
	failure := nodeproto.JobFailure{Job: "source", Stage: "action", Error: &apierr.Error{Code: apierr.SDKError, Message: "recorded failure"}}
	deps.ReadFailure = func(_ context.Context, ref controllerwire.NodeRef, generation int64) (*nodeproto.JobFailure, error) {
		if runspec.ClaimGeneration(generation) != s.nodes[ref.NodeID].Generation {
			t.Fatal("failure reader lost the source generation")
		}
		return &failure, nil
	}
	start := deps.Start
	deps.Start = func(ctx context.Context, in NodeInput, p ExecutorActivity) (ExecutorProcess, error) {
		process, err := start(ctx, in, p)
		process.(*executorProcess).plan = input.Accepted.Plan
		return process, err
	}
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	if err = host.Execute(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	for _, p := range *processes {
		for _, r := range p.requests {
			if r.Op == nodeproto.OpRunStep && !reflect.DeepEqual(r.Failure, &failure) {
				t.Fatalf("recovery failure = %+v", r.Failure)
			}
		}
	}
}

func TestNodeExecutorAcceptedDriftReportsWithoutStarting(t *testing.T) {
	input, s, deps, processes := executorFixture(t)
	input.SpecHash = "drift"
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	if err = host.Execute(context.Background(), input); err == nil {
		t.Fatal("drift accepted")
	}
	if len(*processes) != 0 || len(s.changes) != 1 || s.changes[0].To != store.NodeFailed {
		t.Fatalf("changes = %+v", s.changes)
	}
}

func TestNodeExecutorFirstStartAllocatesOrdinal(t *testing.T) {
	input, s, deps, processes := executorFixture(t)
	input.Attempt, s.row.Attempt = 0, 0
	original := deps.Start
	deps.Start = func(ctx context.Context, input NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
		if input.Attempt != 1 || len(s.changes) != 1 || s.changes[0].Cause != store.CauseStart {
			t.Fatal("SDK started without the committed ordinal")
		}
		return original(ctx, input, activity)
	}
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if len(*processes) != 1 || s.row.Attempt != 1 {
		t.Fatal("first execution did not consume exactly one ordinal")
	}
}

type (
	executorStorageBytes   struct{ blob.Store }
	executorStorageRecords struct{ blob.RecordStore }
)

func TestNodeExecutorRefreshesStorageAndFinalizesLastAttempt(t *testing.T) {
	input, st, deps, processes := executorFixture(t)
	input.Attempt, st.row.Attempt = 0, 0
	executorRetryBudget(t, &input, st, 1)
	var binds, starts, finalized []int64
	deps.BindStorage = func(_ context.Context, in NodeInput) (StorageBinding, error) {
		binds = append(binds, int64(in.Attempt))
		return StorageBinding{Bytes: executorStorageBytes{}, Records: executorStorageRecords{}, Authority: blob.Authority{CapabilityRef: "storage-claim", Principal: "executor", Repo: "repo", Attempt: runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{RunID: in.Accepted.RunID, NodeID: in.NodeID, Attempt: int64(in.Attempt), Generation: int64(in.Generation)}}}}, nil
	}
	deps.PrepareLayer = func(_ context.Context, _ LayerSpec, in NodeInput, _ nodeproto.JobSpec) (ExecutorFinalizer, error) {
		if in.Storage.Authority.Attempt.Value.Attempt != 1 {
			t.Fatal("layer preparation did not receive first bound attempt")
		}
		return func(_ context.Context, latest NodeInput, _ *store.NodeResult) error {
			finalized = append(finalized, latest.Storage.Authority.Attempt.Value.Attempt)
			return nil
		}, nil
	}
	start := deps.Start
	deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
		starts = append(starts, in.Storage.Authority.Attempt.Value.Attempt)
		p, err := start(ctx, in, activity)
		if len(*processes) == 1 {
			p.(*executorProcess).result.Outcome = "failed"
		}
		return p, err
	}
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Execute(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(binds, []int64{1, 2}) || !reflect.DeepEqual(starts, binds) || !reflect.DeepEqual(finalized, []int64{2, 2}) {
		t.Fatalf("bindings=%v starts=%v finalizers=%v", binds, starts, finalized)
	}
}

func TestNodeExecutorStorageRefreshFailureSuppressesStaleFinalizers(t *testing.T) {
	input, st, deps, processes := executorFixture(t)
	executorRetryBudget(t, &input, st, 1)
	sentinel := errors.New("storage authority unavailable")
	binds, finalized := 0, 0
	deps.BindStorage = func(_ context.Context, in NodeInput) (StorageBinding, error) {
		binds++
		if binds == 2 {
			return StorageBinding{}, sentinel
		}
		return StorageBinding{Bytes: executorStorageBytes{}, Records: executorStorageRecords{}, Authority: blob.Authority{CapabilityRef: "claim", Principal: "executor", Repo: "repo", Attempt: runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{RunID: in.Accepted.RunID, NodeID: in.NodeID, Attempt: int64(in.Attempt), Generation: int64(in.Generation)}}}}, nil
	}
	deps.PrepareLayer = func(context.Context, LayerSpec, NodeInput, nodeproto.JobSpec) (ExecutorFinalizer, error) {
		return func(context.Context, NodeInput, *store.NodeResult) error { finalized++; return nil }, nil
	}
	start := deps.Start
	deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
		p, err := start(ctx, in, activity)
		p.(*executorProcess).result.Outcome = "failed"
		return p, err
	}
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	err = host.Execute(t.Context(), input)
	if !errors.Is(err, sentinel) || len(*processes) != 1 || finalized != 0 || st.changes[len(st.changes)-1].To != store.NodeFailed {
		t.Fatalf("error=%v processes=%d finalizers=%d", err, len(*processes), finalized)
	}
}

func TestNodeExecutorReportsOnlyBoundedMaskedFailureText(t *testing.T) {
	input, st, deps, _ := executorFixture(t)
	secret := "private-start-credential"
	sentinel := errors.New(secret + strings.Repeat("界", int(limits.ErrorBytes)) + "failure-tail")
	deps.Mask = func(text string) string { return strings.ReplaceAll(text, secret, "***") }
	deps.Start = func(context.Context, NodeInput, ExecutorActivity) (ExecutorProcess, error) { return nil, sentinel }
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	err = host.Execute(t.Context(), input)
	report := st.changes[len(st.changes)-1].Extra.Result
	if !errors.Is(err, sentinel) || report == nil || len(report.MaskedError) > int(limits.ErrorBytes) || strings.Contains(report.MaskedError, secret) || !strings.HasSuffix(report.MaskedError, "failure-tail") || !utf8.ValidString(report.MaskedError) {
		t.Fatalf("failure report=%+v error=%v", report, err)
	}
}
