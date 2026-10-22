package acceptance_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
	"github.com/sparkwing-dev/sparkwing/tests/acceptance/testdata/protocolgo"
)

func testGoSDKRecoveryChains(t *testing.T) {
	protocolgo.Register()
	host := sparkwingtest.New(sparkwingtest.Options{})
	t.Cleanup(func() { _ = host.Close() })
	bin, cleanup, err := acceptance.BuildGoSDKFixture(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	t.Run("describe", func(t *testing.T) { testRecoveryChainDescription(t, bin) })
	t.Run("arguments", func(t *testing.T) { testRecoveryChainArguments(t, bin, host) })
	t.Run("generated", func(t *testing.T) { testGeneratedRecoveryChain(t, bin, host) })
}

func testRecoveryChainDescription(t *testing.T, bin string) {
	process := startProtocolProbe(t, bin)
	sendProtocolProbe(t, process, probeHello())
	protocolProbeReply(t, process, "hello")
	sendProtocolProbe(t, process, nodeproto.Request{ID: "describe", Op: nodeproto.OpDescribe})
	var doc nodeproto.DescribeDoc
	decodeProtocolProbe(t, protocolProbeReply(t, process, "describe"), &doc)
	found := false
	for _, pipeline := range doc.Pipelines {
		if pipeline.Name == protocolgo.RecoveryChainName {
			found = true
			want := []nodeproto.ArgumentDescription{
				{Name: "recipient", Type: "string", Required: true, JobID: "notify"},
				{Name: "notification", Type: "string", Default: new("email"), Enum: []string{"email", "chat"}, JobID: "notify"},
			}
			if !reflect.DeepEqual(pipeline.Args, want) {
				t.Errorf("nested recovery arguments = %+v, want %+v", pipeline.Args, want)
			}
		}
	}
	if !found {
		t.Error("describe omitted the recovery chain")
	}
	if err := process.Finish(); err != nil {
		t.Fatal(err)
	}
}

func testRecoveryChainArguments(t *testing.T, bin string, host sparkwingtest.Host) {
	for _, tc := range []struct {
		name, output string
		args         map[string]string
	}{
		{"default", `{"Recipient":"team","Mode":"email"}`, map[string]string{"recipient": "team"}},
		{"override", `{"Recipient":"team","Mode":"chat"}`, map[string]string{"recipient": "team", "notification": "chat"}},
		{"required", "", map[string]string{}},
		{"enum", "", map[string]string{"recipient": "team", "notification": "invalid"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake, err := host.Plan(t.Context(), protocolgo.RecoveryChainName, tc.args)
			process := startProtocolProbe(t, bin)
			hello := probeHello()
			hello.Binding = &nodeproto.ProcessBinding{RunID: "r", JobID: "notify", Routes: nodeproto.ProcessRoutes{URL: "http://127.0.0.1:1", Bearer: "fixture"}}
			sendProtocolProbe(t, process, hello)
			protocolProbeReply(t, process, "hello")
			args, prepareErr := prepareProtocolArguments(t, process, protocolgo.RecoveryChainName, tc.args)
			if tc.output == "" {
				if prepareErr == nil || err == nil || prepareErr.Error() != err.Error() {
					t.Fatalf("process preparation = %v, fake preparation = %v", prepareErr, err)
				}
			} else {
				if prepareErr != nil || err != nil {
					t.Fatalf("process preparation = %v, fake preparation = %v", prepareErr, err)
				}
				sendProtocolProbe(t, process, nodeproto.Request{ID: "plan", Op: nodeproto.OpPlan, Pipeline: protocolgo.RecoveryChainName, Args: args, Run: &nodeproto.RunContext{RunID: "r", Pipeline: protocolgo.RecoveryChainName}})
				reply := protocolProbeReply(t, process, "plan")
				var doc nodeproto.PlanDoc
				decodeProtocolProbe(t, reply, &doc)
				assertRecoveryChain(t, doc.Jobs, false)
				if !reflect.DeepEqual(fake, doc) {
					t.Error("fake and process recovery chains differ")
				}
				sendProtocolProbe(t, process, nodeproto.Request{ID: "body", Op: nodeproto.OpRunStep, Job: "notify", Step: "run", Attempt: 1, Settled: []string{}})
				var result nodeproto.RunResult
				decodeProtocolProbe(t, protocolProbeReply(t, process, "body"), &result)
				if result.Outcome != "success" || result.Error != nil || string(result.Output) != tc.output {
					t.Errorf("nested recovery result = %+v, want %s", result, tc.output)
				}
			}
			if err := process.Finish(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func testGeneratedRecoveryChain(t *testing.T, bin string, host sparkwingtest.Host) {
	plan := sparkwing.NewPlan()
	if err := (protocolgo.GeneratedRecoveryChainPipeline{}).Plan(t.Context(), plan, protocolgo.GeneratedRecoveryInputs{}, sparkwing.RunContext{}); err != nil {
		t.Fatal(err)
	}
	fake, err := host.Eval(t.Context(), plan, "source", "source/generate/0", json.RawMessage("null"))
	if err != nil {
		t.Fatalf("fake expansion = %v", err)
	}
	var jobs []nodeproto.JobSpec
	if err := json.Unmarshal(fake, &jobs); err != nil {
		t.Fatal(err)
	}
	assertRecoveryChain(t, jobs, true)
	for _, tc := range []struct {
		name, output string
		args         map[string]string
	}{
		{"override", `{"Mode":"chat"}`, map[string]string{"notification": "chat"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			process := startProtocolProbe(t, bin)
			hello := probeHello()
			hello.Binding = &nodeproto.ProcessBinding{RunID: "r", JobID: "notify", Routes: nodeproto.ProcessRoutes{URL: "http://127.0.0.1:1", Bearer: "fixture"}}
			sendProtocolProbe(t, process, hello)
			protocolProbeReply(t, process, "hello")
			sendProtocolProbe(t, process, nodeproto.Request{ID: "plan", Op: nodeproto.OpPlan, Pipeline: protocolgo.GeneratedRecoveryChainName, Args: tc.args, Run: &nodeproto.RunContext{RunID: "r", Pipeline: protocolgo.GeneratedRecoveryChainName}})
			protocolProbeReply(t, process, "plan")
			sendProtocolProbe(t, process, nodeproto.Request{ID: "expand", Op: nodeproto.OpEval, Closure: "source/generate/0", Input: json.RawMessage("null")})
			var result nodeproto.EvalResult
			decodeProtocolProbe(t, protocolProbeReply(t, process, "expand"), &result)
			if result.Error != nil || !reflect.DeepEqual(result.Value, fake) {
				t.Errorf("process expansion = %+v, want %s", result, fake)
			}
			sendProtocolProbe(t, process, nodeproto.Request{ID: "body", Op: nodeproto.OpRunStep, Job: "notify", Step: "run", Attempt: 1, Settled: []string{}})
			reply := protocolProbeReply(t, process, "body")
			var body nodeproto.RunResult
			decodeProtocolProbe(t, reply, &body)
			if body.Outcome != "success" || body.Error != nil || string(body.Output) != tc.output {
				t.Errorf("generated recovery body = %+v, want %s", body, tc.output)
			}
			if err := process.Finish(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func assertRecoveryChain(t *testing.T, jobs []nodeproto.JobSpec, generated bool) {
	t.Helper()
	if len(jobs) != 3 {
		t.Fatalf("recovery chain has %d jobs, want 3: %+v", len(jobs), jobs)
	}
	for i, want := range []string{"approve", "recover", "notify"} {
		parent := ""
		if i > 0 {
			parent = jobs[i-1].ID
		}
		if jobs[i].ID != want || jobs[i].OnFailureOf != parent {
			t.Errorf("recovery %d = %+v, want %s recovering %s", i, jobs[i], want, parent)
		}
		if generated && (!reflect.DeepEqual(jobs[i].Groups, []string{"source/generate/0"}) || !reflect.DeepEqual(jobs[i].Needs, []string{"source"})) {
			t.Errorf("generated recovery lost source ancestry: %+v", jobs[i])
		}
	}
}
