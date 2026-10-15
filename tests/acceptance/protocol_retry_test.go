package acceptance_test

import (
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/tests/acceptance/testdata/protocolgo"
)

func testGoSDKRetryStepOutputs(t *testing.T) {
	bin, cleanup, err := acceptance.BuildGoSDKFixture(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	process := startProtocolProbe(t, bin)
	hello := probeHello()
	hello.Binding = &nodeproto.ProcessBinding{RunID: "r", JobID: "retry", Routes: nodeproto.ProcessRoutes{
		URL: "http://127.0.0.1:1", Bearer: "retry-fixture",
	}}
	sendProtocolProbe(t, process, hello)
	var negotiated nodeproto.HelloResult
	decodeProtocolProbe(t, protocolProbeReply(t, process, "hello"), &negotiated)
	sendProtocolProbe(t, process, nodeproto.Request{
		ID: "plan", Op: nodeproto.OpPlan,
		Pipeline: protocolgo.Name, Args: map[string]string{"name": "retry"},
		Run: &nodeproto.RunContext{RunID: "r", Pipeline: protocolgo.Name},
	})
	var planned nodeproto.PlanDoc
	decodeProtocolProbe(t, protocolProbeReply(t, process, "plan"), &planned)
	closure := ""
	for _, job := range planned.Jobs {
		if job.ID == "retry" {
			for _, step := range job.Steps {
				if step.ID == "result" {
					closure = step.Closures.SkipIf[0]
				}
			}
		}
	}
	if closure == "" {
		t.Fatal("result predicate is absent")
	}
	for attempt := 4; attempt <= 5; attempt++ {
		for _, step := range []string{"source", "result"} {
			id := fmt.Sprintf("%s-%d", step, attempt)
			settled := []string{}
			if step == "result" {
				settled = append(settled, "source")
				sendProtocolProbe(t, process, nodeproto.Request{ID: "eval-" + id, Op: nodeproto.OpEval, Closure: closure, Attempt: attempt, Settled: settled})
				var evaluated nodeproto.EvalResult
				decodeProtocolProbe(t, protocolProbeReply(t, process, "eval-"+id), &evaluated)
				if evaluated.Error != nil || string(evaluated.Value) != "false" {
					t.Fatalf("predicate lost completed source: %+v", evaluated)
				}
			}
			sendProtocolProbe(t, process, nodeproto.Request{ID: id, Op: nodeproto.OpRunStep, Job: "retry", Step: step, Attempt: attempt, Settled: settled})
			var result nodeproto.RunResult
			decodeProtocolProbe(t, protocolProbeReply(t, process, id), &result)
			if result.Error != nil || result.Outcome != "success" || string(result.Output) != fmt.Sprint(attempt-3) {
				t.Fatalf("%s reused stale output: %+v", id, result)
			}
		}
	}
	sendProtocolProbe(t, process, nodeproto.Request{ID: "skipped-source", Op: nodeproto.OpEval, Closure: closure, Attempt: 6, Settled: []string{"source"}})
	var skipped nodeproto.EvalResult
	decodeProtocolProbe(t, protocolProbeReply(t, process, "skipped-source"), &skipped)
	if skipped.Error != nil || string(skipped.Value) != "true" {
		t.Fatalf("predicate reused prior-attempt source: %+v", skipped)
	}
	if err := process.Finish(); err != nil {
		t.Fatal(err)
	}
}
