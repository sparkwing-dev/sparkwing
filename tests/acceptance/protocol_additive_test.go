package acceptance_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/tests/acceptance/testdata/protocolgo"
)

func testGoSDKAdditiveFieldsAndChildHandles(t *testing.T) {
	bin, cleanup, err := acceptance.BuildGoSDKFixture(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	process := startProtocolProbe(t, bin)
	sendProtocolProbe(t, process, map[string]any{
		"id": "hello", "op": "hello", "protocol": 2, "future": nil,
		"engine": map[string]any{"version": "fixture", "future": true},
		"runner": map[string]any{"name": "local", "type": "local", "labels": []string{}, "future": []int{1}},
		"binding": map[string]any{
			"run_id": "r", "job_id": "child", "future": false,
			"routes": map[string]any{"url": "http://127.0.0.1:1", "bearer": "child-bearer-sentinel", "future": "route"},
		},
	})
	var hello nodeproto.HelloResult
	if err := protocolProbeReply(t, process, "hello").Decode(&hello); err != nil {
		t.Fatal(err)
	}
	if hello.Protocol != nodeproto.Version || hello.Features == nil {
		t.Fatalf("additive hello = %+v", hello)
	}
	for _, feature := range hello.Features {
		t.Fatalf("add a transcript for advertised optional operation %q", feature)
	}
	sendProtocolProbe(t, process, map[string]any{
		"id": "plan", "op": "plan", "pipeline": protocolgo.ChildHandlesName,
		"args": map[string]string{"name": "child"}, "run": map[string]any{"run_id": "r", "pipeline": "invoked-child", "future": true},
		"future": map[string]any{"nested": nil},
	})
	var plan nodeproto.PlanDoc
	if err := protocolProbeReply(t, process, "plan").Decode(&plan); err != nil || len(plan.Jobs) != 1 || plan.Jobs[0].ID != "child" {
		t.Fatalf("additive plan request = %+v, %v", plan, err)
	}
	sendProtocolProbe(t, process, map[string]any{
		"id": "child", "op": "run_step", "job": "child", "step": "run", "attempt": 1, "settled": []string{}, "future": []any{false, nil},
	})
	requireEnvelopeLog(t, process, "child-exited")
	var result nodeproto.RunResult
	if err := protocolProbeReply(t, process, "child").Decode(&result); err != nil || result.Outcome != "success" || result.Error != nil {
		t.Fatalf("child body = %+v, %v", result, err)
	}
	var child protocolgo.ChildHandleObservation
	if err := json.Unmarshal(result.Output, &child); err != nil {
		t.Fatal(err)
	}
	if child.RequestOpen || child.FrameOpen || child.Env == nil {
		t.Fatalf("child inherited protocol handles or did not run: %+v", child)
	}
	for _, value := range child.Env {
		if strings.Contains(value, "child-bearer-sentinel") || strings.HasPrefix(value, "SPARKWING_NODE_") || strings.HasPrefix(value, "SPARKWING_PROTOCOL_HANDLES=") {
			t.Fatal("child inherited a credential or removed protocol transport")
		}
	}
	sendProtocolProbe(t, process, nodeproto.Request{ID: "after-child", Op: nodeproto.OpDescribe})
	var description nodeproto.DescribeDoc
	if err := protocolProbeReply(t, process, "after-child").Decode(&description); err != nil || len(description.Pipelines) == 0 {
		t.Fatalf("protocol stopped after child exit: %+v, %v", description, err)
	}
	if err := process.Finish(); err != nil {
		t.Fatal(err)
	}
	if process.Stdout() != "" || process.Stderr() != "" {
		t.Fatalf("child output escaped onto SDK streams: stdout=%q stderr=%q", process.Stdout(), process.Stderr())
	}
	t.Run("additive-frames", testProtocolAdditiveFrames)
	_, err = nodeproto.ValidatePlan(json.RawMessage(`{"jobs":[],"future":true}`), nodeproto.ValidationContext{
		Placement: nodeproto.PlacementLocal, Secrets: noProbeSecrets{},
	})
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != apierr.PlanInvalid || !strings.Contains(failure.Hint, "future") {
		t.Fatalf("unknown plan field = %v", err)
	}
}

func testProtocolAdditiveFrames(t *testing.T) {
	for name, raw := range map[string]string{
		"reply": `{"reply":"hello","ok":true,"result":{"protocol":2,"sdk":{"language":"go","version":"fixture","future":{}},"features":["optional-probe"],"future":false},"future":null}`,
		"log":   `{"ts":"2026-10-14T00:00:00Z","level":"info","msg":"visible","future":{"nested":[1,null]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			frame, err := nodeproto.ParseFrame([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			if name == "log" {
				if frame.Log == nil || frame.Log.Message != "visible" {
					t.Fatalf("additive log = %+v", frame)
				}
				return
			}
			var hello nodeproto.HelloResult
			if frame.Reply == nil {
				t.Fatal("reply lost its discriminator")
			}
			if err := frame.Reply.Decode(&hello); err != nil || len(hello.Features) != 1 || hello.Features[0] != "optional-probe" {
				t.Fatalf("optional feature advertisement = %+v, %v", hello, err)
			}
		})
	}
	if _, err := nodeproto.ParseFrame([]byte(`{"ts":"2026-10-14T00:00:00Z","level":"info","msg":"visible","job":"untrusted"}`)); !errors.Is(err, nodeproto.ErrMalformed) {
		t.Fatalf("SDK job attribution accepted as an additive field: %v", err)
	}
}
