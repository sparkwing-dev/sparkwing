package acceptance_test

import (
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/tests/acceptance/testdata/protocolgo"
)

func testGoSDKEnvelopeTranscripts(t *testing.T) {
	t.Run("malformed-replies", testProtocolMalformedReplies)
	t.Run("correlation", testProtocolReplyCorrelation)
	t.Run("eval-value-types", testProtocolEvalValueTypes)
	bin, cleanup, err := acceptance.BuildGoSDKFixture(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	t.Run("reused-request-id", func(t *testing.T) {
		recordLeaf(t)
		process := startProtocolProbe(t, bin)
		sendProtocolProbe(t, process, probeHello())
		protocolProbeReply(t, process, "hello")
		sendProtocolProbe(t, process, map[string]any{"id": "hello", "op": "describe"})
		if frame, err := process.NextFrame(); err != io.EOF {
			t.Fatalf("reused request produced %s, %v", frame, err)
		}
		if err := process.Finish(); err == nil || !strings.Contains(err.Error(), "duplicate request ID") {
			t.Fatalf("reused request did not fail the session: %v", err)
		}
	})
	for _, mode := range []string{"success", "fail", "no-memo"} {
		t.Run("closures-"+mode, func(t *testing.T) {
			process, job := planEnvelopeProbe(t, bin, mode)
			for _, closure := range envelopeClosures(job) {
				t.Run(closure.kind, func(t *testing.T) {
					recordLeaf(t)
					sendProtocolProbe(t, process, nodeproto.Request{
						ID: closure.kind, Op: nodeproto.OpEval, Closure: closure.id,
						Input: json.RawMessage(`{"outcome":"success"}`),
					})
					reply := protocolProbeReply(t, process, closure.kind)
					var result nodeproto.EvalResult
					if err := reply.Decode(&result); err != nil || result.ValidateValue(closure.kind) != nil {
						t.Fatalf("invalid %s result: %+v, %v", closure.kind, result, err)
					}
					if mode == "fail" {
						if string(result.Value) != "null" || result.Error == nil || result.Error.Code != apierr.SDKError || result.Error.Message != "fixture closure failed" {
							t.Fatalf("closure failure = %+v", result)
						}
						return
					}
					want := closure.value
					if mode == "no-memo" && closure.kind == "memo_key" {
						want = "null"
					}
					if result.Error != nil || want != "" && string(result.Value) != want {
						t.Fatalf("closure result = %+v; want %s", result, want)
					}
					if closure.kind == "generate" {
						var jobs []nodeproto.JobSpec
						if err := json.Unmarshal(result.Value, &jobs); err != nil || len(jobs) != 1 || jobs[0].ID != "generated" || !reflect.DeepEqual(jobs[0].Needs, []string{"envelope"}) {
							t.Fatalf("generation result = %s, %v", result.Value, err)
						}
					}
				})
			}
			testEnvelopeInvocationResults(t, process, mode)
			if err := process.Finish(); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("targeted-eval-cancel", func(t *testing.T) {
		recordLeaf(t)
		testEnvelopeCancel(t, bin)
	})
}

func testProtocolMalformedReplies(t *testing.T) {
	for name, raw := range map[string]string{
		"both":              `{"reply":"1","ok":true,"result":{},"error":{"code":"sdk_error","message":"bad"}}`,
		"neither":           `{"reply":"1","ok":false}`,
		"true-with-error":   `{"reply":"1","ok":true,"error":{"code":"sdk_error","message":"bad"}}`,
		"false-with-result": `{"reply":"1","ok":false,"result":{}}`,
		"double-error":      `{"reply":"1","ok":false,"error":{"error":{"code":"sdk_error","message":"bad"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			recordLeaf(t)
			var tracker nodeproto.Tracker
			id := tracker.Start(nodeproto.OpEval)
			if frame, err := nodeproto.ParseFrame([]byte(raw)); !errors.Is(err, nodeproto.ErrMalformed) || frame.Reply != nil {
				t.Fatalf("malformed reply accepted: %+v, %v", frame, err)
			}
			if pending := tracker.Pending(); len(pending) != 1 || pending[id] != nodeproto.OpEval {
				t.Fatalf("malformed reply completed a request: %v", pending)
			}
		})
	}
}

func testProtocolReplyCorrelation(t *testing.T) {
	recordLeaf(t)
	property := func(suffix string) bool {
		var tracker nodeproto.Tracker
		id := tracker.Start(nodeproto.OpEval)
		raw, err := json.Marshal(nodeproto.Reply{ID: "unsolicited-" + suffix, OK: true, Result: json.RawMessage(`{"value":true}`)})
		if err != nil {
			t.Fatal(err)
		}
		frame, err := nodeproto.ParseFrame(raw)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tracker.Resolve(*frame.Reply); !errors.Is(err, nodeproto.ErrUnknownReply) || len(tracker.Pending()) != 1 {
			return false
		}
		reply := nodeproto.Reply{ID: id, OK: true, Result: json.RawMessage(`{"value":true}`)}
		op, err := tracker.Resolve(reply)
		if err != nil || op != nodeproto.OpEval || len(tracker.Pending()) != 0 {
			return false
		}
		_, err = tracker.Resolve(reply)
		return errors.Is(err, nodeproto.ErrDuplicateReply) && len(tracker.Pending()) == 0
	}
	if err := quick.Check(property, nil); err != nil {
		t.Fatal(err)
	}
}

func testProtocolEvalValueTypes(t *testing.T) {
	for kind, valid := range map[string][]string{
		"skip_if": {"true", "false"}, "memo_key": {"true", "false", `"key"`, "null", "[]", "{}", "1"},
		"before_run": {"null"}, "after_run": {"null"}, "verify": {"null"},
		"generate": {"[]"},
	} {
		t.Run(kind, func(t *testing.T) {
			recordLeaf(t)
			for _, value := range []string{"true", "false", `"key"`, `""`, "null", "[]", "{}", "1"} {
				var result nodeproto.EvalResult
				if err := (nodeproto.Reply{ID: "eval", OK: true, Result: json.RawMessage(`{"value":` + value + `}`)}).Decode(&result); err != nil {
					t.Fatal(err)
				}
				want := false
				for _, candidate := range valid {
					want = want || candidate == value
				}
				if err := result.ValidateValue(kind); (err == nil) != want {
					t.Fatalf("%s value %s: %v; want valid=%t", kind, value, err, want)
				}
				result.Error = &apierr.Error{Code: apierr.SDKError, Message: "closure failed"}
				if err := result.ValidateValue(kind); (err == nil) != (value == "null") {
					t.Fatalf("failed %s value %s: %v", kind, value, err)
				}
			}
		})
	}
}

func planEnvelopeProbe(t *testing.T, bin, mode string) (*acceptance.GoSDKProcess, nodeproto.JobSpec) {
	t.Helper()
	process := startProtocolProbe(t, bin)
	hello := probeHello()
	hello.Binding = &nodeproto.ProcessBinding{RunID: "r", JobID: "envelope", Routes: nodeproto.ProcessRoutes{URL: "http://127.0.0.1:1", Bearer: "fixture-bearer"}}
	sendProtocolProbe(t, process, hello)
	protocolProbeReply(t, process, "hello")
	sendProtocolProbe(t, process, nodeproto.Request{
		ID: "plan", Op: nodeproto.OpPlan, Pipeline: protocolgo.EnvelopeName,
		Args: map[string]string{"name": mode}, Run: &nodeproto.RunContext{RunID: "r", Pipeline: "invoked-envelope"},
	})
	var doc nodeproto.PlanDoc
	if err := protocolProbeReply(t, process, "plan").Decode(&doc); err != nil || len(doc.Jobs) != 1 {
		t.Fatalf("envelope plan: %+v, %v", doc, err)
	}
	return process, doc.Jobs[0]
}

type envelopeClosure struct{ kind, id, value string }

func envelopeClosures(job nodeproto.JobSpec) []envelopeClosure {
	return []envelopeClosure{
		{"skip_if", job.Closures.SkipIf[0], "true"},
		{"before_run", job.Closures.BeforeRun[0], "null"},
		{"verify", job.Closures.Verify, "null"},
		{"after_run", job.Closures.AfterRun[0], "null"},
		{"memo_key", job.Closures.MemoKey, `"fixture-memo-key"`},
		{"generate", job.Closures.Generate[0], ""},
	}
}

func testEnvelopeInvocationResults(t *testing.T, process *acceptance.GoSDKProcess, mode string) {
	t.Helper()
	sendProtocolProbe(t, process, nodeproto.Request{
		ID: "plan-again", Op: nodeproto.OpPlan, Pipeline: protocolgo.EnvelopeName,
		Args: map[string]string{"name": "changed"}, Run: &nodeproto.RunContext{RunID: "r", Pipeline: "invoked-envelope"},
	})
	requireProtocolCode(t, protocolProbeReply(t, process, "plan-again"), apierr.ProtocolError)
	for _, attempt := range []int{0, -1, 1} {
		id := map[int]string{0: "zero", -1: "negative", 1: "body"}[attempt]
		sendProtocolProbe(t, process, map[string]any{"id": id, "op": "run_step", "job": "envelope", "step": "body", "attempt": attempt, "settled": []string{}})
		reply := protocolProbeReply(t, process, id)
		if attempt < 1 {
			requireProtocolCode(t, reply, apierr.BadRequest)
			continue
		}
		var result nodeproto.RunResult
		if err := reply.Decode(&result); err != nil {
			t.Fatal(err)
		}
		if mode == "fail" {
			if result.Outcome != "failed" || result.Error == nil || result.Error.Code != apierr.SDKError {
				t.Fatalf("body failure = %+v", result)
			}
		} else if result.Outcome != "success" || string(result.Output) != "null" {
			t.Fatalf("body success = %+v", result)
		}
	}
	sendProtocolProbe(t, process, nodeproto.Request{ID: "absent-step", Op: nodeproto.OpRunStep, Job: "envelope", Step: "absent", Attempt: 1, Settled: []string{}})
	requireProtocolCode(t, protocolProbeReply(t, process, "absent-step"), apierr.UnknownStep)
}

func testEnvelopeCancel(t *testing.T, bin string) {
	process, job := planEnvelopeProbe(t, bin, "cancel")
	sendProtocolProbe(t, process, nodeproto.Request{ID: "sibling", Op: nodeproto.OpRunStep, Job: "envelope", Step: "sibling", Attempt: 1, Settled: []string{}})
	requireEnvelopeLog(t, process, "sibling-waiting")
	sendProtocolProbe(t, process, nodeproto.Request{ID: "memo", Op: nodeproto.OpEval, Closure: job.Closures.MemoKey})
	requireEnvelopeLog(t, process, "memo-waiting")
	sendProtocolProbe(t, process, nodeproto.Request{ID: "cancel", Op: nodeproto.OpCancel, Target: "memo"})
	var evaluation nodeproto.EvalResult
	if err := protocolProbeReply(t, process, "memo").Decode(&evaluation); err != nil || evaluation.Error == nil || string(evaluation.Value) != "null" {
		t.Fatalf("cancelled eval = %+v, %v", evaluation, err)
	}
	if reply := protocolProbeReply(t, process, "cancel"); !reply.OK {
		t.Fatalf("cancel acknowledgement = %+v", reply)
	}
	sendProtocolProbe(t, process, nodeproto.Request{ID: "release", Op: nodeproto.OpEval, Closure: job.Closures.BeforeRun[0]})
	for range 2 {
		reply := protocolProbeReply(t, process, "")
		switch reply.ID {
		case "sibling":
			var result nodeproto.RunResult
			if err := reply.Decode(&result); err != nil || result.Outcome != "success" {
				t.Fatalf("target cancellation affected sibling: %+v, %v", result, err)
			}
		case "release":
			if !reply.OK {
				t.Fatalf("release eval = %+v", reply)
			}
		default:
			t.Fatalf("unexpected reply: %+v", reply)
		}
	}
	if err := process.Finish(); err != nil {
		t.Fatal(err)
	}
}

func requireEnvelopeLog(t *testing.T, process *acceptance.GoSDKProcess, message string) {
	t.Helper()
	raw, err := process.NextFrame()
	if err != nil {
		t.Fatal(err)
	}
	frame, err := nodeproto.ParseFrame(raw)
	if err != nil || frame.Log == nil || frame.Log.Message != message {
		t.Fatalf("expected barrier log %q; got %s, %v", message, raw, err)
	}
}
