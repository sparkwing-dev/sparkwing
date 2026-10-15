package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func TestSDKEnvironmentProjection(t *testing.T) {
	t.Helper()
	for _, name := range []string{"plan", "attempt", "eval", "retry_attempt", "retry_eval"} {
		t.Run(name, func(t *testing.T) {
			operation := strings.TrimPrefix(name, "retry_")
			var trace []string
			var processes []*scriptedSDK
			input, s, _ := executionHostFixture(t, "success", &trace, &processes)
			input.Entry = &source.Entry{Command: []string{"unreachable-sdk"}}
			input.GrantedRunner = nodeproto.RunnerIdentity{Name: "awarded", Type: "cloud", Labels: []string{"pool=team"}}
			var spec runspec.Spec
			if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
				t.Fatal(err)
			}
			spec.Resolved.Settings.Options.DryRun.Value = true
			spec.Request.Request.Env, _ = runspec.EnvironmentDigest(map[string]string{"CAPTURED": "sealed"})
			if operation != name {
				// safety: retry intake preserves the source envelope while rows and SDK execution name the destination.
				spec.Request.RunID = "source-run"
			}
			input.Accepted.Spec, _ = json.Marshal(spec)
			job := nodeproto.JobSpec{ID: input.NodeID, Env: map[string]nodeproto.EnvValue{"BODY": {Value: "after plan"}, "STARTUP": {Value: "before exec", Startup: true}}}
			s.snapshot.Nodes[0].Spec, _ = json.Marshal(job)
			kind := map[string]procenv.Kind{"plan": procenv.Plan, "attempt": procenv.Attempt, "eval": procenv.Eval}[operation]
			boundary := errors.New("environment boundary refused startup")
			revoked, built, split := 0, 0, 0
			cfg := ProtocolSessionConfig{Revoke: func() error { revoked++; return nil }, Environments: SDKEnvironment{
				Store: s, Base: procenv.Base{Local: true, Captured: []string{"CAPTURED=sealed"}},
				Split: func(values []procenv.Assignment) (procenv.JobEnv, error) {
					split++
					if !reflect.DeepEqual(values, []procenv.Assignment{{Name: "BODY", Value: "after plan"}, {Name: "STARTUP", Value: "before exec", Startup: true}}) {
						t.Fatalf("persisted environment=%+v", values)
					}
					return procenv.JobEnv{Startup: values[1:], Body: values[:1]}, nil
				}, Build: func(gotKind procenv.Kind, base procenv.Base, runtime procenv.Runtime) ([]string, error) {
					built++
					if gotKind != kind || !base.Local || !reflect.DeepEqual(base.Captured, []string{"CAPTURED=sealed"}) || runtime.RunID != input.Accepted.RunID || runtime.Runner.Name != input.GrantedRunner.Name || !runtime.DryRun {
						t.Fatalf("environment policy changed sealed values: kind=%v runtime=%+v", gotKind, runtime)
					}
					if operation == "plan" {
						if runtime.JobID != "" || len(runtime.Startup) != 0 || split != 0 {
							t.Fatal("initial planning inherited job startup settings")
						}
					} else if runtime.JobID != input.NodeID || !reflect.DeepEqual(runtime.Startup, []procenv.Assignment{{Name: "STARTUP", Value: "before exec", Startup: true}}) || split != 1 {
						t.Fatal("attempt or eval startup included body environment")
					}
					return []string{"PARTIAL=refused"}, boundary
				},
			}}
			var process ExecutorProcess
			var err error
			switch operation {
			case "plan":
				process, err = cfg.StartPlanning(t.Context(), PlanningProcessInput{Entry: input.Entry, Runner: input.GrantedRunner, Environment: spec.Request.Request.Env, Run: nodeproto.RunContext{RunID: input.Accepted.RunID, Pipeline: "build", DryRun: true}}, func(context.Context, nodeproto.FrameLog) error { return nil })
			case "attempt":
				process, err = cfg.Start(t.Context(), input, ExecutorActivity{})
			case "eval":
				process, err = cfg.StartEvaluation(t.Context(), EvaluationInput{Entry: input.Entry, Accepted: input.Accepted, NodeID: input.NodeID, Generation: input.Generation, SpecHash: input.SpecHash, GrantedRunner: input.GrantedRunner})
			}
			if process != nil || !errors.Is(err, boundary) || built != 1 || revoked != 1 {
				t.Fatalf("process=%v error=%v builds=%d revocations=%d", process, err, built, revoked)
			}
		})
	}
}
