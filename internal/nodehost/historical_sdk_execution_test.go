//go:build unix

package nodehost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/sdkcompat"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type historicalExecutionFixture struct {
	input       NodeInput
	state       *executionHostFixtureState
	deps        ExecutorDependencies
	processes   []*scriptedSDK
	resolutions int
	revocations int
}

func historicalExecutionLifecycle(t *testing.T, ctx context.Context, prepared sdkcompat.Prepared, identity engineid.Identity, runner nodeproto.RunnerIdentity, plan nodeproto.PlanDoc, args map[string]string, environment procenv.Env) {
	t.Helper()
	identity.ManifestDigest = historicalHostDigest(t)
	t.Run("executor-retry-concurrency", func(t *testing.T) {
		var mu sync.Mutex
		waiting, requests := 0, 0
		ready := make(chan struct{})
		barrier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			waiting++
			requests++
			round := ready
			if waiting == 2 {
				close(ready)
				ready, waiting = make(chan struct{}), 0
			}
			mu.Unlock()
			select {
			case <-round:
				w.WriteHeader(http.StatusNoContent)
			case <-r.Context().Done():
			}
		}))
		defer barrier.Close()
		env := historicalExecutionEnvironment(t, environment, map[string]string{
			"COMPAT_LIFECYCLE_BARRIER_URL": barrier.URL,
			"COMPAT_RETRY_FILE":            filepath.Join(t.TempDir(), "retry"),
		})
		fixture := newHistoricalExecutionFixture(t, prepared, identity, runner, plan, args, env, "lifecycle")
		fixture.execute(t, ctx, runstate.NodeSuccess, 2)
		mu.Lock()
		defer mu.Unlock()
		if requests != 4 || waiting != 0 {
			t.Fatalf("concurrent helper requests=%d waiting=%d, want two overlapping steps in each attempt", requests, waiting)
		}
		states := map[string][]string{}
		for _, report := range fixture.state.steps {
			states[report.StepID] = append(states[report.StepID], report.State)
		}
		for _, step := range []string{"concurrent-a", "concurrent-b"} {
			if !slices.Equal(states[step], []string{"running", "passed", "running", "passed"}) {
				t.Fatalf("%s step states=%v", step, states[step])
			}
		}
		if !slices.Equal(states["retry"], []string{"running", "failed", "running", "passed"}) {
			t.Fatalf("retry step states=%v", states["retry"])
		}
		failures := 0
		for _, change := range fixture.state.reports {
			if change.Cause == runstate.CauseRetry {
				failures++
				if change.Extra.Result == nil || !strings.Contains(change.Extra.Result.MaskedError, "7") {
					t.Fatalf("retry lost the released SDK's nonzero exit: %+v", change)
				}
			}
		}
		if failures != 1 {
			t.Fatalf("host retry transitions=%d, want one", failures)
		}
	})
	t.Run("executor-dependent", func(t *testing.T) {
		fixture := newHistoricalExecutionFixture(t, prepared, identity, runner, plan, args, environment, "dependent")
		for _, job := range plan.Jobs {
			if job.ID != "task" {
				continue
			}
			body, err := json.Marshal(job)
			if err != nil {
				t.Fatal(err)
			}
			hash, err := runspec.CanonicalHash(job)
			if err != nil {
				t.Fatal(err)
			}
			fixture.state.snapshot.Nodes = append(fixture.state.snapshot.Nodes, runstate.NodeRow{
				Ref: runspec.NodeRef{RunID: "run", NodeID: "task"}, Spec: body, SpecHash: string(hash),
				Status: runstate.StatusDone, Outcome: runstate.NodeSuccess,
			})
		}
		fixture.execute(t, ctx, runstate.NodeSuccess, 1)
		if len(fixture.state.steps) == 0 || fixture.state.steps[len(fixture.state.steps)-1].State != "passed" {
			t.Fatalf("dependent job step records=%+v", fixture.state.steps)
		}
	})
	t.Run("executor-timeout", func(t *testing.T) {
		var deadlineMu sync.Mutex
		var expire context.CancelCauseFunc
		barrier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			deadlineMu.Lock()
			cancel := expire
			deadlineMu.Unlock()
			cancel(context.DeadlineExceeded)
			<-r.Context().Done()
		}))
		defer barrier.Close()
		env := historicalExecutionEnvironment(t, environment, map[string]string{"COMPAT_LIFECYCLE_BARRIER_URL": barrier.URL})
		fixture := newHistoricalExecutionFixture(t, prepared, identity, runner, plan, args, env, "lifecycle")
		fixture.input.Execution.Retry.Value, fixture.state.snapshot.Nodes[0].Execution.Retry.Value = 0, 0
		fixture.state.snapshot.Run.Spec.Resolved.Settings.Jobs["lifecycle"] = fixture.input.Execution
		var err error
		fixture.input.Accepted.Spec, err = json.Marshal(fixture.state.snapshot.Run.Spec)
		if err != nil {
			t.Fatal(err)
		}
		fixture.deps.ChildContext = func(parent context.Context, duration, _ time.Duration) (context.Context, context.CancelFunc, error) {
			if duration != time.Minute {
				t.Errorf("historical timeout budget=%s, want one minute", duration)
			}
			child, cancel := context.WithCancelCause(parent)
			deadlineMu.Lock()
			expire = cancel
			deadlineMu.Unlock()
			return child, func() { cancel(nil) }, nil
		}
		fixture.execute(t, ctx, runstate.NodeFailed, 1)
		final := fixture.state.reports[len(fixture.state.reports)-1]
		if final.Extra.Result == nil || final.Extra.Result.Reason != string(apierr.DeadlineExceeded) {
			t.Fatalf("historical deadline outcome=%+v", final.Extra.Result)
		}
	})
}

func historicalExecutionEnvironment(t *testing.T, environment procenv.Env, overrides map[string]string) procenv.Env {
	t.Helper()
	values := slices.Collect(environment.Values())
	values = slices.DeleteFunc(values, func(assignment string) bool {
		name, _, _ := strings.Cut(assignment, "=")
		_, overridden := overrides[name]
		return overridden
	})
	for name, value := range overrides {
		values = append(values, name+"="+value)
	}
	return testProcessEnv(t, values...)
}

func newHistoricalExecutionFixture(t *testing.T, prepared sdkcompat.Prepared, identity engineid.Identity, runner nodeproto.RunnerIdentity, plan nodeproto.PlanDoc, args map[string]string, environment procenv.Env, jobID string) *historicalExecutionFixture {
	t.Helper()
	fixture := &historicalExecutionFixture{}
	var trace []string
	var unused []*scriptedSDK
	fixture.input, fixture.state, _ = executionHostFixture(t, "success", &trace, &unused, func(deps *ExecutorDependencies) { fixture.deps = *deps })
	input, state := &fixture.input, fixture.state
	var job nodeproto.JobSpec
	for _, candidate := range plan.Jobs {
		if candidate.ID == jobID {
			job = candidate
		}
	}
	if job.ID == "" {
		t.Fatalf("historical plan lacks %s", jobID)
	}
	row := &state.snapshot.Nodes[0]
	input.NodeID, input.RouteBinding.JobID, row.Ref.NodeID = jobID, jobID, runspec.NodeID(jobID)
	input.Engine = engine.Resolved{Identity: identity}
	input.Entry = &source.Entry{Engine: input.Engine, Command: prepared.Command, Directory: prepared.Dir}
	input.GrantedRunner, input.Accepted.Plan = runner, plan
	input.Execution.Retry.Value, row.Execution.Retry.Value = 1, 1
	input.Execution.Timeout.Set, input.Execution.Timeout.Value = true, time.Minute
	row.Execution = input.Execution
	planHash, err := runspec.CanonicalHash(plan)
	if err != nil {
		t.Fatal(err)
	}
	input.Accepted.PlanHash = string(planHash)
	var spec runspec.Spec
	if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	spec.PlanHash, spec.Resolved.Engine, spec.Resolved.Args = planHash, identity, args
	spec.Request.Request.Pipeline, spec.Resolved.Entrypoint = "compat", "compat"
	spec.Request.Request.Origin = runspec.OriginLocal
	spec.Request.Request.Actor.Display = "compat"
	spec.Request.Request.Git.Ref = "main"
	spec.Request.Request.Git.Repository.Display = "example/local"
	spec.Resolved.Settings.Jobs = map[string]runspec.JobExecutionSettings{jobID: input.Execution}
	values, err := capturedValues(slices.Collect(environment.Values()))
	if err != nil {
		t.Fatal(err)
	}
	spec.Request.Request.Env, err = runspec.EnvironmentDigest(values, strings.Repeat("5", 64))
	if err != nil {
		t.Fatal(err)
	}
	input.Accepted.Spec, err = json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	state.snapshot.Run.Spec, state.snapshot.Run.PlanHash = &spec, input.Accepted.PlanHash
	state.snapshot.Run.Plan, err = json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	jobHash, err := runspec.CanonicalHash(job)
	if err != nil {
		t.Fatal(err)
	}
	input.SpecHash, row.SpecHash = string(jobHash), string(jobHash)
	row.Spec, err = json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	var recordMu sync.Mutex
	state.recordStep = func(_ context.Context, report store.StepReport) error {
		recordMu.Lock()
		defer recordMu.Unlock()
		state.steps = append(state.steps, report)
		return nil
	}
	fixture.deps.PublishOutput = func(_ context.Context, in NodeInput, output json.RawMessage) (*blob.RecordKey, error) {
		if in.NodeID != "dependent" || string(output) != "null" {
			return nil, fmt.Errorf("unexpected historical %s output: %s", in.NodeID, output)
		}
		return &blob.RecordKey{}, nil
	}
	fixture.deps.Validation = nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: ownerSealSecrets{}}
	fixture.deps.EnsureEngine = func(_ context.Context, selected engineid.Identity, _ releaseasset.Target) (engine.Resolved, error) {
		fixture.resolutions++
		if !reflect.DeepEqual(selected, identity) {
			return engine.Resolved{}, fmt.Errorf("historical resolver replaced candidate identity: %+v", selected)
		}
		return input.Engine, nil
	}
	fixture.deps.Bind = func(_ context.Context, in NodeInput) (ProcessGrant, error) {
		binding := in.RouteBinding
		binding.Routes = nodeproto.ProcessRoutes{URL: "http://127.0.0.1:1", Bearer: fmt.Sprintf("historical-%d", in.Attempt)}
		var revoke sync.Once
		return ProcessGrant{Binding: binding, Revoke: func() error { revoke.Do(func() { fixture.revocations++ }); return nil }}, nil
	}
	cfg := ProtocolSessionConfig{Environments: SDKEnvironment{Store: state, Base: procenv.Base{Local: true, Captured: slices.Collect(environment.Values())}}}
	fixture.deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
		process, err := cfg.startNode(ctx, in, activity)
		if process == nil {
			return nil, err
		}
		child := &scriptedSDK{request: process.Request, onClose: process.Close, onCleanup: process.Cleanup}
		fixture.processes = append(fixture.processes, child)
		return child, err
	}
	return fixture
}

func (fixture *historicalExecutionFixture) execute(t *testing.T, ctx context.Context, want runstate.NodeState, attempts int) {
	t.Helper()
	executor, err := NewNodeExecutor(fixture.deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.Execute(ctx, fixture.input); err != nil {
		t.Fatal(err)
	}
	if len(fixture.state.reports) == 0 || fixture.state.reports[len(fixture.state.reports)-1].To != want {
		t.Fatalf("historical %s outcome=%+v, want %s", fixture.input.NodeID, fixture.state.reports, want)
	}
	if len(fixture.processes) != attempts || fixture.resolutions != attempts || fixture.revocations != attempts {
		t.Fatalf("processes=%d resolutions=%d revocations=%d, want %d attempts", len(fixture.processes), fixture.resolutions, fixture.revocations, attempts)
	}
	for _, process := range fixture.processes {
		if !process.closed || !process.cleaned {
			t.Fatal("historical SDK process was not joined and cleaned")
		}
		if len(process.requests) < 3 || process.requests[0].Op != nodeproto.OpHello || process.requests[1].Op != nodeproto.OpPlan {
			t.Fatalf("executor bypassed real hello or replan: %+v", process.requests)
		}
	}
}
