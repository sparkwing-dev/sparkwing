package acceptance_test

import (
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"github.com/sparkwing-dev/sparkwing/tests/acceptance/testdata/protocolgo"
)

func testGoSDKRetryStepOutputs(t *testing.T) {
	recordLeaf(t)
	bin, cleanup, err := acceptance.BuildGoSDKFixture(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	process := startProtocolProbe(t, bin)
	hello := probeHello()
	hello.Binding = &nodewire.ProcessBinding{RunID: "r", JobID: "retry", Routes: nodewire.ProcessRoutes{
		URL: "http://127.0.0.1:1", Bearer: "retry-fixture",
	}}
	sendProtocolProbe(t, process, hello)
	var negotiated nodewire.HelloResult
	decodeProtocolProbe(t, protocolProbeReply(t, process, "hello"), &negotiated)
	sendProtocolProbe(t, process, nodewire.Request{
		ID: "plan", Op: nodewire.OpPlan,
		Pipeline: protocolgo.Name, Args: map[string]string{"name": "retry"},
		Run: &nodewire.RunContext{RunID: "r", Pipeline: protocolgo.Name},
	})
	var planned nodewire.PlanDoc
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
				sendProtocolProbe(t, process, nodewire.Request{ID: "eval-" + id, Op: nodewire.OpEval, Closure: closure, Attempt: attempt, Settled: settled})
				var evaluated nodewire.EvalResult
				decodeProtocolProbe(t, protocolProbeReply(t, process, "eval-"+id), &evaluated)
				if evaluated.Error != nil || string(evaluated.Value) != "false" {
					t.Fatalf("predicate lost completed source: %+v", evaluated)
				}
			}
			sendProtocolProbe(t, process, nodewire.Request{ID: id, Op: nodewire.OpRunStep, Job: "retry", Step: step, Attempt: attempt, Settled: settled})
			var result nodewire.RunResult
			decodeProtocolProbe(t, protocolProbeReply(t, process, id), &result)
			if result.Error != nil || result.Outcome != "success" || string(result.Output) != fmt.Sprint(attempt-3) {
				t.Fatalf("%s reused stale output: %+v", id, result)
			}
		}
	}
	sendProtocolProbe(t, process, nodewire.Request{ID: "skipped-source", Op: nodewire.OpEval, Closure: closure, Attempt: 6, Settled: []string{"source"}})
	var skipped nodewire.EvalResult
	decodeProtocolProbe(t, protocolProbeReply(t, process, "skipped-source"), &skipped)
	if skipped.Error != nil || string(skipped.Value) != "true" {
		t.Fatalf("predicate reused prior-attempt source: %+v", skipped)
	}
	if err := process.Finish(); err != nil {
		t.Fatal(err)
	}
}
