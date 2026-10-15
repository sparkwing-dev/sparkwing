package acceptance_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
	"github.com/sparkwing-dev/sparkwing/tests/acceptance/testdata/protocolgo"
)

func testGoSDKJobArguments(t *testing.T) {
	protocolgo.Register()
	host := sparkwingtest.New(sparkwingtest.Options{})
	t.Cleanup(func() { _ = host.Close() })
	bin, cleanup, err := acceptance.BuildGoSDKFixture(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	t.Run("describe", func(t *testing.T) {
		process := startProtocolProbe(t, bin)
		sendProtocolProbe(t, process, probeHello())
		protocolProbeReply(t, process, "hello")
		sendProtocolProbe(t, process, nodeproto.Request{ID: "describe", Op: nodeproto.OpDescribe})
		var doc nodeproto.DescribeDoc
		decodeProtocolProbe(t, protocolProbeReply(t, process, "describe"), &doc)
		for _, pipeline := range doc.Pipelines {
			if pipeline.Name != protocolgo.ArgumentsName {
				continue
			}
			want := []nodeproto.ArgumentDescription{
				{Name: "count", Type: "int", Default: new("3"), JobID: "primary"},
				{Name: "mode", Type: "string", Default: new("rollback"), Enum: []string{"rollback", "notify"}, JobID: "recovery"},
				{Name: "reason", Type: "string", Required: true, JobID: "recovery"},
			}
			if !reflect.DeepEqual(pipeline.Args, want) {
				t.Errorf("job argument descriptions = %+v, want %+v", pipeline.Args, want)
			}
			if err := process.Finish(); err != nil {
				t.Fatal(err)
			}
			return
		}
		t.Fatal("describe omitted the registered arguments pipeline")
	})
	for _, tc := range []struct {
		name, output string
		args         map[string]string
	}{
		{"default", `{"Mode":"rollback","Reason":"fixture"}`, map[string]string{"reason": "fixture"}},
		{"override", `{"Mode":"notify","Reason":"fixture"}`, map[string]string{"reason": "fixture", "mode": "notify"}},
		{"missing", "", map[string]string{}},
		{"invalid", "", map[string]string{"reason": "fixture", "mode": "invalid"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake, err := host.Plan(t.Context(), protocolgo.ArgumentsName, tc.args)
			var failure *apierr.Error
			if tc.output == "" {
				if !errors.As(err, &failure) || failure.Code != apierr.InvalidArgs {
					t.Errorf("fake invalid arguments = %v", err)
				}
			} else if err != nil {
				t.Errorf("fake valid arguments = %v", err)
			}
			process := startProtocolProbe(t, bin)
			hello := probeHello()
			hello.Binding = &nodeproto.ProcessBinding{RunID: "r", JobID: "recovery", Routes: nodeproto.ProcessRoutes{URL: "http://127.0.0.1:1", Bearer: "fixture"}}
			sendProtocolProbe(t, process, hello)
			protocolProbeReply(t, process, "hello")
			sendProtocolProbe(t, process, nodeproto.Request{ID: "plan", Op: nodeproto.OpPlan, Pipeline: protocolgo.ArgumentsName, Args: tc.args, Run: &nodeproto.RunContext{RunID: "r", Pipeline: protocolgo.ArgumentsName}})
			reply := protocolProbeReply(t, process, "plan")
			if tc.output == "" {
				requireProtocolCode(t, reply, apierr.InvalidArgs)
			} else {
				var doc nodeproto.PlanDoc
				decodeProtocolProbe(t, reply, &doc)
				if !reflect.DeepEqual(doc, fake) {
					t.Error("fake and process argument plans differ")
				}
				sendProtocolProbe(t, process, nodeproto.Request{ID: "body", Op: nodeproto.OpRunStep, Job: "recovery", Step: "run", Attempt: 1, Settled: []string{}})
				var result nodeproto.RunResult
				decodeProtocolProbe(t, protocolProbeReply(t, process, "body"), &result)
				if result.Error != nil || result.Outcome != "success" || string(result.Output) != tc.output {
					raw, _ := json.Marshal(result)
					t.Errorf("recovery output = %s, want %s", raw, tc.output)
				}
			}
			if err := process.Finish(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
