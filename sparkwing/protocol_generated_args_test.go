package sparkwing

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
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
			want apierr.Code
		}{
			{"defaults", map[string]string{"replicas": "3"}, ""},
			{"overrides", map[string]string{"replicas": "5", "mode": "recreate"}, ""},
			{"required", map[string]string{}, apierr.InvalidArgs},
			{"enum", map[string]string{"replicas": "3", "mode": "invalid"}, apierr.InvalidArgs},
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
				// safety: generator replay must use the argument snapshot accepted by planning.
				tc.args["replicas"], tc.args["mode"] = "mutated", "mutated"
				result, err := ops.Eval(t.Context(), built, nodeproto.Request{Closure: "source/generate/0", Input: json.RawMessage("null")})
				if err != nil {
					t.Fatal(err)
				}
				if tc.want != "" {
					if result.Error == nil || result.Error.Code != tc.want || result.DeclarationsKnown || planJob(built.(*Plan), "child") != nil {
						t.Fatalf("invalid generated arguments changed the plan: %+v", result)
					}
					return
				}
				if result.Error != nil {
					t.Fatal(result.Error)
				}
				if !result.DeclarationsKnown || len(result.Arguments) == 0 {
					t.Fatalf("successful expansion did not disclose its arguments: %+v", result)
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

func TestGeneratedJobArgumentCollisions(t *testing.T) {
	for _, kind := range []string{"pipeline-name", "pipeline-short", "initial-job", "prior-expansion"} {
		t.Run(kind, func(t *testing.T) {
			name := "generated-args-collision-" + kind
			Register[generatedArgsInputs](name, func() Pipeline[generatedArgsInputs] {
				return generatedArgsFixture{declare: func(p *Plan) {
					if kind == "initial-job" {
						Job(p, "initial", &jobargsJob2{})
					}
					source := Job(p, "source", func(context.Context) error { return nil })
					if kind == "prior-expansion" {
						source.Generate(func(_ context.Context, expansion *Plan) error {
							Job(expansion, "prior", &jobargsJob2{})
							return nil
						})
					}
					source.Generate(func(_ context.Context, expansion *Plan) error {
						var job Workable = &jobargsJob2{}
						switch kind {
						case "pipeline-name":
							job = &jobargsTargetJob{}
						case "pipeline-short":
							job = &jobargsTagJob{}
						}
						expansion.Approval("gate", ApprovalConfig{}).OnFailure("collision", job)
						return nil
					})
				}}
			})
			ops := sdkOperations{}
			built, _, err := ops.Plan(t.Context(), name, nil, nodeproto.RunContext{Pipeline: name})
			if err != nil {
				t.Fatal(err)
			}
			closure := "source/generate/0"
			if kind == "prior-expansion" {
				prior, err := ops.Eval(t.Context(), built, nodeproto.Request{Closure: closure, Input: json.RawMessage("null")})
				if err != nil || prior.Error != nil {
					t.Fatalf("prior expansion = %+v, %v", prior, err)
				}
				closure = "source/generate/1"
			}
			result, err := ops.Eval(t.Context(), built, nodeproto.Request{Closure: closure, Input: json.RawMessage("null")})
			if err != nil || result.Error == nil || result.Error.Code != apierr.PlanInvalid || planJob(built.(*Plan), "collision") != nil {
				t.Fatalf("argument collision changed the plan: %+v, %v", result, err)
			}
		})
	}
}
