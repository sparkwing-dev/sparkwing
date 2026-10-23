package acceptance_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
	"github.com/sparkwing-dev/sparkwing/tests/acceptance/testdata/protocolgo"
)

func testGoSDKFakeHostEvalRunLogs(t *testing.T) {
	recordLeaf(t)
	plan := sparkwing.NewPlan()
	source := sparkwing.Job(plan, "source", func(context.Context) error { return nil })
	generated := source.Generate(func(ctx context.Context, expanded *sparkwing.Plan) error {
		sparkwing.Info(ctx, "generator output")
		sparkwing.Annotate(ctx, sparkwing.Annotation{Message: "generator annotation"})
		sparkwing.Summary(ctx, "generator summary")
		sparkwing.Job(expanded, "child", func(ctx context.Context) error {
			sparkwing.Info(ctx, "child output")
			return nil
		})
		return nil
	})
	host := sparkwingtest.New(sparkwingtest.Options{})
	t.Cleanup(func() { _ = host.Close() })
	if _, err := host.Eval(t.Context(), plan, "source", generated.ID(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := host.RunStep(t.Context(), plan, "child", "run"); err != nil {
		t.Fatal(err)
	}
	records := host.Records()
	if len(records) != 4 {
		t.Fatalf("records = %+v, want three generator records and one child record", records)
	}
	for i, record := range records {
		want := ""
		if i == 3 {
			want = "child"
		}
		if record.JobID != want {
			t.Errorf("record %q has job %q, want %q", record.Msg, record.JobID, want)
		}
	}
}

func testGoSDKClosureIdentifiers(t *testing.T) {
	protocolgo.Register()
	host := sparkwingtest.New(sparkwingtest.Options{})
	t.Cleanup(func() { _ = host.Close() })
	doc, err := host.Plan(t.Context(), protocolgo.ClosureName, nil)
	if err != nil {
		t.Fatalf("valid path identifiers failed planning: %v", err)
	}
	plan := sparkwing.NewPlan()
	if err := (protocolgo.ClosurePipeline{}).Plan(t.Context(), plan, sparkwing.NoInputs{}, sparkwing.RunContext{}); err != nil {
		t.Fatal(err)
	}
	bin, cleanup, err := acceptance.BuildGoSDKFixture(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, job := range doc.Jobs {
		t.Run(job.ID, func(t *testing.T) {
			recordLeaf(t)
			process := startProtocolProbe(t, bin)
			hello := probeHello()
			hello.Binding = &nodeproto.ProcessBinding{RunID: "r", JobID: job.ID, Routes: nodeproto.ProcessRoutes{
				URL: "http://127.0.0.1:1", Bearer: "fixture",
			}}
			sendProtocolProbe(t, process, hello)
			protocolProbeReply(t, process, "hello")
			sendProtocolProbe(t, process, nodeproto.Request{
				ID: "plan", Op: nodeproto.OpPlan,
				Pipeline: protocolgo.ClosureName, Args: map[string]string{},
				Run: &nodeproto.RunContext{RunID: "r", Pipeline: protocolgo.ClosureName},
			})
			var actual nodeproto.PlanDoc
			decodeProtocolProbe(t, protocolProbeReply(t, process, "plan"), &actual)
			if !reflect.DeepEqual(doc, actual) {
				t.Fatalf("fake and process closure declarations differ: fake=%+v process=%+v", doc, actual)
			}
			checks := map[string]string{job.Closures.SkipIf[0]: "false", job.Closures.Generate[0]: "[]"}
			if job.ID == "scope/step/release" {
				checks[job.Closures.SkipIf[0]] = "true"
			}
			for _, step := range job.Steps {
				checks[step.Closures.SkipIf[0]] = "false"
				if step.ID == "release/check" {
					checks[step.Closures.SkipIf[0]] = "true"
				}
			}
			stepClosures := map[string]bool{}
			for _, step := range job.Steps {
				for _, closure := range step.Closures.SkipIf {
					stepClosures[closure] = true
				}
			}
			for closure, want := range checks {
				fake, err := host.Eval(t.Context(), plan, job.ID, closure, json.RawMessage("null"))
				if err != nil || string(fake) != want {
					t.Fatalf("fake closure %q = %s, %v; want %s", closure, fake, err, want)
				}
				request := nodeproto.Request{ID: closure, Op: nodeproto.OpEval, Closure: closure, Input: json.RawMessage("null")}
				if stepClosures[closure] {
					request.Attempt, request.Settled = 1, []string{}
				}
				sendProtocolProbe(t, process, request)
				var result nodeproto.EvalResult
				decodeProtocolProbe(t, protocolProbeReply(t, process, closure), &result)
				if result.Error != nil || string(result.Value) != want {
					t.Fatalf("process closure %q = %+v; want %s", closure, result, want)
				}
			}
			if err := process.Finish(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
