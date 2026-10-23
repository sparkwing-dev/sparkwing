package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestSDKEnvironmentProjectsSealedValuesAndPersistedGeneratedJob(t *testing.T) {
	input, st, _, _ := executorFixture(t)
	var spec runspec.Spec
	if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	spec.Request.Request.Git = runspec.Git{Ref: "feature", SHA: "sealed-sha", Repository: runspec.Repository{Display: "acme/repo"}}
	spec.Request.Request.Origin = runspec.OriginApp
	spec.Request.Request.Env, _ = runspec.EnvironmentDigest(map[string]string{"CAPTURED": "snapshot"}, strings.Repeat("5", 64))
	spec.Resolved.Settings.Options.DryRun.Set, spec.Resolved.Settings.Options.DryRun.Value = true, true
	input.Accepted.Spec, _ = json.Marshal(spec)
	input.GrantedRunner = nodeproto.RunnerIdentity{Name: "awarded", Type: "cloud", Labels: []string{"gpu"}}
	// safety: generated declarations are absent from the initial plan; their environment comes from persistence.
	input.NodeID, input.RouteBinding.JobID = "generated", "generated"
	st.row.Ref.NodeID = runspec.NodeID(input.NodeID)
	job := nodeproto.JobSpec{ID: input.NodeID, Env: map[string]nodeproto.EnvValue{
		"STARTUP": {Value: "interpreter", Startup: true}, "BODY": {Value: "body"},
	}}
	st.row.Spec, _ = json.Marshal(job)
	base := procenv.Base{Local: true, Captured: []string{"CAPTURED=snapshot"}, Ambient: []string{"AMBIENT=host"}, SubmitEnvAllow: []string{"ALLOWED"}}
	for _, kind := range []procenv.Kind{procenv.Attempt, procenv.Eval} {
		t.Run(map[procenv.Kind]string{procenv.Attempt: "attempt", procenv.Eval: "eval"}[kind], func(t *testing.T) {
			called := false
			policy := SDKEnvironment{Base: base, Store: st, Split: func(values []procenv.Assignment) (procenv.JobEnv, error) {
				want := []procenv.Assignment{{Name: "BODY", Value: "body"}, {Name: "STARTUP", Value: "interpreter", Startup: true}}
				if !reflect.DeepEqual(values, want) {
					t.Fatalf("job env = %+v", values)
				}
				return procenv.JobEnv{Startup: values[1:], Body: values[:1]}, nil
			}, Build: func(gotKind procenv.Kind, gotBase procenv.Base, runtime procenv.Runtime) (procenv.Env, error) {
				called = true
				if gotKind != kind || !reflect.DeepEqual(gotBase, base) {
					t.Fatal("environment policy lost process kind or captured inputs")
				}
				want := procenv.Runtime{RunID: "run", JobID: "generated", Runner: procenv.RunnerInfo{Name: "awarded", Type: "cloud", Labels: []string{"gpu"}}, DryRun: true, Branch: "feature", Commit: "sealed-sha", Repo: "acme/repo", TriggerSource: "github_app", Startup: []procenv.Assignment{{Name: "STARTUP", Value: "interpreter", Startup: true}}}
				if !reflect.DeepEqual(runtime, want) {
					t.Fatalf("runtime = %+v", runtime)
				}
				return procenv.Build(procenv.Install, procenv.Base{Local: true, Captured: []string{"BUILT=policy"}}, procenv.Runtime{})
			}}
			env, err := policy.job(t.Context(), kind, input.Accepted, input.NodeID, int64(input.Generation), input.SpecHash, input.GrantedRunner)
			if err != nil || !called || !reflect.DeepEqual(slices.Collect(env.Values()), []string{"BUILT=policy"}) {
				t.Fatalf("environment = %v, error = %v", env, err)
			}
		})
	}
}

func TestSDKPlanningEnvironmentHasNoJobStartup(t *testing.T) {
	input := PlanningProcessInput{Run: nodeproto.RunContext{RunID: "run", Pipeline: "ci", DryRun: true, Git: &nodeproto.Git{SHA: "sha", Branch: "topic", Repo: "repo"}, Trigger: &nodeproto.Trigger{Source: "github_actions"}}, Runner: nodeproto.RunnerIdentity{Name: "actions", Type: "agent", Labels: []string{"linux"}}}
	policy := SDKEnvironment{Build: func(kind procenv.Kind, _ procenv.Base, runtime procenv.Runtime) (procenv.Env, error) {
		if kind != procenv.Plan || runtime.RunID != "run" || runtime.JobID != "" || !runtime.DryRun || runtime.Commit != "sha" || runtime.Branch != "topic" || runtime.Repo != "repo" || runtime.TriggerSource != "github_actions" || runtime.Runner.Name != "actions" || len(runtime.Startup) != 0 {
			t.Fatalf("planning environment = %+v", runtime)
		}
		return procenv.Env{}, nil
	}}
	if _, err := policy.planning(t.Context(), input); err != nil {
		t.Fatal(err)
	}
}

func TestSDKEnvironmentRefusesMissingOrChangedPersistedIdentity(t *testing.T) {
	for _, mode := range []string{"missing_store", "read", "run", "job", "generation", "hash", "missing_hash", "declaration", "seal", "request", "split", "build"} {
		t.Run(mode, func(t *testing.T) {
			input, st, _, _ := executorFixture(t)
			refused := errors.New("policy refused")
			called := false
			policy := SDKEnvironment{Store: st, Split: func([]procenv.Assignment) (procenv.JobEnv, error) {
				if mode == "split" {
					return procenv.JobEnv{}, refused
				}
				return procenv.JobEnv{}, nil
			}, Build: func(procenv.Kind, procenv.Base, procenv.Runtime) (procenv.Env, error) {
				called = true
				return testProcessEnv(t, "PARTIAL=unsafe"), refused
			}}
			switch mode {
			case "missing_store":
				policy.Store = nil
			case "read":
				policy.Store = environmentReadError{err: refused}
			case "run":
				st.row.Ref.RunID = "other"
			case "job":
				st.row.Ref.NodeID = "other"
			case "generation":
				st.row.Generation++
			case "hash":
				st.row.SpecHash = "other"
			case "missing_hash":
				input.SpecHash, st.row.SpecHash = "", ""
			case "declaration":
				st.row.Spec = json.RawMessage(`{"id":"other"}`)
			case "seal":
				input.Accepted.SpecHash = "other"
			case "request":
				input.Accepted.RunID = "other"
			}
			env, err := policy.job(t.Context(), procenv.Attempt, input.Accepted, input.NodeID, int64(input.Generation), input.SpecHash, input.GrantedRunner)
			if err == nil || len(slices.Collect(env.Values())) != 0 || called != (mode == "build") {
				t.Fatalf("environment = %v, built = %v, error = %v", env, called, err)
			}
			if (mode == "split" || mode == "read" || mode == "build") && !errors.Is(err, refused) {
				t.Fatalf("lost policy error: %v", err)
			}
		})
	}
}

type environmentReadError struct {
	EvaluationStore
	err error
}

func sdkTestEnvironment(st EvaluationStore) SDKEnvironment {
	return SDKEnvironment{
		Store: st, Base: procenv.Base{Captured: []string{"GORACE=atexit_sleep_ms=0"}},
		Build: func(_ procenv.Kind, base procenv.Base, _ procenv.Runtime) (procenv.Env, error) {
			return procenv.Build(procenv.Install, procenv.Base{Local: true, Captured: base.Captured}, procenv.Runtime{})
		}, Split: func(values []procenv.Assignment) (procenv.JobEnv, error) {
			if len(values) != 0 {
				return procenv.JobEnv{}, errors.New("fixture did not specify job environment policy")
			}
			return procenv.JobEnv{}, nil
		},
	}
}

func (s environmentReadError) ReadNode(context.Context, runspec.NodeRef) (runstate.NodeRow, error) {
	return runstate.NodeRow{}, s.err
}

func testProcessEnv(t testing.TB, values ...string) procenv.Env {
	t.Helper()
	env, err := procenv.Build(procenv.Install, procenv.Base{Local: true, Captured: values}, procenv.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	return env
}
