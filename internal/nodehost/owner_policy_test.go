package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func policyFixture(t *testing.T, edit func(*nodeproto.JobSpec)) (EvaluationInput, *executorStore, evaluationDependencies, *[]*executorProcess) {
	t.Helper()
	in, st, deps, _ := evaluationFixture(t)
	job := in.Accepted.Plan.Jobs[0]
	job.Closures.Generate = nil
	edit(&job)
	in.Accepted.Plan.Jobs[0] = job
	hash, err := runspec.CanonicalHash(in.Accepted.Plan)
	if err != nil {
		t.Fatal(err)
	}
	in.Accepted.PlanHash = string(hash)
	var spec runspec.Spec
	if err := json.Unmarshal(in.Accepted.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	spec.PlanHash = hash
	in.Accepted.Spec, err = json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	st.row.Spec, err = json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	hash, err = runspec.CanonicalHash(job)
	if err != nil {
		t.Fatal(err)
	}
	st.row.SpecHash, in.SpecHash = string(hash), string(hash)
	st.row.Generation, in.Generation = 0, 0
	st.row.Status, st.row.Outcome, st.row.Expansion = runstate.StatusPending, "", runstate.ExpansionNone
	processes := []*executorProcess{}
	deps.Start = func(context.Context, EvaluationInput) (ExecutorProcess, error) {
		p := &executorProcess{plan: in.Accepted.Plan}
		processes = append(processes, p)
		return p, nil
	}
	st.accepted = &in.Accepted
	return in, st, deps, &processes
}

func executorPolicyFixture(t *testing.T, edit func(*nodeproto.JobSpec)) (NodeInput, *executorStore, ExecutorDependencies, *[]*executorProcess) {
	t.Helper()
	in, st, deps, processes := executorFixture(t)
	job := in.Accepted.Plan.Jobs[0]
	edit(&job)
	executorChangeJob(t, &in, st, job)
	start := deps.Start
	deps.Start = func(ctx context.Context, input NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
		if input.Authority != in.Authority || input.Generation != in.Generation || input.Attempt != st.row.Attempt {
			t.Error("predicate process lost its execution award")
		}
		p, err := start(ctx, input, activity)
		p.(*executorProcess).plan = in.Accepted.Plan
		return p, err
	}
	return in, st, deps, processes
}

func executorPolicyHost(t *testing.T, deps ExecutorDependencies, in NodeInput) (*nodeExecutor, runspec.Spec, nodeproto.JobSpec) {
	t.Helper()
	host, err := NewNodeExecutor(deps)
	if err != nil {
		t.Fatal(err)
	}
	var spec runspec.Spec
	if err := json.Unmarshal(in.Accepted.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	return host.(*nodeExecutor), spec, in.Accepted.Plan.Jobs[0]
}

func executorPolicyMemoKey(ctx context.Context, host *nodeExecutor, in NodeInput, spec runspec.Spec, job nodeproto.JobSpec) (string, error) {
	var key string
	err := host.withProcess(ctx, in, spec, func(session *executorSession, code NodeInput) error {
		var err error
		key, err = host.memoKey(ctx, session, code, job)
		return err
	})
	return key, err
}

func TestExecutorPolicySkipTimeoutContinuesWithJoinedProcess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		in, _, deps, processes := executorPolicyFixture(t, func(job *nodeproto.JobSpec) {
			job.Closures.SkipIf = []string{"slow", "skip"}
			budget := int64(7)
			job.SkipIfBudgetMS = &budget
		})
		start := deps.Start
		deps.Start = func(ctx context.Context, input NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
			if len(*processes) > 0 && ((*processes)[0].closed != 1 || (*processes)[0].cleaned != 1) {
				t.Error("next predicate started before expired process joined")
			}
			p, err := start(ctx, input, activity)
			p.(*executorProcess).answer = func(ctx context.Context, request nodeproto.Request) (any, error) {
				if request.Op != nodeproto.OpEval {
					return nil, nil
				}
				if request.Closure == "slow" {
					deadline, ok := ctx.Deadline()
					if !ok || deadline.Sub(time.Now()) != 7*time.Millisecond {
						t.Error("predicate lost its declared budget")
					}
					<-ctx.Done()
					return nil, context.Cause(ctx)
				}
				return nodeproto.EvalResult{Value: json.RawMessage("true")}, nil
			}
			return p, err
		}
		host, spec, job := executorPolicyHost(t, deps, in)
		result, err := host.skipIf(t.Context(), in, spec, job)
		if err != nil || !result.Skip || !reflect.DeepEqual(result.TimedOut, []int{0}) {
			t.Fatalf("skip = %+v, error = %v", result, err)
		}
	})
}

func TestExecutorPolicySkipFailuresPreventBody(t *testing.T) {
	for _, mode := range []string{"null", "string", "sdk", "drift", "close", "cleanup", "cancel", "lease", "cancel_and_cleanup"} {
		t.Run(mode, func(t *testing.T) {
			in, _, deps, processes := executorPolicyFixture(t, func(job *nodeproto.JobSpec) {
				job.Closures.SkipIf = []string{"guard"}
			})
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(context.Canceled)
			start := deps.Start
			deps.Start = func(ctx context.Context, input NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
				process, err := start(ctx, input, activity)
				p := process.(*executorProcess)
				switch mode {
				case "drift":
					p.plan = nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "changed"}}}
				case "close":
					p.closeErr = errors.New("private close error")
				case "cleanup":
					p.cleanupErr = errors.New("private cleanup error")
				case "cancel_and_cleanup":
					p.cleanupErr = errors.New("private cleanup error")
				}
				p.answer = func(_ context.Context, request nodeproto.Request) (any, error) {
					if request.Op != nodeproto.OpEval {
						return nil, nil
					}
					result := nodeproto.EvalResult{Value: json.RawMessage("true")}
					switch mode {
					case "null":
						result.Value = json.RawMessage("null")
					case "string":
						result.Value = json.RawMessage(`"private"`)
					case "sdk":
						result.Error = &apierr.Error{Code: apierr.SDKError, Message: "private failure"}
					case "cancel", "cancel_and_cleanup":
						cancel(&lease.CancelError{})
					case "lease":
						cancel(lease.ErrLeaseLost)
					}
					return result, nil
				}
				return p, err
			}
			host, spec, job := executorPolicyHost(t, deps, in)
			result, err := host.skipIf(ctx, in, spec, job)
			if err == nil || result.Skip || len(result.TimedOut) != 0 || strings.Contains(err.Error(), "private") {
				t.Fatalf("skip = %+v, error = %v", result, err)
			}
			if mode == "lease" && !errors.Is(err, lease.ErrLeaseLost) {
				t.Fatalf("lease cause lost: %v", err)
			}
			if len(*processes) != 1 || (*processes)[0].closed != 1 || (*processes)[0].cleaned != 1 {
				t.Fatal("failed policy process leaked")
			}
		})
	}
}

func TestExecutorPolicySkipShortCircuitsBeforeBody(t *testing.T) {
	in, _, deps, processes := executorPolicyFixture(t, func(job *nodeproto.JobSpec) {
		job.Closures.SkipIf = []string{"first", "second", "unreachable"}
	})
	var evaluated []string
	start := deps.Start
	deps.Start = func(ctx context.Context, input NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
		p, err := start(ctx, input, activity)
		p.(*executorProcess).answer = func(_ context.Context, request nodeproto.Request) (any, error) {
			if request.Op != nodeproto.OpEval {
				return nil, nil
			}
			if request.Job != "" || request.Attempt != 0 {
				t.Errorf("predicate evaluation manufactured an attempt: %+v", request)
			}
			evaluated = append(evaluated, request.Closure)
			value := json.RawMessage("false")
			if request.Closure == "second" {
				value = json.RawMessage("true")
			}
			return nodeproto.EvalResult{Value: value}, nil
		}
		return p, err
	}
	host, spec, job := executorPolicyHost(t, deps, in)
	result, err := host.skipIf(t.Context(), in, spec, job)
	if err != nil || !result.Skip || len(result.TimedOut) != 0 {
		t.Fatalf("skip = %+v, error = %v", result, err)
	}
	if !reflect.DeepEqual(evaluated, []string{"first", "second"}) {
		t.Fatalf("evaluated = %v", evaluated)
	}
	for _, p := range *processes {
		if p.closed != 1 || p.cleaned != 1 {
			t.Fatal("predicate process leaked")
		}
		for _, request := range p.requests {
			if request.Op == nodeproto.OpRunStep {
				t.Fatal("predicate evaluated a body")
			}
		}
	}
}

func TestExecutorPolicyMemoKeyHashesJSONOrBypassesNull(t *testing.T) {
	for _, raw := range []string{`"key"`, `null`, `""`, `42`, `{}`, `false`, `failed_null`} {
		t.Run(raw, func(t *testing.T) {
			in, _, deps, processes := executorPolicyFixture(t, func(job *nodeproto.JobSpec) {
				job.Memo = &nodeproto.MemoSpec{}
				job.Closures.MemoKey = "memo"
			})
			start := deps.Start
			deps.Start = func(ctx context.Context, input NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
				p, err := start(ctx, input, activity)
				p.(*executorProcess).answer = func(_ context.Context, request nodeproto.Request) (any, error) {
					if request.Op == nodeproto.OpEval {
						if request.Closure != "memo" || request.Attempt != 0 {
							t.Error("memo changed closure or created an attempt")
						}
						if raw == "failed_null" {
							return nodeproto.EvalResult{Value: json.RawMessage(`null`), Error: &apierr.Error{Code: apierr.SDKError, Message: "closure failed"}}, nil
						}
						return nodeproto.EvalResult{Value: json.RawMessage(raw)}, nil
					}
					return nil, nil
				}
				return p, err
			}
			host, spec, job := executorPolicyHost(t, deps, in)
			key, err := executorPolicyMemoKey(t.Context(), host, in, spec, job)
			valid := raw != `""` && raw != `failed_null`
			if (err == nil) != valid || valid && raw != `null` && key == "" || (!valid || raw == `null`) && key != "" {
				t.Fatalf("key = %q, error = %v", key, err)
			}
			if len(*processes) != 1 || (*processes)[0].closed != 1 || (*processes)[0].cleaned != 1 {
				t.Fatal("memo process leaked")
			}
		})
	}
}

func TestExecutorPolicyProcessesReceiveFreshRouteBindings(t *testing.T) {
	in, _, deps, _ := executorPolicyFixture(t, func(job *nodeproto.JobSpec) {
		job.Closures.SkipIf = []string{"first", "second"}
	})
	in.RouteBinding.Routes.Bearer = "parent"
	bindings := []string{"first-child", "second-child"}
	var issued, started int
	deps.Bind = func(_ context.Context, input NodeInput) (ProcessGrant, error) {
		binding := input.RouteBinding
		binding.Routes.Bearer = bindings[issued]
		issued++
		return ProcessGrant{Binding: binding, Revoke: func() error { return nil }}, nil
	}
	deps.Start = func(_ context.Context, input NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
		want := bindings[started]
		started++
		if input.RouteBinding.Routes.Bearer != want {
			t.Error("process adapter did not receive its delegated binding")
		}
		return &executorProcess{plan: input.Accepted.Plan, answer: func(_ context.Context, request nodeproto.Request) (any, error) {
			if request.Op == nodeproto.OpHello && request.Binding.Routes.Bearer != want {
				t.Error("hello reused an earlier process bearer")
			}
			if request.Op == nodeproto.OpEval {
				return nodeproto.EvalResult{Value: json.RawMessage("false")}, nil
			}
			return nil, nil
		}}, nil
	}
	host, spec, job := executorPolicyHost(t, deps, in)
	result, err := host.skipIf(t.Context(), in, spec, job)
	if err != nil || result.Skip || issued != 2 || started != 2 {
		t.Fatalf("result = %+v, issued = %d, started = %d, error = %v", result, issued, started, err)
	}
}

func TestExecutorPolicyMemoFailuresPreventBody(t *testing.T) {
	for _, mode := range []string{"timeout", "cleanup", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				in, st, deps, processes := executorPolicyFixture(t, func(job *nodeproto.JobSpec) {
					job.Memo = &nodeproto.MemoSpec{}
					job.Closures.MemoKey = "memo"
				})
				ctx, cancel := context.WithCancelCause(t.Context())
				defer cancel(context.Canceled)
				start := deps.Start
				deps.Start = func(ctx context.Context, input NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
					process, err := start(ctx, input, activity)
					p := process.(*executorProcess)
					if mode == "cleanup" {
						p.cleanupErr = errors.New("private cleanup failure")
					}
					p.answer = func(ctx context.Context, request nodeproto.Request) (any, error) {
						if request.Op != nodeproto.OpEval {
							return nil, nil
						}
						if mode == "timeout" {
							<-ctx.Done()
							return nil, context.Cause(ctx)
						}
						if mode == "cancel" {
							cancel(lease.ErrLeaseLost)
						}
						return nodeproto.EvalResult{Value: json.RawMessage(`"private key"`)}, nil
					}
					return p, err
				}
				host, spec, _ := executorPolicyHost(t, deps, in)
				spec.Resolved.Settings.Options.NoMemo.Value = true
				in.Accepted.Spec, _ = json.Marshal(spec)
				st.accepted = &in.Accepted
				err := host.Execute(ctx, in)
				if err == nil || strings.Contains(err.Error(), "private") || mode == "cancel" && !errors.Is(err, lease.ErrLeaseLost) {
					t.Fatalf("memo failure = %v", err)
				}
				if len(*processes) != 1 {
					t.Fatalf("memo process count = %d, error = %v", len(*processes), err)
				}
				if mode != "cancel" {
					if len(st.changes) != 2 || st.changes[0].Cause != runstate.CauseStart || st.changes[1].Cause != runstate.CauseReport {
						t.Fatalf("memo failure did not start and settle the job: %+v", st.changes)
					}
					result := st.changes[len(st.changes)-1].Extra.Result
					if result == nil || result.Outcome != runstate.NodeFailed || strings.Contains(result.MaskedError, "private") {
						t.Fatalf("memo failure became a successful result: %+v", result)
					}
				}
				for _, request := range (*processes)[0].requests {
					if request.Op == nodeproto.OpRunStep {
						t.Fatal("body ran after memo failure")
					}
				}
				if (*processes)[0].closed != 1 || (*processes)[0].cleaned != 1 {
					t.Fatal("memo failure leaked its process")
				}
			})
		})
	}
}

func TestExecutorPolicyRefusesChangedProcessAuthority(t *testing.T) {
	for _, mode := range []string{"missing", "same_bearer", "empty_bearer", "run", "job"} {
		t.Run(mode, func(t *testing.T) {
			in, _, deps, processes := executorPolicyFixture(t, func(job *nodeproto.JobSpec) {
				job.Closures.SkipIf = []string{"condition"}
			})
			in.RouteBinding.Routes.Bearer = "parent"
			if mode != "missing" {
				deps.Bind = func(_ context.Context, in NodeInput) (ProcessGrant, error) {
					binding := in.RouteBinding
					binding.Routes.Bearer = "child"
					switch mode {
					case "same_bearer":
						binding.Routes.Bearer = "parent"
					case "empty_bearer":
						binding.Routes.Bearer = ""
					case "run":
						binding.RunID = "other"
					case "job":
						binding.JobID = "other"
					}
					return ProcessGrant{Binding: binding, Revoke: func() error { return nil }}, nil
				}
			}
			host, spec, job := executorPolicyHost(t, deps, in)
			if result, err := host.skipIf(t.Context(), in, spec, job); err == nil || result.Skip || len(*processes) != 0 {
				t.Fatalf("result = %+v, started = %d, error = %v", result, len(*processes), err)
			}
		})
	}
}
