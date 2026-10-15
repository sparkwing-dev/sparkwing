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
	for attempt := 4; attempt <= 5; attempt++ {
		for _, step := range []string{"source", "result"} {
			id := fmt.Sprintf("%s-%d", step, attempt)
			sendProtocolProbe(t, process, nodeproto.Request{ID: id, Op: nodeproto.OpRunStep, Job: "retry", Step: step, Attempt: attempt})
			var result nodeproto.RunResult
			decodeProtocolProbe(t, protocolProbeReply(t, process, id), &result)
			if result.Error != nil || result.Outcome != "success" || string(result.Output) != fmt.Sprint(attempt-3) {
				t.Fatalf("%s reused stale output: %+v", id, result)
			}
		}
	}
	if err := process.Finish(); err != nil {
		t.Fatal(err)
	}
}
