package nodehost

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

func FuzzNativeStartupGrantIsolation(f *testing.F) {
	f.Add("run", "build", "child", false)
	f.Add("foreign", "build", "child", false)
	f.Add("run", "foreign", "child", false)
	f.Add("run", "build", "parent", false)
	f.Add("run", "build", "child", true)
	f.Fuzz(func(t *testing.T, run, job, bearer string, bindFailed bool) {
		for _, owner := range []bool{false, true} {
			revoked, stale, projected := false, false, false
			refused := errors.New("environment refused")
			grant := ProcessGrant{Binding: nodeproto.ProcessBinding{RunID: run, JobID: job, Routes: nodeproto.ProcessRoutes{Bearer: bearer}}, Revoke: func() error { revoked = true; return nil }}
			var bindError error
			if bindFailed {
				bindError = errors.New("delegation interrupted")
			}
			cfg := ProtocolSessionConfig{Revoke: func() error { stale = true; return nil }}
			cfg.Environments.Build = func(procenv.Kind, procenv.Base, procenv.Runtime) ([]string, error) {
				projected = true
				return nil, refused
			}
			var err error
			if owner {
				input, st, deps, _ := policyFixture(t, func(job *nodeproto.JobSpec) { job.Closures.SkipIf = []string{"condition"} })
				input.RouteBinding.Routes.Bearer = "parent"
				cfg.Environments.Store = st
				deps.Bind = func(context.Context, EvaluationInput) (ProcessGrant, error) { return grant, bindError }
				deps.Start = cfg.StartEvaluation
				host, buildErr := NewOwnerEvaluator(deps)
				if buildErr != nil {
					t.Fatal(buildErr)
				}
				_, err = host.SkipIf(t.Context(), input)
			} else {
				input, st, deps, _ := executorFixture(t)
				input.RouteBinding.Routes.Bearer = "parent"
				cfg.Environments.Store = st
				deps.Bind = func(context.Context, NodeInput) (ProcessGrant, error) { return grant, bindError }
				deps.Start = cfg.Start
				host, buildErr := NewNodeExecutor(deps)
				if buildErr != nil {
					t.Fatal(buildErr)
				}
				err = host.Execute(t.Context(), input)
			}
			valid := !bindFailed && run == "run" && job == "build" && bearer != "" && bearer != "parent"
			if err == nil || !revoked || stale || projected != valid {
				t.Fatalf("owner=%v valid=%v revoked=%v stale=%v projected=%v error=%v", owner, valid, revoked, stale, projected, err)
			}
		}
	})
}
