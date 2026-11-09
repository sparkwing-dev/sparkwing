package acceptance_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"github.com/sparkwing-dev/sparkwing/sparkwingtest"
	"github.com/sparkwing-dev/sparkwing/tests/acceptance/testdata/protocolgo"
)

func testGoSDKReplayEnvironment(t *testing.T) {
	t.Setenv(protocolgo.ReplayEnvKey, "outside")
	bin, cleanup, err := acceptance.BuildGoSDKFixture(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	host := sparkwingtest.New(sparkwingtest.Options{})
	t.Cleanup(func() { _ = host.Close() })
	fake := sparkwing.NewPlan()
	inputs := protocolgo.ReplayEnvInputs{Executable: bin}
	if err := (protocolgo.ReplayEnvPipeline{}).Plan(t.Context(), fake, inputs, sparkwing.RunContext{}); err != nil {
		t.Fatal(err)
	}
	process := startProtocolProbe(t, bin)
	hello := probeHello()
	hello.Binding = &nodewire.ProcessBinding{RunID: "r", JobID: "leaf", Routes: nodewire.ProcessRoutes{URL: "http://127.0.0.1:1", Bearer: "leaf-only"}}
	sendProtocolProbe(t, process, hello)
	protocolProbeReply(t, process, "hello")
	sendProtocolProbe(t, process, nodewire.Request{
		ID: "plan", Op: nodewire.OpPlan, Pipeline: protocolgo.ReplayEnvName,
		Args: map[string]string{"executable": bin}, Run: &nodewire.RunContext{RunID: "r", Pipeline: protocolgo.ReplayEnvName},
	})
	protocolProbeReply(t, process, "plan")
	for _, source := range []string{"source", "child"} {
		t.Run(source, func(t *testing.T) {
			recordLeaf(t)
			closure := source + "/generate/0"
			value, err := host.Eval(t.Context(), fake, source, closure, json.RawMessage("null"))
			if err != nil {
				t.Errorf("fake generator applied body environment: %v", err)
			}
			sendProtocolProbe(t, process, nodewire.Request{ID: source, Op: nodewire.OpEval, Closure: closure, Input: json.RawMessage("null")})
			var result nodewire.EvalResult
			decodeProtocolProbe(t, protocolProbeReply(t, process, source), &result)
			if result.Error != nil {
				t.Errorf("process generator applied body environment: %v", result.Error)
			} else if err == nil && !reflect.DeepEqual(result.Value, value) {
				t.Errorf("fake/process expansions differ: %s, %s", value, result.Value)
			}
		})
		if t.Failed() {
			return
		}
	}
	value, err := host.Eval(t.Context(), fake, "leaf", "leaf/skip/0", nil)
	if err != nil || string(value) != "false" {
		t.Fatalf("fake condition body environment = %s, %v", value, err)
	}
	if _, err := host.RunStep(t.Context(), fake, "leaf", "run"); err != nil {
		t.Fatalf("fake step body environment: %v", err)
	}
	sendProtocolProbe(t, process, nodewire.Request{ID: "condition", Op: nodewire.OpEval, Closure: "leaf/skip/0"})
	var condition nodewire.EvalResult
	decodeProtocolProbe(t, protocolProbeReply(t, process, "condition"), &condition)
	if condition.Error != nil || string(condition.Value) != "false" {
		t.Fatalf("process condition body environment = %+v", condition)
	}
	sendProtocolProbe(t, process, nodewire.Request{ID: "body", Op: nodewire.OpRunStep, Job: "leaf", Step: "run", Attempt: 1, Settled: []string{}})
	var body nodewire.RunResult
	decodeProtocolProbe(t, protocolProbeReply(t, process, "body"), &body)
	if body.Error != nil || body.Outcome != "success" {
		t.Fatalf("process step body environment = %+v", body)
	}
	if err := process.Finish(); err != nil {
		t.Fatal(err)
	}
}
