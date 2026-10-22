package nodehost

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestNodeExecutorRevokesBindingBeforeJoinAndCleanup(t *testing.T) {
	for _, mode := range []string{"success", "bind_error", "start_error", "partial_start", "nil_process", "revoke_error", "hello_error"} {
		t.Run(mode, func(t *testing.T) {
			input, st, deps, processes := executorFixture(t)
			input.RouteBinding.Routes.Bearer = "parent"
			revoked := false
			deps.Bind = func(_ context.Context, in NodeInput) (ProcessGrant, error) {
				binding := in.RouteBinding
				binding.Routes.Bearer = "child"
				grant := ProcessGrant{Binding: binding, Revoke: func() error {
					revoked = true
					for _, process := range *processes {
						if process.closed != 0 || process.cleaned != 0 {
							t.Error("authority outlived process join or cleanup")
						}
					}
					if mode == "revoke_error" {
						return errors.New("revocation failed")
					}
					return nil
				}}
				if mode == "bind_error" {
					return grant, errors.New("binding failed after delegation")
				}
				return grant, nil
			}
			start := deps.Start
			deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
				if revoked {
					t.Error("authority revoked before process startup")
				}
				if mode == "start_error" || mode == "nil_process" {
					if mode == "start_error" {
						return nil, errors.New("start failed")
					}
					return nil, nil
				}
				p, err := start(ctx, in, activity)
				if mode == "partial_start" {
					return p, errors.New("startup interrupted")
				}
				if mode == "hello_error" {
					p.(*executorProcess).answer = func(context.Context, nodeproto.Request) (any, error) {
						return nil, errors.New("hello refused")
					}
				}
				return p, err
			}
			host, err := NewNodeExecutor(deps)
			if err != nil {
				t.Fatal(err)
			}
			err = host.Execute(t.Context(), input)
			if !revoked || (err == nil) != (mode == "success") {
				t.Fatalf("revoked=%v error=%v", revoked, err)
			}
			for _, process := range *processes {
				if process.closed != 1 || process.cleaned != 1 {
					t.Fatal("failed startup leaked a process")
				}
			}
			last := st.changes[len(st.changes)-1]
			if mode == "revoke_error" && last.To != store.NodeFailed {
				t.Fatal("failed revocation became a successful result")
			}
		})
	}
}

func TestExecutorPolicyRevokesBindingOnRejectedStartup(t *testing.T) {
	for _, mode := range []string{"binding", "bind_error", "start_error", "nil_process", "revoke_error", "success"} {
		t.Run(mode, func(t *testing.T) {
			input, _, deps, processes := executorPolicyFixture(t, func(job *nodeproto.JobSpec) {
				job.Approval, job.Steps = &nodeproto.ApprovalSpec{Message: "ship?"}, nil
				job.Closures.SkipIf = []string{"condition"}
			})
			input.RouteBinding.Routes.Bearer = "parent"
			revoked := false
			deps.Bind = func(_ context.Context, in NodeInput) (ProcessGrant, error) {
				binding := in.RouteBinding
				binding.Routes.Bearer = "child"
				if mode == "binding" {
					binding.JobID = "foreign"
				}
				grant := ProcessGrant{Binding: binding, Revoke: func() error {
					revoked = true
					for _, process := range *processes {
						if process.closed != 0 || process.cleaned != 0 {
							t.Error("executor authority outlived join or cleanup")
						}
					}
					if mode == "revoke_error" {
						return errors.New("revocation failed")
					}
					return nil
				}}
				if mode == "bind_error" {
					return grant, errors.New("binding failed")
				}
				return grant, nil
			}
			start := deps.Start
			deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
				if mode == "start_error" {
					return nil, errors.New("start failed")
				}
				if mode == "nil_process" {
					return nil, nil
				}
				p, err := start(ctx, in, activity)
				p.(*executorProcess).answer = func(_ context.Context, request nodeproto.Request) (any, error) {
					if request.Op == nodeproto.OpEval {
						return nodeproto.EvalResult{Value: []byte("false")}, nil
					}
					return nil, nil
				}
				return p, err
			}
			host, spec, job := executorPolicyHost(t, deps, input)
			_, err := host.skipIf(t.Context(), input, spec, job)
			if !revoked || (err == nil) != (mode == "success") {
				t.Fatalf("revoked=%v error=%v", revoked, err)
			}
		})
	}
}

func FuzzExecutorBindingRevocation(f *testing.F) {
	f.Add("run", "job", "child", false)
	f.Add("foreign", "job", "child", false)
	f.Add("run", "foreign", "child", false)
	f.Add("run", "job", "parent", false)
	f.Add("run", "job", "", false)
	f.Add("run", "job", "child", true)
	f.Fuzz(func(t *testing.T, run, job, bearer string, bindError bool) {
		input, _, deps, processes := executorFixture(t)
		input.RouteBinding.Routes.Bearer = "parent"
		revoked := false
		deps.Bind = func(context.Context, NodeInput) (ProcessGrant, error) {
			grant := ProcessGrant{Binding: nodeproto.ProcessBinding{RunID: run, JobID: job, Routes: nodeproto.ProcessRoutes{Bearer: bearer}}, Revoke: func() error { revoked = true; return nil }}
			if bindError {
				return grant, errors.New("binding failed")
			}
			return grant, nil
		}
		host, err := NewNodeExecutor(deps)
		if err != nil {
			t.Fatal(err)
		}
		err = host.Execute(t.Context(), input)
		valid := !bindError && run == input.Accepted.RunID && job == input.NodeID && bearer != "" && bearer != "parent"
		if !revoked || (len(*processes) != 0) != valid || (err == nil) != valid {
			t.Fatalf("valid=%v revoked=%v processes=%d error=%v", valid, revoked, len(*processes), err)
		}
	})
}

func FuzzOwnerBindingRevocation(f *testing.F) {
	f.Add("run", "build", "child", false)
	f.Add("foreign", "build", "child", false)
	f.Add("run", "foreign", "child", false)
	f.Add("run", "build", "parent", false)
	f.Add("run", "build", "", false)
	f.Add("run", "build", "child", true)
	f.Fuzz(func(t *testing.T, run, job, bearer string, bindError bool) {
		input, _, deps, processes := policyFixture(t, func(job *nodeproto.JobSpec) {
			job.Approval, job.Steps = &nodeproto.ApprovalSpec{Message: "ship?"}, nil
			job.Closures.SkipIf = []string{"condition"}
		})
		input.RouteBinding.Routes.Bearer = "parent"
		revoked := false
		deps.Bind = func(context.Context, EvaluationInput) (ProcessGrant, error) {
			grant := ProcessGrant{Binding: nodeproto.ProcessBinding{RunID: run, JobID: job, Routes: nodeproto.ProcessRoutes{Bearer: bearer}}, Revoke: func() error { revoked = true; return nil }}
			if bindError {
				return grant, errors.New("binding failed")
			}
			return grant, nil
		}
		start := deps.Start
		deps.Start = func(ctx context.Context, in EvaluationInput) (ExecutorProcess, error) {
			p, err := start(ctx, in)
			p.(*executorProcess).answer = func(_ context.Context, request nodeproto.Request) (any, error) {
				if request.Op == nodeproto.OpEval {
					return nodeproto.EvalResult{Value: []byte("false")}, nil
				}
				return nil, nil
			}
			return p, err
		}
		host, err := NewOwnerEvaluator(deps)
		if err != nil {
			t.Fatal(err)
		}
		_, err = host.approvalSkipIf(t.Context(), input)
		valid := !bindError && run == input.Accepted.RunID && job == input.NodeID && bearer != "" && bearer != "parent"
		if !revoked || (len(*processes) != 0) != valid || (err == nil) != valid {
			t.Fatalf("valid=%v revoked=%v processes=%d error=%v", valid, revoked, len(*processes), err)
		}
	})
}
