package acceptance_test

import (
	"encoding/json"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/protofd"
	"github.com/sparkwing-dev/sparkwing/tests/acceptance/testdata/protocolgo"
)

func testGoSDKHelloAndHandles(t *testing.T) {
	bin, cleanup, err := acceptance.BuildGoSDKFixture(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, mode := range []string{"planning", "execution", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			process := startProtocolProbe(t, bin)
			hello := probeHello()
			if mode != "planning" {
				hello.Binding = &nodeproto.ProcessBinding{RunID: "r", JobID: "probe", Routes: nodeproto.ProcessRoutes{
					URL: "http://127.0.0.1:1", Bearer: "pipe-bearer-sentinel",
				}}
			}
			sendProtocolProbe(t, process, hello)
			var greeting nodeproto.HelloResult
			decodeProtocolProbe(t, protocolProbeReply(t, process, "hello"), &greeting)
			if greeting.Protocol != 2 || greeting.SDK.Language != "go" || greeting.SDK.Version == "" || greeting.Features == nil {
				t.Fatalf("hello result = %+v", greeting)
			}
			sendProtocolProbe(t, process, nodeproto.Request{ID: "describe", Op: nodeproto.OpDescribe})
			var description nodeproto.DescribeDoc
			decodeProtocolProbe(t, protocolProbeReply(t, process, "describe"), &description)
			sendProtocolProbe(t, process, nodeproto.Request{
				ID: "plan", Op: nodeproto.OpPlan, Pipeline: protocolgo.Name,
				Args: map[string]string{"name": mode}, Run: &nodeproto.RunContext{RunID: "r", Pipeline: protocolgo.Name},
			})
			var plan nodeproto.PlanDoc
			decodeProtocolProbe(t, protocolProbeReply(t, process, "plan"), &plan)
			if mode != "planning" {
				var closure string
				for _, job := range plan.Jobs {
					if job.ID == "probe" {
						closure = job.Closures.SkipIf[0]
					}
				}
				sendProtocolProbe(t, process, nodeproto.Request{ID: "eval", Op: nodeproto.OpEval, Closure: closure})
				var evaluation nodeproto.EvalResult
				decodeProtocolProbe(t, protocolProbeReply(t, process, "eval"), &evaluation)
				if string(evaluation.Value) != "false" || evaluation.Error != nil {
					t.Fatalf("skip condition = %+v", evaluation)
				}
				runProtocolProbe(t, process, mode, hello.Runner)
			}
			if err := process.Finish(); err != nil {
				t.Fatal(err)
			}
			if mode != "planning" && (process.Stdout() != "fixture stdout\n" || process.Stderr() != "fixture stderr\n") {
				t.Fatal("stdout/stderr were not separate from the protocol frames")
			}
		})
	}
	t.Run("required-run-snapshot-fields", func(t *testing.T) {
		process := startProtocolProbe(t, bin)
		sendProtocolProbe(t, process, probeHello())
		protocolProbeReply(t, process, "hello")
		for name, run := range map[string]string{
			"git-sha":     `{"run_id":"r","pipeline":"p","git":{"branch":"main"}}`,
			"git-branch":  `{"run_id":"r","pipeline":"p","git":{"sha":"commit"}}`,
			"source":      `{"run_id":"r","pipeline":"p","trigger":{}}`,
			"pull-number": `{"run_id":"r","pipeline":"p","trigger":{"source":"push","pull_request":{}}}`,
		} {
			sendProtocolProbe(t, process, map[string]any{
				"id": name, "op": "plan", "pipeline": protocolgo.Name,
				"args": map[string]string{"name": "demo"}, "run": json.RawMessage(run),
			})
			if reply := protocolProbeReply(t, process, name); reply.OK || reply.Error == nil {
				t.Fatalf("plan accepted a snapshot missing %s: %+v", name, reply)
			}
		}
		sendProtocolProbe(t, process, map[string]any{
			"id": "valid", "op": "plan", "pipeline": protocolgo.Name, "args": map[string]string{"name": "demo"},
			"run": json.RawMessage(`{"run_id":"r","pipeline":"p","git":{"sha":"","branch":"","future":null},"trigger":{"source":"","pull_request":{"number":0}},"dry_run":false,"future":null}`),
		})
		var plan nodeproto.PlanDoc
		decodeProtocolProbe(t, protocolProbeReply(t, process, "valid"), &plan)
		if len(plan.Jobs) == 0 {
			t.Fatal("valid snapshot did not reach pipeline declaration")
		}
		if err := process.Finish(); err != nil {
			t.Fatal(err)
		}
	})
}

func runProtocolProbe(t *testing.T, process *acceptance.GoSDKProcess, mode string, runner *nodeproto.RunnerIdentity) {
	t.Helper()
	sendProtocolProbe(t, process, nodeproto.Request{ID: "step", Op: nodeproto.OpRunStep, Job: "probe", Step: "run", Attempt: 1})
	line, err := process.NextFrame()
	if err != nil {
		t.Fatal(err)
	}
	var started nodeproto.FrameLog
	if err := json.Unmarshal(line, &started); err != nil || started.Message != "probe-started" {
		t.Fatalf("step start frame = %s (%v)", line, err)
	}
	if mode == "cancel" {
		sendProtocolProbe(t, process, nodeproto.Request{ID: "cancel", Op: nodeproto.OpCancel, Job: "probe", Step: "run"})
	}
	step := protocolProbeReply(t, process, "")
	if mode == "cancel" {
		next := protocolProbeReply(t, process, "")
		if step.ID == "cancel" {
			step, next = next, step
		}
		if next.ID != "cancel" || !next.OK {
			t.Fatalf("cancel reply = %+v", next)
		}
	}
	if step.ID != "step" {
		t.Fatalf("step reply ID = %q", step.ID)
	}
	var result nodeproto.RunResult
	decodeProtocolProbe(t, step, &result)
	if mode == "cancel" {
		if result.Outcome != "failed" || result.Error == nil || !strings.Contains(result.Error.Message, "canceled") {
			t.Fatalf("cancelled body = %+v", result)
		}
		return
	}
	if result.Outcome != "success" || result.Error != nil {
		t.Fatalf("probe body = %+v", result)
	}
	var observation protocolgo.Observation
	if err := json.Unmarshal(result.Output, &observation); err != nil {
		t.Fatal(err)
	}
	if observation.Runner == nil || observation.Runner.Name != runner.Name || observation.Runner.Type != runner.Type || !reflect.DeepEqual(observation.Runner.Labels, runner.Labels) {
		t.Fatalf("granted runner changed: %+v", observation.Runner)
	}
	for _, value := range append(observation.Args, observation.Env...) {
		if strings.Contains(value, "pipe-bearer-sentinel") || strings.HasPrefix(value, "SPARKWING_NODE_") || strings.HasPrefix(value, "SPARKWING_PROTOCOL_HANDLES=") {
			t.Fatal("credential or removed protocol transport escaped into argv/environment")
		}
	}
	if len(observation.Args) < 2 || observation.Args[1] != protofd.Arg {
		t.Fatalf("SDK startup argv = %v", observation.Args)
	}
	if runtime.GOOS == "windows" && (len(observation.Args) != 4 || observation.Args[2] != protofd.HandlesArg) {
		t.Fatalf("Windows startup lacks argv handles: %v", observation.Args)
	}
}

func probeHello() nodeproto.Request {
	return nodeproto.Request{
		ID: "hello", Op: nodeproto.OpHello, Protocol: 2,
		Engine: &nodeproto.EngineIdentity{Version: "fixture"},
		Runner: &nodeproto.RunnerIdentity{Name: "server-granted", Type: "actions", Labels: []string{"pool=granted", "os=" + runtime.GOOS}},
	}
}

func startProtocolProbe(t *testing.T, bin string) *acceptance.GoSDKProcess {
	t.Helper()
	process, err := acceptance.StartGoSDKProcess(t.Context(), bin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(process.Close)
	return process
}

func sendProtocolProbe(t *testing.T, process *acceptance.GoSDKProcess, request any) {
	t.Helper()
	if err := process.Send(request); err != nil {
		t.Fatal(err)
	}
}

func protocolProbeReply(t *testing.T, process *acceptance.GoSDKProcess, id string) nodeproto.Reply {
	t.Helper()
	for {
		line, err := process.NextFrame()
		if err != nil {
			t.Fatal(err)
		}
		var reply nodeproto.Reply
		if err := json.Unmarshal(line, &reply); err != nil {
			t.Fatal(err)
		}
		if reply.ID == "" {
			var log nodeproto.FrameLog
			if err := json.Unmarshal(line, &log); err != nil || log.TS == "" {
				t.Fatalf("invalid log frame: %s (%v)", line, err)
			}
			continue
		}
		if id != "" && reply.ID != id || reply.OK != (len(reply.Result) != 0) || reply.OK == (reply.Error != nil) {
			t.Fatalf("inconsistent reply: %s", line)
		}
		return reply
	}
}

func decodeProtocolProbe(t *testing.T, reply nodeproto.Reply, result any) {
	t.Helper()
	if !reply.OK {
		t.Fatalf("%s failed: %+v", reply.ID, reply.Error)
	}
	if err := json.Unmarshal(reply.Result, result); err != nil {
		t.Fatal(err)
	}
}

func requireProtocolCode(t *testing.T, reply nodeproto.Reply, code apierr.Code) {
	t.Helper()
	if reply.OK || reply.Error == nil || reply.Error.Code != code || reply.Error.Message == "" {
		t.Fatalf("reply = %+v, want %s", reply, code)
	}
}
