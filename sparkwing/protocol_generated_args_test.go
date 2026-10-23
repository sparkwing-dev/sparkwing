package sparkwing

import (
	"context"
	"encoding/json"
	"maps"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

type generatedArgsInputs struct {
	Target string            `flag:"target" short:"t"`
	Extra  map[string]string `flag:",extra"`
}

type generatedArgsFixture struct{ declare func(*Plan) }

func (f generatedArgsFixture) Plan(_ context.Context, p *Plan, _ generatedArgsInputs, _ RunContext) error {
	f.declare(p)
	return nil
}

func TestGeneratedJobArguments(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		kind := "job"
		if recovery {
			kind = "recovery"
		}
		for _, tc := range []struct {
			name string
			args map[string]string
		}{
			{"prepared", map[string]string{"replicas": "3", "image": "nginx:latest", "mode": "rolling"}},
			{"overrides", map[string]string{"replicas": "5", "image": "nginx:latest", "mode": "recreate"}},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				name := "generated-args-" + kind + "-" + tc.name
				var job *jobargsJob1
				Register[generatedArgsInputs](name, func() Pipeline[generatedArgsInputs] {
					return generatedArgsFixture{declare: func(p *Plan) {
						Job(p, "source", func(context.Context) error { return nil }).Generate(func(_ context.Context, expansion *Plan) error {
							job = &jobargsJob1{}
							if recovery {
								expansion.Approval("gate", ApprovalConfig{}).OnFailure("child", job)
							} else {
								Job(expansion, "child", job)
							}
							return nil
						})
					}}
				})
				ops := sdkOperations{}
				built, _, err := ops.Plan(t.Context(), name, tc.args, nodeproto.RunContext{Pipeline: name})
				if err != nil {
					t.Fatal(err)
				}
				acceptedArgs := maps.Clone(tc.args)
				// safety: generator replay must use the argument snapshot accepted by planning.
				tc.args["replicas"], tc.args["mode"] = "mutated", "mutated"
				result, err := ops.Eval(t.Context(), built, nodeproto.Request{Closure: "source/generate/0", Input: json.RawMessage("null")})
				if err != nil {
					t.Fatal(err)
				}

				if result.Error != nil {
					t.Fatal(result.Error)
				}
				if !result.DeclarationsKnown || len(result.Arguments) == 0 {
					t.Fatalf("successful expansion did not disclose its arguments: %+v", result)
				}
				var children []nodeproto.JobSpec
				if err := json.Unmarshal(result.Value, &children); err != nil || len(children) == 0 {
					t.Fatalf("generated declarations: %s, %v", result.Value, err)
				}
				var child nodeproto.JobSpec
				for _, member := range children {
					if member.ID == "child" {
						child = member
					}
				}
				prepared, err := nodeproto.PrepareJobArguments(child.Arguments, acceptedArgs)
				if err != nil {
					t.Fatal(err)
				}
				run, err := ops.RunStep(t.Context(), built, nodeproto.Request{Args: prepared, Job: "child", Step: "run", Attempt: 1, Settled: []string{}})
				if err != nil || run.Error != nil {
					t.Fatalf("run generated job: %+v, %v", run, err)
				}
				want := jobargsArgs1{Replicas: 3, Image: "nginx:latest", Mode: "rolling"}
				if tc.name == "overrides" {
					want.Replicas, want.Mode = 5, "recreate"
				}
				if got := job.Args(t.Context()); got != want {
					t.Errorf("generated arguments = %+v, want %+v", got, want)
				}
			})
		}
	}
}
